package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EvilFreelancer/coddy-agent/internal/acp"
	"github.com/EvilFreelancer/coddy-agent/internal/config"
	"github.com/EvilFreelancer/coddy-agent/internal/llm"
	"github.com/EvilFreelancer/coddy-agent/internal/session"
)

func imagePart(name string) llm.ImagePart {
	return llm.ImagePart{DataURL: "data:image/png;base64,AAAA" + name, Name: name}
}

func toolResultWith(id string, images ...llm.ImagePart) llm.Message {
	return llm.Message{Role: llm.RoleTool, ToolCallID: id, Content: "result of " + id, ImageParts: images}
}

func TestWithToolImagesMovesThePicturesAfterEachRunOfResults(t *testing.T) {
	history := []llm.Message{
		{Role: llm.RoleUser, Content: "look"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "r1", Name: "read"}, {ID: "g1", Name: "glob"}, {ID: "r2", Name: "read"}}},
		toolResultWith("r1", imagePart("a.png")),
		toolResultWith("g1"),
		toolResultWith("r2", imagePart("b.png")),
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "r3", Name: "read"}}},
		toolResultWith("r3", imagePart("c.png")),
		{Role: llm.RoleAssistant, Content: "seen"},
	}
	before, _ := json.Marshal(history)

	out := withToolImages(history, true)

	var roles []string
	for _, m := range out {
		roles = append(roles, string(m.Role))
	}
	want := "user,assistant,tool,tool,tool,user,assistant,tool,user,assistant"
	if strings.Join(roles, ",") != want {
		t.Fatalf("roles = %s, want %s", strings.Join(roles, ","), want)
	}
	for i, m := range out {
		if m.Role == llm.RoleTool && len(m.ImageParts) > 0 {
			t.Errorf("message %d: a tool result still carries %d image(s)", i, len(m.ImageParts))
		}
	}
	if got := imageNames(out[5]); strings.Join(got, ",") != "a.png,b.png" {
		t.Errorf("first step's pictures = %v, want [a.png b.png]", got)
	}
	if got := imageNames(out[8]); strings.Join(got, ",") != "c.png" {
		t.Errorf("second step's pictures = %v, want [c.png]", got)
	}
	for _, name := range []string{"a.png", "r1", "b.png", "r2"} {
		if !strings.Contains(out[5].Content, name) {
			t.Errorf("the pictures message %q does not name %s", out[5].Content, name)
		}
	}
	if after, _ := json.Marshal(history); string(after) != string(before) {
		t.Error("the history itself was changed")
	}
	// Built from the history alone: every request replays it byte for byte.
	again, _ := json.Marshal(withToolImages(history, true))
	if first, _ := json.Marshal(out); string(first) != string(again) {
		t.Error("two requests over the same history differ")
	}
}

func TestWithToolImagesLeavesAHistoryWithoutPicturesAlone(t *testing.T) {
	history := []llm.Message{
		{Role: llm.RoleUser, Content: "look", ImageParts: []llm.ImagePart{imagePart("attached.png")}},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "g1", Name: "glob"}}},
		toolResultWith("g1"),
	}
	out := withToolImages(history, true)
	if len(out) != len(history) || &out[0] != &history[0] {
		t.Error("a history whose tool results carry no picture was copied")
	}
}

func TestWithToolImagesTellsAModelWithoutImagesThePicturesAreLeftOut(t *testing.T) {
	history := []llm.Message{
		{Role: llm.RoleUser, Content: "look", ImageParts: []llm.ImagePart{imagePart("attached.png")}},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "r1", Name: "read"}}},
		toolResultWith("r1", imagePart("a.png")),
		{Role: llm.RoleAssistant, Content: "seen"},
	}
	out := withToolImages(history, false)
	for i, m := range out {
		if len(m.ImageParts) > 0 {
			t.Errorf("message %d (%s) still carries %d image(s) for a model that does not read them", i, m.Role, len(m.ImageParts))
		}
	}
	if len(out) != 5 || out[3].Role != llm.RoleUser || !strings.Contains(out[3].Content, "a.png") {
		t.Fatalf("no note after the results names the picture left out: %+v", out)
	}
	if len(history[0].ImageParts) != 1 || len(history[2].ImageParts) != 1 {
		t.Error("the history itself lost its pictures")
	}
}

func TestCallResultMessageKeepsThePicturesOfASuccessfulCallOnly(t *testing.T) {
	a := &Agent{}
	tc := llm.ToolCall{ID: "r1", Name: "read"}

	a.callImages = []llm.ImagePart{imagePart("a.png")}
	if msg := a.callResultMessage(tc, "ok", nil, ""); len(msg.ImageParts) != 1 {
		t.Errorf("a successful call's result carries %d image(s), want 1", len(msg.ImageParts))
	}
	if len(a.callImages) != 0 {
		t.Error("the pictures were not taken off the call")
	}

	a.callImages = []llm.ImagePart{imagePart("a.png")}
	if msg := a.callResultMessage(tc, "", errors.New("boom"), ""); len(msg.ImageParts) != 0 {
		t.Errorf("a failed call's result carries %d image(s)", len(msg.ImageParts))
	}
	if len(a.callImages) != 0 {
		t.Error("a failed call left its pictures for the next one")
	}
}

func TestEvictionDropsThePictureOfAReadItCollapses(t *testing.T) {
	cwd := t.TempDir()
	read := func(id, path string) llm.ToolCall {
		b, _ := json.Marshal(map[string]string{"path": path})
		return llm.ToolCall{ID: id, Name: "read", InputJSON: string(b)}
	}
	big := imagePart("old.png")
	big.DataURL = "data:image/png;base64," + strings.Repeat("A", 4096)
	history := []llm.Message{
		{Role: llm.RoleUser, Content: "look"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{read("r1", "old.png")}},
		// The text of an image read is a line; the picture is what fills the context.
		{Role: llm.RoleTool, ToolCallID: "r1", Content: "old.png: PNG image", ImageParts: []llm.ImagePart{big}},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{read("r2", "new.txt")}},
		{Role: llm.RoleTool, ToolCallID: "r2", Content: strings.Repeat("x", 4096)},
	}
	out := pruneToolResults(history, resultEvictionOptions{Enabled: true, KeepRecent: 1, MinResultBytes: 1024, CWD: cwd})
	if !strings.HasPrefix(out[2].Content, "[evicted:") {
		t.Fatalf("the image read was not evicted: %q", out[2].Content)
	}
	if len(out[2].ImageParts) != 0 {
		t.Error("the evicted read still carries its picture")
	}
	if len(history[2].ImageParts) != 1 {
		t.Error("eviction changed the history itself")
	}
}

func TestReadOfAnImageIsRefusedForAModelThatDoesNotReadImages(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "shot.png"), testPNG(4, 3, color.Black), 0o644); err != nil {
		t.Fatal(err)
	}
	sessionDir := filepath.Join(dir, ".session")
	cfg := &config.Config{
		Providers: []config.ProviderConfig{{Name: "fake", Type: "openai", APIKey: "test"}},
		Models:    []config.ModelEntry{{Model: "fake/text", MaxTokens: 100, MaxContextTokens: 128000}},
		Agent:     config.Agent{Model: "fake/text"},
		Tools:     config.Tools{PermissionMode: config.PermModeBypass},
	}
	st := &session.State{ID: "sess_text_model", CWD: dir, Mode: session.ModeAgent, SessionDir: sessionDir}
	provider := &evScriptProvider{steps: []evStep{{calls: []llm.ToolCall{tcReadPath("r1", "shot.png")}}, {text: "answer"}}}
	ag := NewAgent(cfg, st, resumePermissionSender{}, nil)
	ag.providerFactory = func(llm.ProviderInput) (llm.Provider, error) { return provider, nil }
	if _, err := ag.Run(context.Background(), []acp.ContentBlock{{Type: "text", Text: "look"}}); err != nil {
		t.Fatal(err)
	}

	last := provider.streamSeen[len(provider.streamSeen)-1]
	var result string
	for _, m := range last {
		if len(m.ImageParts) > 0 {
			t.Errorf("a %s message carries %d image(s) for a model that does not read them", m.Role, len(m.ImageParts))
		}
		if m.Role == llm.RoleTool && m.ToolCallID == "r1" {
			result = m.Content
		}
	}
	if !strings.Contains(result, "does not read images") || !strings.Contains(result, "fake/text") {
		t.Errorf("tool result %q does not say the model fake/text does not read images", result)
	}
	if entries, _ := os.ReadDir(session.AssetsPath(sessionDir)); len(entries) > 0 {
		t.Errorf("a refused picture was saved with the assets: %v", entries)
	}
}

// Pictures stay in the history and go out with every request. A request
// carries only the newest of them, at most toolImagesMaxCount and
// toolImagesMaxBytes of data, so a session that read many screenshots keeps
// fitting what a provider takes (Anthropic: 32 MB a request, and 2000 pixels a
// side once it holds more than 20 pictures); the older ones are named as left
// out.
func TestWithToolImagesSendsOnlyTheNewestPicturesARequestCanHold(t *testing.T) {
	var history []llm.Message
	history = append(history, llm.Message{Role: llm.RoleUser, Content: "look"})
	for i := 0; i < toolImagesMaxCount+2; i++ {
		id := fmt.Sprintf("r%02d", i)
		history = append(history,
			llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: id, Name: "read"}}},
			toolResultWith(id, imagePart(fmt.Sprintf("p%02d.png", i))))
	}
	out := withToolImages(history, true)
	sent := 0
	var leftOut []string
	for _, m := range out {
		sent += len(m.ImageParts)
		if m.Role == llm.RoleUser && strings.Contains(m.Content, "left out") {
			leftOut = append(leftOut, m.Content)
		}
	}
	if sent != toolImagesMaxCount {
		t.Fatalf("the request carries %d pictures, want %d", sent, toolImagesMaxCount)
	}
	if len(leftOut) != 2 || !strings.Contains(leftOut[0], "p00.png") || !strings.Contains(leftOut[1], "p01.png") {
		t.Errorf("the two oldest pictures are not named as left out: %q", leftOut)
	}
	last := out[len(out)-1]
	if got := imageNames(last); len(got) != 1 || got[0] != fmt.Sprintf("p%02d.png", toolImagesMaxCount+1) {
		t.Errorf("the newest picture is not sent: %v", got)
	}

	big := func(name string) llm.ImagePart {
		p := imagePart(name)
		p.DataURL = "data:image/png;base64," + strings.Repeat("A", 8<<20)
		return p
	}
	heavy := []llm.Message{
		{Role: llm.RoleUser, Content: "look"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "a", Name: "read"}, {ID: "b", Name: "read"}, {ID: "c", Name: "read"}}},
		toolResultWith("a", big("a.png")),
		toolResultWith("b", big("b.png")),
		toolResultWith("c", big("c.png")),
	}
	out = withToolImages(heavy, true)
	pictures := out[len(out)-1]
	if got := imageNames(pictures); strings.Join(got, ",") != "b.png,c.png" {
		t.Errorf("under the byte budget the request carries %v, want the two newest", got)
	}
	if !strings.Contains(pictures.Content, "a.png") || !strings.Contains(pictures.Content, "left out") {
		t.Errorf("the step's message %q does not name a.png as left out", pictures.Content)
	}
}
