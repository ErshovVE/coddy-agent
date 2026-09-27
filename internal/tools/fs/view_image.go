package fs

import (
	"context"
	"encoding/base64"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/EvilFreelancer/coddy-agent/internal/llm"
	"github.com/EvilFreelancer/coddy-agent/internal/tooling"
)

// viewImageExtToMime covers the common cases explicitly; mime.TypeByExtension
// is tried as a fallback for anything else the local system recognizes.
var viewImageExtToMime = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
}

// ViewImageTool returns the view_image built-in: base64-encodes an image
// file and queues it via env.QueueImageParts. The agent runtime delivers the
// queue as one synthetic user message once the whole tool batch has its
// results -- tool results (role "tool") cannot carry image content per the
// OpenAI-compatible chat completions schema, only "user"/"assistant"
// messages can (see deliverQueuedImages in internal/agent/react.go). read
// refuses binary files, so this is the model's only way to see an image
// from the workspace.
func ViewImageTool() *tooling.Tool {
	return &tooling.Tool{
		Definition: llm.ToolDefinition{
			Name: "view_image",
			Description: "View an image file (PNG/JPEG/GIF/WebP) as an actual picture. " +
				"Use it to inspect a screenshot, a diagram or a rendered page instead of guessing " +
				"from its name; `read` refuses binary files. The image is shown to you in a message " +
				"right after this batch of tool calls; to compare several, view each with its own " +
				"call in the same batch.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{
						"type":        "string",
						"description": "Path to an image file (absolute or relative to working directory)",
					},
				},
				"required": []string{"path"},
			},
		},
		Execute: executeViewImage,
	}
}

type viewImageArgs struct {
	Path string `json:"path"`
}

func executeViewImage(_ context.Context, argsJSON string, env *tooling.Env) (string, error) {
	args, err := tooling.ParseArgs[viewImageArgs](argsJSON)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(args.Path) == "" {
		return "", fmt.Errorf("view_image: path is required")
	}
	if env == nil || env.QueueImageParts == nil {
		return "", fmt.Errorf("view_image: not available in this runtime")
	}

	path := ResolvePath(args.Path, env.CWD)
	ext := strings.ToLower(filepath.Ext(path))
	mimeType := viewImageExtToMime[ext]
	if mimeType == "" {
		mimeType = mime.TypeByExtension(ext)
	}
	if !strings.HasPrefix(mimeType, "image/") {
		return "", fmt.Errorf("view_image: %q is not a recognized image type (expected .png/.jpg/.jpeg/.gif/.webp)", ext)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("view_image: %w", err)
	}

	dataURL := fmt.Sprintf("data:%s;base64,%s", mimeType, base64.StdEncoding.EncodeToString(data))
	if err := env.QueueImageParts([]llm.ImagePart{{
		DataURL:  dataURL,
		Name:     filepath.Base(path),
		FilePath: path,
	}}); err != nil {
		return "", fmt.Errorf("view_image: %w", err)
	}

	return fmt.Sprintf("Queued %s -- it will be shown to you as an image in the next message.", filepath.Base(path)), nil
}
