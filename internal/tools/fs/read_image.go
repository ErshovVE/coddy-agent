package fs

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/gif"
	_ "image/jpeg" // image.DecodeConfig reads the size of a JPEG
	"image/png"
	"path/filepath"

	"github.com/EvilFreelancer/coddy-agent/internal/tooling"
)

// readImageFormats are the pictures read hands the model, by the type their
// content sniffs as, never by their name: the formats every provider Coddy
// talks to takes as an image.
var readImageFormats = map[string]string{
	"image/png":  "PNG",
	"image/jpeg": "JPEG",
	"image/gif":  "GIF",
	"image/webp": "WebP",
}

const (
	// readImageMaxBytes bounds one picture: 3.75 MiB is 5 MiB once base64
	// encoded, the most the Anthropic API takes per image. The picture stays
	// in the history the session replays to whichever provider it switches to,
	// so the strictest provider's limit is the one that holds.
	readImageMaxBytes = 3*1024*1024 + 768*1024
	// readImageMaxSide is the widest and tallest picture a provider takes:
	// the Anthropic API refuses one over 8000 pixels a side.
	readImageMaxSide = 8000
)

// readImage answers read for a file whose content sniffs as a picture: it
// hands the picture to the model through env.AttachImage and says what it is.
// A picture no provider would take is refused before it reaches the history,
// where a rejected image would fail every later request of the session. With
// no agent to take the picture the file is refused as binary, as read always
// refused it.
func readImage(argPath, path string, data []byte, kind string, env *tooling.Env) (string, error) {
	if env == nil || env.AttachImage == nil {
		return "", fmt.Errorf("read: %s %w (%s, %d bytes); read shows text files only", argPath, errBinaryFile, kind, len(data))
	}
	format := readImageFormats[kind]
	if len(data) > readImageMaxBytes {
		return "", fmt.Errorf("read: %s is a %s image of %s, more than the %s one picture may take; save a scaled-down copy and read that",
			argPath, format, formatBytes(len(data)), formatBytes(readImageMaxBytes))
	}
	w, h, err := imageSize(data, kind)
	if err != nil {
		return "", fmt.Errorf("read: %s looks like a %s image but cannot be decoded: %v", argPath, format, err)
	}
	if w > readImageMaxSide || h > readImageMaxSide {
		return "", fmt.Errorf("read: %s is a %s image of %dx%d, larger than the %d pixels a side a model takes; save a scaled-down copy and read that",
			argPath, format, w, h, readImageMaxSide)
	}

	sent, sentType, note := data, kind, ""
	if kind == "image/gif" {
		// Not every provider takes an animated GIF, and telling an animated one
		// from a still one means decoding every frame, which a small file of
		// many large frames turns into gigabytes. The first frame alone, as a
		// PNG, is a picture every provider takes.
		frame, err := gifFirstFrame(data)
		if err != nil {
			return "", fmt.Errorf("read: %s looks like a GIF image but cannot be decoded: %v", argPath, err)
		}
		if len(frame) > readImageMaxBytes {
			return "", fmt.Errorf("read: the first frame of %s is %s as a PNG, more than the %s one picture may take; save a scaled-down copy and read that",
				argPath, formatBytes(len(frame)), formatBytes(readImageMaxBytes))
		}
		sent, sentType, note = frame, "image/png", "; a GIF is shown to you as its first frame"
	}
	if err := env.AttachImage(filepath.Base(path), sentType, sent); err != nil {
		return "", fmt.Errorf("read: %s: %w", argPath, err)
	}
	return fmt.Sprintf("%s: %s image, %dx%d, %s%s. The picture is attached for you to look at.",
		argPath, format, w, h, formatBytes(len(data)), note), nil
}

// imageSize reads a picture's width and height from its header, without
// decoding its pixels.
func imageSize(data []byte, kind string) (int, int, error) {
	if kind == "image/webp" {
		return webpSize(data)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0, err
	}
	return cfg.Width, cfg.Height, nil
}

// webpSize reads a WebP's canvas size from its first chunk, which the
// standard library has no decoder for: an extended file (VP8X) states it
// outright, a lossy one (VP8) in its key frame header, a lossless one (VP8L)
// in its first bits.
func webpSize(data []byte) (int, int, error) {
	if len(data) < 30 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return 0, 0, errors.New("no WebP header")
	}
	chunk, body := string(data[12:16]), data[20:]
	switch chunk {
	case "VP8X":
		w := 1 + (int(body[4]) | int(body[5])<<8 | int(body[6])<<16)
		h := 1 + (int(body[7]) | int(body[8])<<8 | int(body[9])<<16)
		return w, h, nil
	case "VP8 ":
		if body[3] != 0x9d || body[4] != 0x01 || body[5] != 0x2a {
			return 0, 0, errors.New("no VP8 key frame")
		}
		w := int(binary.LittleEndian.Uint16(body[6:8]) & 0x3fff)
		h := int(binary.LittleEndian.Uint16(body[8:10]) & 0x3fff)
		return w, h, nil
	case "VP8L":
		if body[0] != 0x2f {
			return 0, 0, errors.New("no VP8L signature")
		}
		bits := binary.LittleEndian.Uint32(body[1:5])
		return int(bits&0x3fff) + 1, int(bits>>14&0x3fff) + 1, nil
	}
	return 0, 0, fmt.Errorf("unknown WebP chunk %q", chunk)
}

// gifFirstFrame returns the first frame of a GIF as a PNG. gif.Decode stops
// after that frame, so the rest of an animation is never decoded, and the
// frame keeps its palette: no canvas of the whole picture is allocated.
func gifFirstFrame(data []byte) ([]byte, error) {
	frame, err := gif.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, frame); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// formatBytes spells a file size the way a person reads it.
func formatBytes(n int) string {
	switch {
	case n >= 1024*1024:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	case n >= 1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	}
	return fmt.Sprintf("%d bytes", n)
}
