package agent

// Godog harness for features/view_image.feature: drives the real Agent through
// a scripted provider that issues view_image calls over a real temp workspace,
// then asserts the shape of the next LLM request, the tools it offered and
// the persisted transcript.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cucumber/godog"

	"github.com/EvilFreelancer/coddy-agent/internal/acp"
	"github.com/EvilFreelancer/coddy-agent/internal/config"
	"github.com/EvilFreelancer/coddy-agent/internal/llm"
	"github.com/EvilFreelancer/coddy-agent/internal/session"
	"github.com/EvilFreelancer/coddy-agent/internal/tooling"
)

func tcViewImage(id, path string) llm.ToolCall {
	b, _ := json.Marshal(map[string]string{"path": path})
	return llm.ToolCall{ID: id, Name: "view_image", InputJSON: string(b)}
}

// viProvider is evScriptProvider that also records the tools each request offered.
type viProvider struct {
	evScriptProvider
	toolsSeen [][]llm.ToolDefinition
}

func (p *viProvider) Stream(ctx context.Context, messages []llm.Message, tools []llm.ToolDefinition, onChunk func(llm.StreamChunk)) (*llm.Response, error) {
	p.toolsSeen = append(p.toolsSeen, tools)
	return p.evScriptProvider.Stream(ctx, messages, tools, onChunk)
}

type viewImageFeatureState struct {
	cwd        string
	multimodal bool
	st         *session.State
	ag         *Agent
	provider   *viProvider
}

func (s *viewImageFeatureState) reset() error {
	s.close()
	dir, err := os.MkdirTemp("", "coddy-bdd-view-image-*")
	if err != nil {
		return err
	}
	s.cwd = dir
	s.multimodal = false
	s.provider = &viProvider{}
	return os.MkdirAll(filepath.Join(dir, ".session"), 0o755)
}

func (s *viewImageFeatureState) close() {
	if s.cwd != "" {
		_ = os.RemoveAll(s.cwd)
		s.cwd = ""
	}
	s.st = nil
	s.ag = nil
}

func (s *viewImageFeatureState) modelReadsImages() error {
	s.multimodal = true
	return nil
}

func (s *viewImageFeatureState) modelWithoutImages() error {
	s.multimodal = false
	return nil
}

func (s *viewImageFeatureState) writeFile(name string) error {
	return os.WriteFile(filepath.Join(s.cwd, name), []byte("bytes of "+name), 0o644)
}

func (s *viewImageFeatureState) twoImages(a, b string) error {
	if err := s.writeFile(a); err != nil {
		return err
	}
	return s.writeFile(b)
}

func (s *viewImageFeatureState) agent() *Agent {
	if s.ag != nil {
		return s.ag
	}
	cfg := &config.Config{
		Providers: []config.ProviderConfig{{Name: "fake", Type: "openai", APIKey: "test"}},
		Models:    []config.ModelEntry{{Model: "fake/model", MaxTokens: 100, MaxContextTokens: 128000, Multimodal: s.multimodal}},
		Agent:     config.Agent{Model: "fake/model"},
		Tools:     config.Tools{PermissionMode: config.PermModeBypass},
	}
	s.st = &session.State{ID: "sess_bdd_view_image", CWD: s.cwd, Mode: session.ModeAgent, SessionDir: filepath.Join(s.cwd, ".session")}
	s.ag = NewAgent(cfg, s.st, resumePermissionSender{}, nil)
	s.ag.providerFactory = func(llm.ProviderInput) (llm.Provider, error) { return s.provider, nil }
	return s.ag
}

// run scripts the next steps (a final answer is appended) and runs one turn.
func (s *viewImageFeatureState) run(ctx context.Context, steps ...evStep) error {
	ag := s.agent()
	s.provider.steps = append(s.provider.steps, append(steps, evStep{text: "answer"})...)
	_, err := ag.Run(ctx, []acp.ContentBlock{{Type: "text", Text: "look"}})
	return err
}

func (s *viewImageFeatureState) viewTwoInOneBatch(a, b string) error {
	return s.run(context.Background(), evStep{calls: []llm.ToolCall{tcViewImage("v1", a), tcViewImage("v2", b)}})
}

func (s *viewImageFeatureState) viewOne(name string) error {
	return s.run(context.Background(), evStep{calls: []llm.ToolCall{tcViewImage("v1", name)}})
}

// viewThenCancel runs a batch whose second call cancels the turn, so the
// image the first call queued is never delivered. The turn's own result
// does not matter here, only what the next turn sends.
func (s *viewImageFeatureState) viewThenCancel(name string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.agent().registry.Register(&tooling.Tool{
		Definition: llm.ToolDefinition{Name: "cancel_turn", InputSchema: map[string]interface{}{"type": "object"}},
		Execute: func(context.Context, string, *tooling.Env) (string, error) {
			cancel()
			return "cancelled", nil
		},
	})
	calls := []llm.ToolCall{tcViewImage("v1", name), {ID: "c1", Name: "cancel_turn", InputJSON: "{}"}, tcViewImage("v2", name)}
	s.provider.steps = append(s.provider.steps, evStep{calls: calls})
	_, _ = s.ag.Run(ctx, []acp.ContentBlock{{Type: "text", Text: "look"}})
	return nil
}

// nextPrompt runs a turn with a tool batch of its own: images left over from
// the cancelled turn would go out right after it.
func (s *viewImageFeatureState) nextPrompt() error {
	return s.run(context.Background(), evStep{calls: []llm.ToolCall{{ID: "g1", Name: "glob", InputJSON: `{"pattern":"*.png"}`}}})
}

func (s *viewImageFeatureState) firstRequestHidesTool() error {
	if len(s.provider.toolsSeen) == 0 {
		return fmt.Errorf("no LLM request was made")
	}
	for _, d := range s.provider.toolsSeen[0] {
		if d.Name == "view_image" {
			return fmt.Errorf("view_image was offered to a model that does not read images")
		}
	}
	return nil
}

func (s *viewImageFeatureState) lastRequest() []llm.Message {
	seen := s.provider.streamSeen
	if len(seen) == 0 {
		return nil
	}
	return seen[len(seen)-1]
}

// requestTail is the part of the last request after the prompt: what the
// tool batch added, without the turn context the send boundary appends to
// every request.
func (s *viewImageFeatureState) requestTail() ([]llm.Message, error) {
	req := s.lastRequest()
	if n := len(req); n > 0 && req[n-1].Role == llm.RoleUser && strings.Contains(req[n-1].Content, turnContextOpenTag) {
		req = req[:n-1]
	}
	for i, m := range req {
		if m.Role == llm.RoleAssistant && len(m.ToolCalls) > 0 {
			return req[i:], nil
		}
	}
	return nil, fmt.Errorf("the last request has no assistant tool-call message")
}

func (s *viewImageFeatureState) requestShape() error {
	tail, err := s.requestTail()
	if err != nil {
		return err
	}
	roles := make([]string, 0, len(tail))
	for _, m := range tail {
		roles = append(roles, string(m.Role))
	}
	want := []string{string(llm.RoleAssistant), string(llm.RoleTool), string(llm.RoleTool), string(llm.RoleUser)}
	if strings.Join(roles, ",") != strings.Join(want, ",") {
		return fmt.Errorf("request tail roles = %v, want %v", roles, want)
	}
	if tail[1].ToolCallID != "v1" || tail[2].ToolCallID != "v2" {
		return fmt.Errorf("tool results answer %q and %q, want v1 and v2", tail[1].ToolCallID, tail[2].ToolCallID)
	}
	return nil
}

func imageNames(m llm.Message) []string {
	names := make([]string, 0, len(m.ImageParts))
	for _, p := range m.ImageParts {
		names = append(names, p.Name)
	}
	return names
}

func (s *viewImageFeatureState) userMessageCarries(a, b string) error {
	tail, err := s.requestTail()
	if err != nil {
		return err
	}
	user := tail[len(tail)-1]
	if got := imageNames(user); strings.Join(got, ",") != a+","+b {
		return fmt.Errorf("user message images = %v, want [%s %s]", got, a, b)
	}
	for _, p := range user.ImageParts {
		if !strings.HasPrefix(p.DataURL, "data:image/png;base64,") {
			return fmt.Errorf("image %s is not a PNG data URL: %.40q", p.Name, p.DataURL)
		}
	}
	return nil
}

func (s *viewImageFeatureState) transcriptKeepsImages() error {
	for _, m := range s.st.GetMessages() {
		if m.Role == llm.RoleUser && len(m.ImageParts) == 2 {
			return nil
		}
	}
	return fmt.Errorf("no persisted user message carries both images")
}

func (s *viewImageFeatureState) toolResultSays(text string) error {
	tail, err := s.requestTail()
	if err != nil {
		return err
	}
	if got := requestToolContent(tail, "v1"); !strings.Contains(got, text) {
		return fmt.Errorf("tool result %q does not contain %q", got, text)
	}
	return nil
}

func (s *viewImageFeatureState) requestCarriesNoImages() error {
	for _, m := range s.lastRequest() {
		if len(m.ImageParts) > 0 {
			return fmt.Errorf("a %s message carries %d image(s)", m.Role, len(m.ImageParts))
		}
	}
	return nil
}

func initializeViewImageScenario(sc *godog.ScenarioContext) {
	s := &viewImageFeatureState{}
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		return ctx, s.reset()
	})
	sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		s.close()
		return ctx, nil
	})

	sc.Step(`^a model that reads images$`, s.modelReadsImages)
	sc.Step(`^a model that does not read images$`, s.modelWithoutImages)
	sc.Step(`^workspace images "([^"]*)" and "([^"]*)"$`, s.twoImages)
	sc.Step(`^a workspace file "([^"]*)"$`, s.writeFile)
	sc.Step(`^the model views "([^"]*)" and "([^"]*)" in one batch, then answers$`, s.viewTwoInOneBatch)
	sc.Step(`^the model views "([^"]*)", then answers$`, s.viewOne)
	sc.Step(`^the next LLM request has the assistant tool calls, then both tool results, then one user message$`, s.requestShape)
	sc.Step(`^that user message carries the images "([^"]*)" and "([^"]*)" in that order$`, s.userMessageCarries)
	sc.Step(`^the persisted transcript keeps that user message with both images$`, s.transcriptKeepsImages)
	sc.Step(`^the model views "([^"]*)" and the turn is cancelled before the batch ends$`, s.viewThenCancel)
	sc.Step(`^the user sends the next prompt and the model lists the workspace, then answers$`, s.nextPrompt)
	sc.Step(`^the first LLM request does not offer view_image$`, s.firstRequestHidesTool)
	sc.Step(`^the tool result says "([^"]*)"$`, s.toolResultSays)
	sc.Step(`^the next LLM request carries no images$`, s.requestCarriesNoImages)
}

func TestViewImageFeature(t *testing.T) {
	suite := godog.TestSuite{
		Name:                "view-image",
		ScenarioInitializer: initializeViewImageScenario,
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/view_image.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("view_image feature suite failed")
	}
}
