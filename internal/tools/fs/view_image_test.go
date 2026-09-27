package fs

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EvilFreelancer/coddy-agent/internal/llm"
	"github.com/EvilFreelancer/coddy-agent/internal/tooling"
)

// The signatures http.DetectContentType recognizes: enough bytes for the
// tool, which tells the type by content, not by name.
var (
	pngHeader  = []byte("\x89PNG\r\n\x1a\n")
	jpegHeader = []byte("\xff\xd8\xff\xe0")
	gifHeader  = []byte("GIF89a")
	webpHeader = []byte("RIFF\x00\x00\x00\x00WEBPVP8 ")
)

func viewImageEnv(cwd string, queued *[]llm.ImagePart) *tooling.Env {
	return &tooling.Env{
		CWD: cwd,
		QueueImageParts: func(parts []llm.ImagePart) error {
			*queued = append(*queued, parts...)
			return nil
		},
	}
}

func TestViewImageQueuesTheFileAsADataURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		file     string
		content  []byte
		wantMime string
	}{
		{name: "png", file: "shot.png", content: pngHeader, wantMime: "image/png"},
		{name: "jpeg", file: "photo.JPEG", content: jpegHeader, wantMime: "image/jpeg"},
		{name: "gif", file: "anim.gif", content: gifHeader, wantMime: "image/gif"},
		{name: "webp", file: "page.webp", content: webpHeader, wantMime: "image/webp"},
		{name: "content wins over the name", file: "misnamed.jpg", content: pngHeader, wantMime: "image/png"},
		{name: "no extension", file: "capture", content: pngHeader, wantMime: "image/png"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, tt.file), tt.content, 0o644); err != nil {
				t.Fatal(err)
			}
			var queued []llm.ImagePart

			out, err := executeViewImage(context.Background(), `{"path":"`+tt.file+`"}`, viewImageEnv(root, &queued))

			if err != nil {
				t.Fatalf("executeViewImage: %v", err)
			}
			if len(queued) != 1 {
				t.Fatalf("queued %d parts, want 1", len(queued))
			}
			want := "data:" + tt.wantMime + ";base64," + base64.StdEncoding.EncodeToString(tt.content)
			if queued[0].DataURL != want {
				t.Errorf("DataURL = %q, want %q", queued[0].DataURL, want)
			}
			if queued[0].Name != tt.file {
				t.Errorf("Name = %q, want %q", queued[0].Name, tt.file)
			}
			if queued[0].FilePath != filepath.Join(root, tt.file) {
				t.Errorf("FilePath = %q, want the path resolved against the working directory", queued[0].FilePath)
			}
			if !strings.Contains(out, tt.file) {
				t.Errorf("result %q does not name the queued file", out)
			}
		})
	}
}

func TestViewImageRefusesWithoutQueueing(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("text"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"shot.png":    string(pngHeader),
		"renamed.png": "just text",
		"icon.bmp":    "BM\x00\x00\x00\x00",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name    string
		args    string
		noQueue bool
		refuses bool
		wantErr string
	}{
		{name: "empty path", args: `{"path":"  "}`, wantErr: "path is required"},
		{name: "not an image", args: `{"path":"notes.txt"}`, wantErr: "is not a PNG, JPEG, GIF or WebP image"},
		{name: "text renamed to .png", args: `{"path":"renamed.png"}`, wantErr: "reads as text/plain"},
		{name: "an image format providers do not take", args: `{"path":"icon.bmp"}`, wantErr: "reads as image/bmp"},
		{name: "missing file", args: `{"path":"absent.png"}`, wantErr: "absent.png"},
		{name: "no session to deliver to", args: `{"path":"shot.png"}`, noQueue: true, wantErr: "not available"},
		{name: "the model does not read images", args: `{"path":"shot.png"}`, refuses: true, wantErr: "does not read images"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var queued []llm.ImagePart
			env := viewImageEnv(root, &queued)
			if tt.noQueue {
				env.QueueImageParts = nil
			}
			if tt.refuses {
				env.QueueImageParts = func([]llm.ImagePart) error { return errors.New("the current model does not read images") }
			}

			_, err := executeViewImage(context.Background(), tt.args, env)

			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want one containing %q", err, tt.wantErr)
			}
			if len(queued) != 0 {
				t.Errorf("queued %d parts on a refused call", len(queued))
			}
		})
	}
}

func TestViewImageIsABuiltin(t *testing.T) {
	t.Parallel()

	var names []string
	RegisterBuiltins(func(tool *tooling.Tool) { names = append(names, tool.Definition.Name) })

	for _, name := range names {
		if name == "view_image" {
			return
		}
	}
	t.Fatalf("view_image missing from the built-ins: %v", names)
}
