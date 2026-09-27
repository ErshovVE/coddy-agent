package agent

import (
	"encoding/base64"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/EvilFreelancer/coddy-agent/internal/llm"
	"github.com/EvilFreelancer/coddy-agent/internal/session"
)

// Pictures a tool call hands the model (read on an image file) ride on that
// call's result message, the one place every surface already looks: the web
// UI previews them on the call's row, a Telegram chat is sent them, the
// console and an editor show the call's text. The transcript never gains a
// message nobody typed. Only what the provider is sent moves them into a user
// message of their own (withToolImages), because an OpenAI-compatible tool
// result cannot carry an image.

// modelReadsImages reports whether the session's current model is configured
// to accept images (models[].multimodal). A missing or unknown entry fails
// closed, the rule the HTTP surface applies to prompt attachments.
func (a *Agent) modelReadsImages() bool {
	entry := a.cfg.FindModelEntry(a.state.EffectiveModelID(a.cfg))
	return entry != nil && entry.Multimodal
}

// attachToolImage is Env.AttachImage. The model is checked when the picture
// is handed over, not when the tools are listed: read is offered to every
// model, and switch_model can change the model in the middle of a turn. The
// copy with the session's assets is what the surfaces show, so a later change
// to the workspace file never changes what this call showed; a copy that
// cannot be written costs the surfaces their preview, never the model its
// picture.
func (a *Agent) attachToolImage(name, mimeType string, data []byte) error {
	if strings.TrimSpace(a.currentToolCallID) == "" {
		return fmt.Errorf("no tool call is running to attach the picture to")
	}
	if !a.modelReadsImages() {
		return fmt.Errorf("the session's model %s does not read images (models[].multimodal is not set), so the picture cannot be shown to it",
			a.state.EffectiveModelID(a.cfg))
	}
	part := llm.ImagePart{
		DataURL: "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data),
		Name:    name,
	}
	if sd := strings.TrimSpace(a.state.GetPersistedSessionDir()); sd != "" {
		asset, thumb, err := session.SaveToolImageAsset(sd, name, mimeType, data)
		if err != nil {
			a.log.Warn("the picture a tool call attached was not saved with the session's assets", "name", name, "err", err)
		}
		part.FilePath, part.ThumbnailPath = asset, thumb
	}
	a.callImages = append(a.callImages, part)
	return nil
}

// callResultMessage is toolResultMessage for the call that just returned,
// carrying the pictures it attached. A failed call keeps none, and either way
// the pictures are taken, so they never reach the next call's result.
func (a *Agent) callResultMessage(tc llm.ToolCall, result string, execErr error, callRules string) llm.Message {
	msg := toolResultMessage(tc, result, execErr, callRules)
	images := a.callImages
	a.callImages = nil
	if execErr == nil && len(images) > 0 {
		msg.ImageParts = images
	}
	return msg
}

// toolImagesForSurfaces describes the pictures of a finished call for the
// _meta of its final tool_call_update. Only a picture saved with the
// session's assets is listed: nothing else has an address a surface can load.
func toolImagesForSurfaces(sessionID string, parts []llm.ImagePart) []session.ToolImage {
	var out []session.ToolImage
	for _, p := range parts {
		if strings.TrimSpace(p.FilePath) == "" {
			continue
		}
		asset := filepath.Base(p.FilePath)
		img := session.ToolImage{
			Name:     p.Name,
			MIMEType: dataURLType(p.DataURL),
			Asset:    asset,
			URL:      session.AssetRoute(sessionID, asset),
		}
		if strings.TrimSpace(p.ThumbnailPath) != "" {
			img.PreviewURL = session.AssetThumbnailRoute(sessionID, asset)
		}
		out = append(out, img)
	}
	return out
}

// dataURLType is the media type a data URL names.
func dataURLType(dataURL string) string {
	rest, ok := strings.CutPrefix(dataURL, "data:")
	if !ok {
		return ""
	}
	if end := strings.IndexAny(rest, ";,"); end >= 0 {
		return rest[:end]
	}
	return ""
}

// withToolImages returns msgs as the provider is sent them: the pictures the
// tool results carry move into one user message right after each run of tool
// results. A tool message cannot hold an image in the OpenAI-compatible
// schema, and a user message between the results of one step would break the
// assistant(tool_calls) -> tool results adjacency strict endpoints require.
// Built from the history alone, the projection is the same bytes on every
// request, so the provider's prompt cache holds.
//
// A model that does not read images - the session switched to one after the
// read - is sent no picture at all, its prompt attachments included, and is
// told which pictures the results returned without them. The input is never
// written.
func withToolImages(msgs []llm.Message, readsImages bool) []llm.Message {
	needed := false
	for _, m := range msgs {
		if len(m.ImageParts) > 0 && (m.Role == llm.RoleTool || !readsImages) {
			needed = true
			break
		}
	}
	if !needed {
		return msgs
	}
	out := make([]llm.Message, 0, len(msgs)+2)
	var pending []llm.ImagePart
	var names []string
	flush := func() {
		if len(pending) == 0 {
			return
		}
		out = append(out, toolImagesMessage(pending, names, readsImages))
		pending, names = nil, nil
	}
	for _, m := range msgs {
		if m.Role != llm.RoleTool {
			flush()
			if !readsImages {
				m.ImageParts = nil
			}
			out = append(out, m)
			continue
		}
		for _, p := range m.ImageParts {
			pending = append(pending, p)
			names = append(names, fmt.Sprintf("%s (from call %s)", p.Name, m.ToolCallID))
		}
		m.ImageParts = nil
		out = append(out, m)
	}
	flush()
	return out
}

// toolImagesMessage is the user message that carries the pictures of one
// step to the provider, or tells a model that cannot take them what it is not
// shown.
func toolImagesMessage(parts []llm.ImagePart, names []string, readsImages bool) llm.Message {
	if !readsImages {
		return llm.Message{
			Role: llm.RoleUser,
			Content: "The tool calls above returned pictures the current model cannot be shown: " +
				strings.Join(names, ", ") + ".",
		}
	}
	return llm.Message{
		Role:       llm.RoleUser,
		Content:    "The pictures the tool calls above returned, in order:\n- " + strings.Join(names, "\n- "),
		ImageParts: parts,
	}
}
