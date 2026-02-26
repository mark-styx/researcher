package research

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marklubin/researcher/internal/config"
	"github.com/marklubin/researcher/internal/llm"
)

// mockProvider records calls and returns canned responses.
// If responses is set, it returns them in order (cycling the last one).
// Otherwise it returns the single response field.
type mockProvider struct {
	calls     []llm.Request
	response  string
	responses []string
	callIdx   int
	err       error
}

func (m *mockProvider) Name() string { return "mock" }
func (m *mockProvider) Complete(_ context.Context, req llm.Request) (string, error) {
	m.calls = append(m.calls, req)
	if m.err != nil {
		return "", m.err
	}
	if len(m.responses) > 0 {
		resp := m.responses[m.callIdx]
		if m.callIdx < len(m.responses)-1 {
			m.callIdx++
		}
		return resp, nil
	}
	return m.response, nil
}

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()
	return &config.Config{
		ResearchDir: dir,
		Claude:      config.ClaudeConfig{MaxTokens: 4000},
		Tools:       config.ToolsConfig{Enabled: true},
	}
}

func TestNewRunner(t *testing.T) {
	cfg := testConfig(t)
	mock := &mockProvider{}
	r := NewRunner(cfg, mock)
	if r == nil {
		t.Fatal("expected non-nil runner")
	}
}

func TestRunner_UnknownType(t *testing.T) {
	cfg := testConfig(t)
	mock := &mockProvider{}
	r := NewRunner(cfg, mock)

	_, err := r.Run(context.Background(), Task{Type: "bogus", Topic: "test"})
	if err == nil {
		t.Fatal("expected error for unknown task type")
	}
	if !strings.Contains(err.Error(), "unknown task type") {
		t.Errorf("error = %q, expected to contain 'unknown task type'", err.Error())
	}
}

func TestRunner_Ask(t *testing.T) {
	cfg := testConfig(t)
	mock := &mockProvider{response: "answer here"}
	r := NewRunner(cfg, mock)

	out, err := r.Run(context.Background(), Task{Type: TypeAsk, Topic: "what is Go?"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// ask returns empty string (prints to stdout)
	if out != "" {
		t.Errorf("output = %q, want empty", out)
	}
	if len(mock.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(mock.calls))
	}
	if mock.calls[0].UserPrompt != "what is Go?" {
		t.Errorf("UserPrompt = %q", mock.calls[0].UserPrompt)
	}
}

func TestRunner_Dive(t *testing.T) {
	cfg := testConfig(t)
	mock := &mockProvider{responses: []string{
		`{"category": "programming", "filename": "rust-language"}`, // categorize call
		"Deep dive content", // content call
	}}
	r := NewRunner(cfg, mock)

	out, err := r.Run(context.Background(), Task{Type: TypeDive, Topic: "Rust Language"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(out, "rust-language.md") {
		t.Errorf("output path = %q, expected to end with rust-language.md", out)
	}
	if !strings.Contains(out, "programming") {
		t.Errorf("output path = %q, expected to contain category 'programming'", out)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("reading output: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "Rust Language") {
		t.Error("missing topic in header")
	}
	if !strings.Contains(content, "Deep dive content") {
		t.Error("missing LLM response in output")
	}
	if len(mock.calls) != 2 {
		t.Errorf("expected 2 LLM calls (categorize + content), got %d", len(mock.calls))
	}
}

func TestRunner_Watch(t *testing.T) {
	cfg := testConfig(t)
	mock := &mockProvider{responses: []string{
		`{"category": "llm", "filename": "ai-news"}`, // categorize call
		"Latest updates",                               // content call
	}}
	r := NewRunner(cfg, mock)

	out, err := r.Run(context.Background(), Task{Type: TypeWatch, Topic: "AI News"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(out, "-watch.md") {
		t.Errorf("output path = %q, expected to end with -watch.md", out)
	}

	data, _ := os.ReadFile(out)
	content := string(data)
	if !strings.Contains(content, "AI News") {
		t.Error("missing topic in header")
	}
	if !strings.Contains(content, "Latest updates") {
		t.Error("missing LLM response")
	}
}

func TestRunner_Watch_Appends(t *testing.T) {
	cfg := testConfig(t)
	mock := &mockProvider{responses: []string{
		`{"category": "general", "filename": "test-topic"}`, // categorize call 1
		"update 1",                                           // content call 1
		`{"category": "general", "filename": "test-topic"}`, // categorize call 2
		"update 2",                                           // content call 2
	}}
	r := NewRunner(cfg, mock)

	out1, _ := r.Run(context.Background(), Task{Type: TypeWatch, Topic: "test"})

	out2, _ := r.Run(context.Background(), Task{Type: TypeWatch, Topic: "test"})

	if out1 != out2 {
		t.Errorf("expected same path, got %q and %q", out1, out2)
	}

	data, _ := os.ReadFile(out1)
	content := string(data)
	if !strings.Contains(content, "update 1") {
		t.Error("missing first update")
	}
	if !strings.Contains(content, "update 2") {
		t.Error("missing second update")
	}
}

func TestRunner_Review(t *testing.T) {
	cfg := testConfig(t)
	mock := &mockProvider{responses: []string{
		`{"category": "llm", "filename": "llm-agents"}`, // categorize call
		"Synthesis content",                               // content call
	}}
	r := NewRunner(cfg, mock)

	out, err := r.Run(context.Background(), Task{Type: TypeReview, Topic: "LLM Agents"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(out, "-review.md") {
		t.Errorf("output path = %q, expected to end with -review.md", out)
	}
	if !strings.Contains(out, "llm") {
		t.Errorf("output path = %q, expected to contain category 'llm'", out)
	}
}

func TestRunner_Review_WithSources(t *testing.T) {
	cfg := testConfig(t)
	mock := &mockProvider{responses: []string{
		`{"category": "general", "filename": "test-review"}`, // categorize call
		"Combined review",                                     // content call
	}}
	r := NewRunner(cfg, mock)

	// Create a temp source file
	srcFile := filepath.Join(t.TempDir(), "source.md")
	os.WriteFile(srcFile, []byte("source document content"), 0644)

	_, err := r.Run(context.Background(), Task{
		Type:    TypeReview,
		Topic:   "test",
		Sources: []string{srcFile},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mock.calls) != 2 {
		t.Fatalf("expected 2 calls (categorize + content), got %d", len(mock.calls))
	}
	// The content call is the second one (index 1)
	if !strings.Contains(mock.calls[1].UserPrompt, "source document content") {
		t.Error("source content not included in prompt")
	}
}

func TestRunner_Enrich(t *testing.T) {
	cfg := testConfig(t)
	mock := &mockProvider{response: "Enriched version"}
	r := NewRunner(cfg, mock)

	// Create source document
	srcFile := filepath.Join(t.TempDir(), "doc.md")
	os.WriteFile(srcFile, []byte("original content"), 0644)

	out, err := r.Run(context.Background(), Task{
		Type:    TypeEnrich,
		Topic:   "test",
		Sources: []string{srcFile},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "doc-enriched.md") {
		t.Errorf("output path = %q, expected to contain doc-enriched.md", out)
	}

	data, _ := os.ReadFile(out)
	if !strings.Contains(string(data), "Enriched version") {
		t.Error("missing enriched content")
	}
}

func TestRunner_Enrich_NoSources(t *testing.T) {
	cfg := testConfig(t)
	mock := &mockProvider{}
	r := NewRunner(cfg, mock)

	_, err := r.Run(context.Background(), Task{Type: TypeEnrich, Topic: "test"})
	if err == nil {
		t.Fatal("expected error when no sources provided")
	}
}

func TestRunner_ProviderError(t *testing.T) {
	cfg := testConfig(t)
	mock := &mockProvider{err: fmt.Errorf("LLM down")}
	r := NewRunner(cfg, mock)

	_, err := r.Run(context.Background(), Task{Type: TypeAsk, Topic: "test"})
	if err == nil {
		t.Fatal("expected error from provider")
	}
	if !strings.Contains(err.Error(), "LLM down") {
		t.Errorf("error = %q, expected to contain 'LLM down'", err.Error())
	}
}

func TestRunner_DefaultTools(t *testing.T) {
	t.Run("tools enabled", func(t *testing.T) {
		cfg := testConfig(t)
		cfg.Tools.Enabled = true
		mock := &mockProvider{response: "ok"}
		r := NewRunner(cfg, mock)

		r.Run(context.Background(), Task{Type: TypeAsk, Topic: "test"})
		if len(mock.calls) != 1 {
			t.Fatalf("expected 1 call, got %d", len(mock.calls))
		}
		if mock.calls[0].Tools == nil {
			t.Error("expected tools to be non-nil when enabled")
		}
	})

	t.Run("tools disabled", func(t *testing.T) {
		cfg := testConfig(t)
		cfg.Tools.Enabled = false
		mock := &mockProvider{response: "ok"}
		r := NewRunner(cfg, mock)

		r.Run(context.Background(), Task{Type: TypeAsk, Topic: "test"})
		if mock.calls[0].Tools != nil {
			t.Error("expected tools to be nil when disabled")
		}
	})
}
