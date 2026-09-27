package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// ToolImage is a picture a tool call handed the model (read on an image
// file), as the surfaces find it on the call's final tool_call_update
// (_meta.coddy.images) and on the call's result in the transcript: the file
// name the model was told, the type it was sent as, and the copy kept with the
// session's assets. The web UI previews it from URL and PreviewURL, a
// Telegram chat is sent the Asset file; the console and an editor show only
// the call's text. The keys match the files of a user message
// (GET /coddy/sessions/{id}/messages), so one reader serves both.
type ToolImage struct {
	Name       string `json:"name"`
	MIMEType   string `json:"mime_type,omitempty"`
	Asset      string `json:"asset,omitempty"`
	URL        string `json:"url,omitempty"`
	PreviewURL string `json:"preview_url,omitempty"`
}

// AssetRoute is where coddy serve answers with a session asset
// (GET /coddy/sessions/{id}/assets/{name}).
func AssetRoute(sessionID, assetName string) string {
	return "/coddy/sessions/" + url.PathEscape(sessionID) + "/assets/" + url.PathEscape(assetName)
}

// AssetThumbnailRoute is where coddy serve answers with an asset's bounded
// preview (GET /coddy/sessions/{id}/assets/{name}/thumbnail).
func AssetThumbnailRoute(sessionID, assetName string) string {
	return AssetRoute(sessionID, assetName) + "/thumbnail"
}

// toolImageStemMax bounds the part of a copy's name taken from the file's.
const toolImageStemMax = 80

// toolImageExt names a copy by the type the model was sent, which is not
// always the file's own: a GIF goes as the PNG of its first frame.
var toolImageExt = map[string]string{
	"image/png":  ".png",
	"image/jpeg": ".jpg",
	"image/gif":  ".gif",
	"image/webp": ".webp",
}

// SaveToolImageAsset keeps a copy of a picture a tool call handed the model
// with the session's assets, read-only, under the file's name plus a digest
// of its content. The digest is what makes the copy the picture the model was
// shown: the workspace file may change after the call, and a later preview of
// that call must not follow it. A picture read again unchanged is stored once.
// It returns the copy's path and its thumbnail's, or no thumbnail when none
// could be made (a WebP, which the standard library cannot decode); with no
// session directory nothing is saved.
func SaveToolImageAsset(sessionDir, name, mimeType string, data []byte) (assetPath, thumbPath string, err error) {
	if strings.TrimSpace(sessionDir) == "" {
		return "", "", nil
	}
	base := filepath.Base(filepath.Clean(strings.TrimSpace(name)))
	if base == "." || base == string(filepath.Separator) || base == "" {
		base = "image"
	}
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	if stem == "" {
		stem = "image"
	}
	// The digest and the extension come on top of the name, and a file name
	// is 255 bytes on most file systems.
	for len(stem) > toolImageStemMax {
		_, size := utf8.DecodeLastRuneInString(stem)
		stem = stem[:len(stem)-size]
	}
	ext := toolImageExt[mimeType]
	if ext == "" {
		ext = filepath.Ext(base)
	}
	sum := sha256.Sum256(data)
	assetName := stem + "-" + hex.EncodeToString(sum[:6]) + ext

	assetsDir := AssetsPath(sessionDir)
	if err := os.MkdirAll(assetsDir, 0o755); err != nil {
		return "", "", fmt.Errorf("assets dir: %w", err)
	}
	assetPath = filepath.Join(assetsDir, assetName)
	if info, statErr := os.Lstat(assetPath); statErr != nil || !info.Mode().IsRegular() {
		if err := writeReadOnly(assetPath, data); err != nil {
			return "", "", fmt.Errorf("write asset %s: %w", assetName, err)
		}
	}
	thumbPath = AssetThumbnailPath(sessionDir, assetName)
	if info, statErr := os.Lstat(thumbPath); statErr == nil && info.Mode().IsRegular() {
		return assetPath, thumbPath, nil
	}
	thumb, ok := makeImageThumbnail(data)
	if !ok {
		return assetPath, "", nil
	}
	if err := os.MkdirAll(AssetThumbnailsPath(sessionDir), 0o755); err != nil {
		return "", "", fmt.Errorf("thumbnail dir: %w", err)
	}
	if err := writeReadOnly(thumbPath, thumb); err != nil {
		return "", "", fmt.Errorf("write thumbnail %s: %w", assetName, err)
	}
	return assetPath, thumbPath, nil
}

// ToolImagesMeta returns meta with the pictures of a tool call under
// coddy.images, the _meta of its final tool_call_update. Whatever meta already
// carries stays; with no pictures meta is returned as it was.
func ToolImagesMeta(meta map[string]interface{}, images []ToolImage) map[string]interface{} {
	if len(images) == 0 {
		return meta
	}
	if meta == nil {
		meta = map[string]interface{}{}
	}
	coddy, _ := meta["coddy"].(map[string]interface{})
	if coddy == nil {
		coddy = map[string]interface{}{}
		meta["coddy"] = coddy
	}
	coddy["images"] = append([]ToolImage(nil), images...)
	return meta
}

// ToolImagesFromMeta reads the pictures of a tool_call_update's _meta, both
// the value the agent published in this process and its JSON on the wire.
func ToolImagesFromMeta(meta map[string]interface{}) []ToolImage {
	coddy, _ := meta["coddy"].(map[string]interface{})
	raw, ok := coddy["images"]
	if !ok || raw == nil {
		return nil
	}
	if images, ok := raw.([]ToolImage); ok {
		return append([]ToolImage(nil), images...)
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var images []ToolImage
	if json.Unmarshal(data, &images) != nil || len(images) == 0 {
		return nil
	}
	return images
}
