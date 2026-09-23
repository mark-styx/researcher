package research

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/llm"
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
	metadata  string
}

func (m *mockProvider) Name() string { return "mock" }
func (m *mockProvider) Metadata() string {
	return m.metadata
}
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

func TestRunner_Ask_ProviderMetadata(t *testing.T) {
	cfg := testConfig(t)
	cfg.Grepai = config.GrepaiConfig{Binary: "nonexistent-grepai-xyz"}
	cfg.Ask = config.AskConfig{MaxAge: "90d"}
	mock := &mockProvider{response: "answer here", metadata: `{"mode":"hybrid"}`}
	r := NewRunner(cfg, mock)

	result, err := r.Run(context.Background(), Task{
		Type:       TypeAsk,
		Topic:      "what is Go?",
		NoSave:     true,
		NoResearch: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Metadata != `{"mode":"hybrid"}` {
		t.Fatalf("Metadata = %q, want hybrid JSON", result.Metadata)
	}
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

func TestRunner_Ask_DefaultBehavior(t *testing.T) {
	cfg := testConfig(t)
	// grepai will fail (binary doesn't exist), which is fine — non-fatal
	cfg.Grepai = config.GrepaiConfig{Binary: "nonexistent-grepai-xyz"}
	cfg.Ask = config.AskConfig{MaxAge: "90d"}

	// Two responses: one for the ask LLM call, one for categorize
	mock := &mockProvider{responses: []string{
		"answer here",
		`{"category": "general", "filename": "what-is-go"}`,
	}}
	r := NewRunner(cfg, mock)

	result, err := r.Run(context.Background(), Task{Type: TypeAsk, Topic: "what is Go?"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should save and return a path with -ask.md suffix
	if result.FilePath == "" {
		t.Fatal("expected output path, got empty")
	}
	if !strings.HasSuffix(result.FilePath, "-ask.md") {
		t.Errorf("output path = %q, expected -ask.md suffix", result.FilePath)
	}
	// First LLM call should be the ask
	if mock.calls[0].UserPrompt != "what is Go?" {
		t.Errorf("UserPrompt = %q", mock.calls[0].UserPrompt)
	}
	// File should exist with content
	data, err := os.ReadFile(result.FilePath)
	if err != nil {
		t.Fatalf("reading output: %v", err)
	}
	if !strings.Contains(string(data), "answer here") {
		t.Error("missing answer in saved file")
	}
	// Response should be populated
	if result.Response != "answer here" {
		t.Errorf("Response = %q, want %q", result.Response, "answer here")
	}
}

func TestRunner_Ask_NoSave(t *testing.T) {
	cfg := testConfig(t)
	cfg.Grepai = config.GrepaiConfig{Binary: "nonexistent-grepai-xyz"}
	cfg.Ask = config.AskConfig{MaxAge: "90d"}
	mock := &mockProvider{response: "answer here"}
	r := NewRunner(cfg, mock)

	result, err := r.Run(context.Background(), Task{
		Type:       TypeAsk,
		Topic:      "what is Go?",
		NoSave:     true,
		NoResearch: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.FilePath != "" {
		t.Errorf("expected empty output path with --no-save, got %q", result.FilePath)
	}
	if result.Response != "answer here" {
		t.Errorf("Response = %q, want %q", result.Response, "answer here")
	}
	// Only 1 LLM call (no categorize call)
	if len(mock.calls) != 1 {
		t.Errorf("expected 1 LLM call, got %d", len(mock.calls))
	}
}

func TestRunner_Ask_NoResearch(t *testing.T) {
	cfg := testConfig(t)
	cfg.Grepai = config.GrepaiConfig{Binary: "nonexistent-grepai-xyz"}
	cfg.Ask = config.AskConfig{MaxAge: "90d"}
	mock := &mockProvider{responses: []string{
		"answer here",
		`{"category": "general", "filename": "test-q"}`,
	}}
	r := NewRunner(cfg, mock)

	_, err := r.Run(context.Background(), Task{
		Type:       TypeAsk,
		Topic:      "what is Go?",
		NoResearch: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// System prompt should NOT contain "Existing Research"
	if strings.Contains(mock.calls[0].SystemPrompt, "Existing Research") {
		t.Error("system prompt should not contain research context when --no-research is set")
	}
}

func TestRunner_Ask_WithResearchContext(t *testing.T) {
	cfg := testConfig(t)
	cfg.Ask = config.AskConfig{MaxAge: "90d"}

	// Create a fake grepai that returns JSON pointing to a real file
	researchDir := cfg.ResearchDir
	subDir := filepath.Join(researchDir, "llm")
	os.MkdirAll(subDir, 0755)
	researchFile := filepath.Join(subDir, "agents.md")
	os.WriteFile(researchFile, []byte("# LLM Agents\nAgents are autonomous systems."), 0644)

	// Fake grepai returns JSON with relative path
	scriptDir := t.TempDir()
	results := fmt.Sprintf(`[{"file_path":"llm/agents.md","start_line":1,"end_line":2,"score":0.95,"content":"LLM agents"}]`)
	script := filepath.Join(scriptDir, "fake-grepai")
	os.WriteFile(script, []byte("#!/bin/sh\ncat <<'JSONEOF'\n"+results+"\nJSONEOF\n"), 0755)
	cfg.Grepai = config.GrepaiConfig{Binary: script}

	mock := &mockProvider{responses: []string{
		"synthesized answer from research",
		`{"category": "llm", "filename": "agents-ask"}`,
	}}
	r := NewRunner(cfg, mock)

	result, err := r.Run(context.Background(), Task{Type: TypeAsk, Topic: "tell me about LLM agents"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// System prompt should contain research context
	if !strings.Contains(mock.calls[0].SystemPrompt, "Existing Research") {
		t.Error("system prompt should contain 'Existing Research' section")
	}
	if !strings.Contains(mock.calls[0].SystemPrompt, "agents.md") {
		t.Error("system prompt should contain the research file reference")
	}

	// Should have saved
	if result.FilePath == "" {
		t.Fatal("expected output path")
	}
	if !strings.HasSuffix(result.FilePath, "-ask.md") {
		t.Errorf("output = %q, expected -ask.md suffix", result.FilePath)
	}
}

func TestRunner_Dive(t *testing.T) {
	cfg := testConfig(t)
	mock := &mockProvider{responses: []string{
		`{"category": "programming", "filename": "rust-language"}`, // categorize call
		"Deep dive content", // content call
	}}
	r := NewRunner(cfg, mock)

	result, err := r.Run(context.Background(), Task{Type: TypeDive, Topic: "Rust Language"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(result.FilePath, "rust-language.md") {
		t.Errorf("output path = %q, expected to end with rust-language.md", result.FilePath)
	}
	if !strings.Contains(result.FilePath, "programming") {
		t.Errorf("output path = %q, expected to contain category 'programming'", result.FilePath)
	}

	data, err := os.ReadFile(result.FilePath)
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

func TestRunner_Dive_WithResearchContext(t *testing.T) {
	cfg := testConfig(t)

	researchDir := cfg.ResearchDir
	subDir := filepath.Join(researchDir, "rust")
	os.MkdirAll(subDir, 0755)
	os.WriteFile(filepath.Join(subDir, "notes.md"), []byte("# Rust notes\nOwnership and borrowing."), 0644)

	scriptDir := t.TempDir()
	results := `[{"file_path":"rust/notes.md","start_line":1,"end_line":2,"score":0.9,"content":"Rust notes"}]`
	script := filepath.Join(scriptDir, "fake-grepai")
	os.WriteFile(script, []byte("#!/bin/sh\ncat <<'JSONEOF'\n"+results+"\nJSONEOF\n"), 0755)
	cfg.Grepai = config.GrepaiConfig{Binary: script}
	cfg.Ask = config.AskConfig{MaxAge: "90d"}

	mock := &mockProvider{responses: []string{
		`{"category": "programming", "filename": "rust-language"}`,
		"Deep dive content",
	}}
	r := NewRunner(cfg, mock)

	_, err := r.Run(context.Background(), Task{Type: TypeDive, Topic: "Rust Language"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(mock.calls[1].SystemPrompt, "Existing Research") {
		t.Error("dive system prompt should contain 'Existing Research' section")
	}
	if !strings.Contains(mock.calls[1].SystemPrompt, "notes.md") {
		t.Error("dive system prompt should reference the existing research file")
	}
}

func TestRunner_Dive_NoResearch(t *testing.T) {
	cfg := testConfig(t)
	mock := &mockProvider{responses: []string{
		`{"category": "programming", "filename": "rust-language"}`,
		"Deep dive content",
	}}
	r := NewRunner(cfg, mock)

	_, err := r.Run(context.Background(), Task{Type: TypeDive, Topic: "Rust Language", NoResearch: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(mock.calls[1].SystemPrompt, "Existing Research") {
		t.Error("dive system prompt should not contain research context when --no-research is set")
	}
}

func TestRunner_Watch(t *testing.T) {
	cfg := testConfig(t)
	mock := &mockProvider{responses: []string{
		`{"category": "llm", "filename": "ai-news"}`, // categorize call
		"Latest updates", // content call
	}}
	r := NewRunner(cfg, mock)

	result, err := r.Run(context.Background(), Task{Type: TypeWatch, Topic: "AI News"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(result.FilePath, "-watch.md") {
		t.Errorf("output path = %q, expected to end with -watch.md", result.FilePath)
	}

	data, _ := os.ReadFile(result.FilePath)
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
		"update 1", // content call 1
		`{"category": "general", "filename": "test-topic"}`, // categorize call 2
		"update 2", // content call 2
	}}
	r := NewRunner(cfg, mock)

	result1, _ := r.Run(context.Background(), Task{Type: TypeWatch, Topic: "test"})

	result2, _ := r.Run(context.Background(), Task{Type: TypeWatch, Topic: "test"})

	if result1.FilePath != result2.FilePath {
		t.Errorf("expected same path, got %q and %q", result1.FilePath, result2.FilePath)
	}

	data, _ := os.ReadFile(result1.FilePath)
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
		"Synthesis content",                             // content call
	}}
	r := NewRunner(cfg, mock)

	result, err := r.Run(context.Background(), Task{Type: TypeReview, Topic: "LLM Agents"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(result.FilePath, "-review.md") {
		t.Errorf("output path = %q, expected to end with -review.md", result.FilePath)
	}
	if !strings.Contains(result.FilePath, "llm") {
		t.Errorf("output path = %q, expected to contain category 'llm'", result.FilePath)
	}
}

func TestRunner_Review_WithSources(t *testing.T) {
	cfg := testConfig(t)
	mock := &mockProvider{responses: []string{
		`{"category": "general", "filename": "test-review"}`, // categorize call
		"Combined review", // content call
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

func TestRunner_Review_WithResearchContext(t *testing.T) {
	cfg := testConfig(t)

	researchDir := cfg.ResearchDir
	subDir := filepath.Join(researchDir, "llm")
	os.MkdirAll(subDir, 0755)
	os.WriteFile(filepath.Join(subDir, "agents.md"), []byte("# Agents\nBackground on agents."), 0644)

	scriptDir := t.TempDir()
	results := `[{"file_path":"llm/agents.md","start_line":1,"end_line":2,"score":0.9,"content":"Agents"}]`
	script := filepath.Join(scriptDir, "fake-grepai")
	os.WriteFile(script, []byte("#!/bin/sh\ncat <<'JSONEOF'\n"+results+"\nJSONEOF\n"), 0755)
	cfg.Grepai = config.GrepaiConfig{Binary: script}
	cfg.Ask = config.AskConfig{MaxAge: "90d"}

	mock := &mockProvider{responses: []string{
		`{"category": "llm", "filename": "llm-agents"}`,
		"Synthesis content",
	}}
	r := NewRunner(cfg, mock)

	_, err := r.Run(context.Background(), Task{Type: TypeReview, Topic: "LLM Agents"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(mock.calls[1].SystemPrompt, "Existing Research") {
		t.Error("review system prompt should contain 'Existing Research' section")
	}
}

func TestRunner_Compare_Topics_WithResearchContext(t *testing.T) {
	cfg := testConfig(t)

	researchDir := cfg.ResearchDir
	subDir := filepath.Join(researchDir, "frameworks")
	os.MkdirAll(subDir, 0755)
	os.WriteFile(filepath.Join(subDir, "notes.md"), []byte("# Frameworks\nReact vs Vue notes."), 0644)

	scriptDir := t.TempDir()
	results := `[{"file_path":"frameworks/notes.md","start_line":1,"end_line":2,"score":0.9,"content":"Frameworks"}]`
	script := filepath.Join(scriptDir, "fake-grepai")
	os.WriteFile(script, []byte("#!/bin/sh\ncat <<'JSONEOF'\n"+results+"\nJSONEOF\n"), 0755)
	cfg.Grepai = config.GrepaiConfig{Binary: script}
	cfg.Ask = config.AskConfig{MaxAge: "90d"}

	mock := &mockProvider{responses: []string{
		`{"category": "frameworks", "filename": "react-vs-vue"}`,
		"Comparison content here",
	}}
	r := NewRunner(cfg, mock)

	_, err := r.Run(context.Background(), Task{Type: TypeCompare, Topic: "React vs Vue"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(mock.calls[1].SystemPrompt, "Existing Research") {
		t.Error("compare system prompt should contain 'Existing Research' section")
	}
}

func TestRunner_Compare_Documents_SkipsResearchContext(t *testing.T) {
	cfg := testConfig(t)

	// If gatherResearchContext ran with the "document comparison" placeholder
	// topic, this fake grepai would still return a fresh hit — proving the
	// document-comparison path must skip the search entirely, not just
	// happen to find nothing.
	researchDir := cfg.ResearchDir
	os.MkdirAll(filepath.Join(researchDir, "misc"), 0755)
	os.WriteFile(filepath.Join(researchDir, "misc", "unrelated.md"), []byte("unrelated"), 0644)
	scriptDir := t.TempDir()
	results := `[{"file_path":"misc/unrelated.md","start_line":1,"end_line":1,"score":0.9,"content":"unrelated"}]`
	script := filepath.Join(scriptDir, "fake-grepai")
	os.WriteFile(script, []byte("#!/bin/sh\ncat <<'JSONEOF'\n"+results+"\nJSONEOF\n"), 0755)
	cfg.Grepai = config.GrepaiConfig{Binary: script}
	cfg.Ask = config.AskConfig{MaxAge: "90d"}

	tmpDir := t.TempDir()
	src1 := filepath.Join(tmpDir, "doc1.md")
	src2 := filepath.Join(tmpDir, "doc2.md")
	os.WriteFile(src1, []byte("Content of document one"), 0644)
	os.WriteFile(src2, []byte("Content of document two"), 0644)

	mock := &mockProvider{responses: []string{
		`{"category": "comparisons", "filename": "doc-comparison"}`,
		"Document comparison result",
	}}
	r := NewRunner(cfg, mock)

	_, err := r.Run(context.Background(), Task{
		Type:    TypeCompare,
		Topic:   "document comparison",
		Sources: []string{src1, src2},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(mock.calls[1].SystemPrompt, "Existing Research") {
		t.Error("document-comparison mode should not gather research context from the placeholder topic")
	}
}

func TestRunner_Enrich(t *testing.T) {
	cfg := testConfig(t)
	mock := &mockProvider{response: "Enriched version"}
	r := NewRunner(cfg, mock)

	// Create source document
	srcFile := filepath.Join(t.TempDir(), "doc.md")
	os.WriteFile(srcFile, []byte("original content"), 0644)

	result, err := r.Run(context.Background(), Task{
		Type:    TypeEnrich,
		Topic:   "test",
		Sources: []string{srcFile},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.FilePath, "doc-enriched.md") {
		t.Errorf("output path = %q, expected to contain doc-enriched.md", result.FilePath)
	}

	data, _ := os.ReadFile(result.FilePath)
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

	_, err := r.Run(context.Background(), Task{Type: TypeAsk, Topic: "test", NoSave: true, NoResearch: true})
	if err == nil {
		t.Fatal("expected error from provider")
	}
	if !strings.Contains(err.Error(), "LLM down") {
		t.Errorf("error = %q, expected to contain 'LLM down'", err.Error())
	}
}

func TestRunner_Compare_Topics(t *testing.T) {
	cfg := testConfig(t)
	mock := &mockProvider{responses: []string{
		`{"category": "frameworks", "filename": "react-vs-vue"}`, // categorize call
		"Comparison content here",                                // content call
	}}
	r := NewRunner(cfg, mock)

	result, err := r.Run(context.Background(), Task{Type: TypeCompare, Topic: "React vs Vue"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(result.FilePath, "-comparison.md") {
		t.Errorf("output path = %q, expected -comparison.md suffix", result.FilePath)
	}
	if !strings.Contains(result.FilePath, "frameworks") {
		t.Errorf("output path = %q, expected to contain category 'frameworks'", result.FilePath)
	}

	data, err := os.ReadFile(result.FilePath)
	if err != nil {
		t.Fatalf("reading output: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "Comparison: React vs Vue") {
		t.Error("missing topic in header")
	}
	if !strings.Contains(content, "Comparison content here") {
		t.Error("missing LLM response in output")
	}
	if len(mock.calls) != 2 {
		t.Errorf("expected 2 LLM calls (categorize + content), got %d", len(mock.calls))
	}
	// Content call prompt should mention comparative analysis
	if !strings.Contains(mock.calls[1].UserPrompt, "comparative analysis") {
		t.Error("expected prompt to mention 'comparative analysis'")
	}
}

func TestRunner_Compare_Documents(t *testing.T) {
	cfg := testConfig(t)
	mock := &mockProvider{responses: []string{
		`{"category": "comparisons", "filename": "doc-comparison"}`, // categorize call
		"Document comparison result",                                // content call
	}}
	r := NewRunner(cfg, mock)

	// Create two temp source files
	tmpDir := t.TempDir()
	src1 := filepath.Join(tmpDir, "doc1.md")
	src2 := filepath.Join(tmpDir, "doc2.md")
	os.WriteFile(src1, []byte("Content of document one"), 0644)
	os.WriteFile(src2, []byte("Content of document two"), 0644)

	result, err := r.Run(context.Background(), Task{
		Type:    TypeCompare,
		Topic:   "document comparison",
		Sources: []string{src1, src2},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(result.FilePath, "-comparison.md") {
		t.Errorf("output path = %q, expected -comparison.md suffix", result.FilePath)
	}

	// The content call should include both documents
	if len(mock.calls) < 2 {
		t.Fatalf("expected at least 2 calls, got %d", len(mock.calls))
	}
	prompt := mock.calls[1].UserPrompt
	if !strings.Contains(prompt, "Content of document one") {
		t.Error("source 1 content not included in prompt")
	}
	if !strings.Contains(prompt, "Content of document two") {
		t.Error("source 2 content not included in prompt")
	}
}

func TestRunner_Compare_DocumentReadError(t *testing.T) {
	cfg := testConfig(t)
	mock := &mockProvider{responses: []string{
		`{"category": "test", "filename": "test"}`, // categorize call
	}}
	r := NewRunner(cfg, mock)

	_, err := r.Run(context.Background(), Task{
		Type:    TypeCompare,
		Topic:   "test",
		Sources: []string{"/nonexistent/file1.md", "/nonexistent/file2.md"},
	})
	if err == nil {
		t.Fatal("expected error when source file doesn't exist")
	}
	if !strings.Contains(err.Error(), "reading source") {
		t.Errorf("error = %q, expected to mention 'reading source'", err.Error())
	}
}

func TestRunner_DefaultTools(t *testing.T) {
	t.Run("tools enabled", func(t *testing.T) {
		cfg := testConfig(t)
		cfg.Tools.Enabled = true
		mock := &mockProvider{response: "ok"}
		r := NewRunner(cfg, mock)

		r.Run(context.Background(), Task{Type: TypeAsk, Topic: "test", NoSave: true, NoResearch: true})
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

		r.Run(context.Background(), Task{Type: TypeAsk, Topic: "test", NoSave: true, NoResearch: true})
		if mock.calls[0].Tools != nil {
			t.Error("expected tools to be nil when disabled")
		}
	})
}
