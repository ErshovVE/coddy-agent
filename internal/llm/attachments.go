package llm

import (
	"fmt"
	"strings"
)

// pictureTypes are the picture types the Anthropic Messages API and the
// OpenAI Responses API take as an image.
var pictureTypes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
}

// What an attached part is to a provider that takes only pictureTypes.
const (
	attachedPicture    = iota // sent as a picture
	attachedText              // written out as a labelled text block
	attachedUnsendable        // a picture it cannot take, named instead
)

// sortAttachment sorts a part for a provider that takes only pictureTypes: a
// base64 data URL of one of them, or an https address, is a picture (with its
// type and base64 payload for a data URL); a data URL of anything else but a
// picture is text to write out, an SVG included, since it is text; any other
// picture - another type, a data URL that is not base64 - cannot be sent.
func sortAttachment(ip ImagePart) (kind int, mime, payload string) {
	if !strings.HasPrefix(ip.DataURL, "data:") {
		if strings.HasPrefix(ip.DataURL, "https://") {
			return attachedPicture, "", ""
		}
		return attachedUnsendable, "", ""
	}
	mime = dataURLMIME(ip.DataURL)
	if !strings.HasPrefix(mime, "image/") || mime == "image/svg+xml" {
		return attachedText, mime, ""
	}
	comma := strings.IndexByte(ip.DataURL, ',')
	if comma < 0 || !strings.Contains(ip.DataURL[:comma], ";base64") || !pictureTypes[mime] {
		return attachedUnsendable, mime, ""
	}
	return attachedPicture, mime, ip.DataURL[comma+1:]
}

// attachmentText is the text a part that is not sent as a picture adds to its
// message: the file written out, or a line naming a picture the provider
// cannot take, so the model is never left to guess what came with a prompt.
func attachmentText(ip ImagePart, kind int, mime string) string {
	label := ip.Name
	if label == "" {
		label = "file"
	}
	if kind == attachedUnsendable {
		if mime == "" {
			mime = "picture"
		}
		return fmt.Sprintf("\n\n[File: %s: a %s this provider cannot be sent as a picture]", label, mime)
	}
	return fmt.Sprintf("\n\n[File: %s]\n%s", label, decodeDataURL(ip.DataURL))
}
