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
type mockProvider struct {
	calls    []llm.Request
	response string
	err      error
}

func (m *mockProvider) Name() string { return "mock" }
func (m *mockProvider) Complete(_ context.Context, req llm.Request) (string, error) {
	m.calls = append(m.calls, req)
	return m.response, m.err
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
	mock := &mockProvider{response: "Deep dive content"}
	r := NewRunner(cfg, mock)

	out, err := r.Run(context.Background(), Task{Type: TypeDive, Topic: "Rust Language"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(out, "README.md") {
		t.Errorf("output path = %q, expected to end with README.md", out)
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
}

func TestRunner_Watch(t *testing.T) {
	cfg := testConfig(t)
	mock := &mockProvider{response: "Latest updates"}
	r := NewRunner(cfg, mock)

	out, err := r.Run(context.Background(), Task{Type: TypeWatch, Topic: "AI News"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(out, "updates.md") {
		t.Errorf("output path = %q, expected to end with updates.md", out)
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
	mock := &mockProvider{response: "update 1"}
	r := NewRunner(cfg, mock)

	out1, _ := r.Run(context.Background(), Task{Type: TypeWatch, Topic: "test"})

	mock.response = "update 2"
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
	mock := &mockProvider{response: "Synthesis content"}
	r := NewRunner(cfg, mock)

	out, err := r.Run(context.Background(), Task{Type: TypeReview, Topic: "LLM Agents"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(out, "review.md") {
		t.Errorf("output path = %q, expected to end with review.md", out)
	}
}

func TestRunner_Review_WithSources(t *testing.T) {
	cfg := testConfig(t)
	mock := &mockProvider{response: "Combined review"}
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

	if len(mock.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(mock.calls))
	}
	if !strings.Contains(mock.calls[0].UserPrompt, "source document content") {
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
