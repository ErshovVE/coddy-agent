package llm

import (
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// A user message with a picture and a text file, the shape prompt attachments
// and the pictures a read showed (after the agent's send boundary) both take.
func userMessageWithAttachments() Message {
	return Message{
		Role:    RoleUser,
		Content: "what is on it?",
		ImageParts: []ImagePart{
			{Name: "shot.png", DataURL: "data:image/png;base64,iVBORw0KGgo="},
			{Name: "notes.txt", DataURL: "data:text/plain;base64," + base64.StdEncoding.EncodeToString([]byte("file body"))},
		},
	}
}

func TestAnthropicSendsThePicturesOfAUserMessage(t *testing.T) {
	p := newAnthropicProvider("claude-sonnet-4-5", "", "", nil, 8192, 0.7, "")
	_, conv := p.splitMessages([]Message{userMessageWithAttachments()})
	raw, err := json.Marshal(conv)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{
		`"text":"what is on it?\n\n[File: notes.txt]\nfile body"`,
		`"type":"image"`,
		`"media_type":"image/png"`,
		`"data":"iVBORw0KGgo="`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("request lacks %s: %s", want, s)
		}
	}
	if text, image := strings.Index(s, "what is on it?"), strings.Index(s, `"type":"image"`); text < 0 || text > image {
		t.Errorf("the picture comes before the text: %s", s)
	}
}

func TestAnthropicKeepsATextOnlyUserMessageAsItWas(t *testing.T) {
	p := newAnthropicProvider("claude-sonnet-4-5", "", "", nil, 8192, 0.7, "")
	_, conv := p.splitMessages([]Message{{Role: RoleUser, Content: "hi"}})
	raw, _ := json.Marshal(conv)
	if string(raw) != `[{"content":[{"text":"hi","type":"text"}],"role":"user"}]` {
		t.Errorf("a plain prompt changed on the wire: %s", raw)
	}
}

func TestCodexSendsThePicturesOfAUserMessage(t *testing.T) {
	p := newCodexProvider("gpt-5.6", filepath.Join(t.TempDir(), "auth.json"), true, "", nil, 0, "")
	params := p.buildParams([]Message{userMessageWithAttachments()}, nil)
	raw, err := json.Marshal(params.Input)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{
		`"type":"input_text"`,
		`what is on it?\n\n[File: notes.txt]\nfile body`,
		`"type":"input_image"`,
		`"image_url":"data:image/png;base64,iVBORw0KGgo="`,
		`"detail":"auto"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("request lacks %s: %s", want, s)
		}
	}
}

// The Messages API and the Responses API take PNG, JPEG, GIF and WebP pictures.
// Any other image type goes as what it is: an SVG is text and is sent as its
// source, another type is named as not sent, and so is a data URL that is not
// base64 - never dropped without a word, never a request the API refuses.
func TestPicturesAProviderCannotTakeGoAsText(t *testing.T) {
	msg := Message{Role: RoleUser, Content: "files", ImageParts: []ImagePart{
		{Name: "logo.svg", DataURL: "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte("<svg/>"))},
		{Name: "old.bmp", DataURL: "data:image/bmp;base64,Qk0="},
		{Name: "raw.png", DataURL: "data:image/png,not-base64"},
	}}
	anthropic := newAnthropicProvider("claude-sonnet-4-5", "", "", nil, 8192, 0.7, "")
	_, conv := anthropic.splitMessages([]Message{msg})
	codex := newCodexProvider("gpt-5.6", filepath.Join(t.TempDir(), "auth.json"), true, "", nil, 0, "")
	params := codex.buildParams([]Message{msg}, nil)
	for name, v := range map[string]any{"anthropic": conv, "codex": params.Input} {
		raw, _ := json.Marshal(v)
		s := string(raw)
		if strings.Contains(s, `"type":"image"`) || strings.Contains(s, `"type":"input_image"`) {
			t.Errorf("%s sends a picture it cannot take: %s", name, s)
		}
		for _, want := range []string{`[File: logo.svg]\n`, "svg/", "old.bmp", "raw.png"} {
			if !strings.Contains(s, want) {
				t.Errorf("%s does not name %s: %s", name, want, s)
			}
		}
	}
}
