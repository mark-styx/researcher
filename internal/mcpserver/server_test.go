package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/marklubin/researcher/internal/config"
	"github.com/marklubin/researcher/internal/llm"
)

// mockProvider records calls and returns canned responses.
type mockProvider struct {
	calls     []llm.Request
	response  string
	responses []string
	callIdx   int
}

func (m *mockProvider) Name() string { return "mock" }
func (m *mockProvider) Complete(_ context.Context, req llm.Request) (string, error) {
	m.calls = append(m.calls, req)
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
		Tools:       config.ToolsConfig{Enabled: false},
		Grepai:      config.GrepaiConfig{Binary: "nonexistent-grepai-xyz"},
		Ask:         config.AskConfig{MaxAge: "90d"},
	}
}

func setupClient(t *testing.T, cfg *config.Config, provider llm.Provider) *client.Client {
	t.Helper()
	s := New(cfg, provider, "test")
	c, err := client.NewInProcessClient(s)
	if err != nil {
		t.Fatalf("creating in-process client: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	ctx := context.Background()
	_, err = c.Initialize(ctx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: "2024-11-05",
			Capabilities:    mcp.ClientCapabilities{},
			ClientInfo: mcp.Implementation{
				Name:    "test-client",
				Version: "1.0.0",
			},
		},
	})
	if err != nil {
		t.Fatalf("initializing client: %v", err)
	}
	return c
}

func TestMCPServer_ListTools(t *testing.T) {
	cfg := testConfig(t)
	mock := &mockProvider{response: "ok"}
	c := setupClient(t, cfg, mock)

	result, err := c.ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("listing tools: %v", err)
	}

	expectedTools := map[string]bool{
		"researcher_ask":     false,
		"researcher_dive":    false,
		"researcher_review":  false,
		"researcher_compare": false,
		"researcher_enrich":  false,
		"researcher_search":  false,
		"researcher_context": false,
		"researcher_list":    false,
		"researcher_read":    false,
	}

	for _, tool := range result.Tools {
		if _, ok := expectedTools[tool.Name]; ok {
			expectedTools[tool.Name] = true
		}
	}

	for name, found := range expectedTools {
		if !found {
			t.Errorf("tool %q not registered", name)
		}
	}

	if len(result.Tools) != 9 {
		t.Errorf("expected 9 tools, got %d", len(result.Tools))
	}
}

func TestMCPServer_List(t *testing.T) {
	cfg := testConfig(t)
	researchDir := cfg.ResearchDir

	// Create some research files
	llmDir := filepath.Join(researchDir, "llm")
	os.MkdirAll(llmDir, 0755)
	os.WriteFile(filepath.Join(llmDir, "agents.md"), []byte("# Agents"), 0644)
	os.WriteFile(filepath.Join(llmDir, "tools.md"), []byte("# Tools"), 0644)

	secDir := filepath.Join(researchDir, "security")
	os.MkdirAll(secDir, 0755)
	os.WriteFile(filepath.Join(secDir, "auth.md"), []byte("# Auth"), 0644)

	mock := &mockProvider{}
	c := setupClient(t, cfg, mock)

	// List all
	result, err := c.CallTool(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "researcher_list",
			Arguments: map[string]any{},
		},
	})
	if err != nil {
		t.Fatalf("calling list: %v", err)
	}

	text := extractText(t, result)
	var entries []listEntry
	if err := json.Unmarshal([]byte(text), &entries); err != nil {
		t.Fatalf("parsing list JSON: %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("expected 3 entries, got %d", len(entries))
	}

	// List filtered by category
	result, err = c.CallTool(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "researcher_list",
			Arguments: map[string]any{"category": "llm"},
		},
	})
	if err != nil {
		t.Fatalf("calling list with filter: %v", err)
	}

	text = extractText(t, result)
	var filtered []listEntry
	if err := json.Unmarshal([]byte(text), &filtered); err != nil {
		t.Fatalf("parsing filtered list JSON: %v", err)
	}
	if len(filtered) != 2 {
		t.Errorf("expected 2 entries for category 'llm', got %d", len(filtered))
	}
}

func TestMCPServer_Read(t *testing.T) {
	cfg := testConfig(t)
	researchDir := cfg.ResearchDir

	llmDir := filepath.Join(researchDir, "llm")
	os.MkdirAll(llmDir, 0755)
	os.WriteFile(filepath.Join(llmDir, "agents.md"), []byte("# LLM Agents\nContent here."), 0644)

	mock := &mockProvider{}
	c := setupClient(t, cfg, mock)

	result, err := c.CallTool(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "researcher_read",
			Arguments: map[string]any{"path": "llm/agents.md"},
		},
	})
	if err != nil {
		t.Fatalf("calling read: %v", err)
	}

	text := extractText(t, result)
	if !strings.Contains(text, "LLM Agents") {
		t.Errorf("expected content to contain 'LLM Agents', got %q", text)
	}
	if !strings.Contains(text, "Content here.") {
		t.Errorf("expected content to contain 'Content here.', got %q", text)
	}
}

func TestMCPServer_Read_DirectoryTraversal(t *testing.T) {
	cfg := testConfig(t)
	mock := &mockProvider{}
	c := setupClient(t, cfg, mock)

	result, err := c.CallTool(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "researcher_read",
			Arguments: map[string]any{"path": "../../../etc/passwd"},
		},
	})
	if err != nil {
		t.Fatalf("calling read: %v", err)
	}

	// Should return error result, not the file contents
	if !result.IsError {
		t.Error("expected error for directory traversal attempt")
	}
}

func TestMCPServer_Read_PrefixCollisionBlocked(t *testing.T) {
	cfg := testConfig(t)
	mock := &mockProvider{}
	c := setupClient(t, cfg, mock)

	// "<researchDir>-outside" should not be considered inside researchDir.
	outsideDir := cfg.ResearchDir + "-outside"
	if err := os.MkdirAll(outsideDir, 0755); err != nil {
		t.Fatalf("creating outside dir: %v", err)
	}
	outsidePath := filepath.Join(outsideDir, "secret.md")
	if err := os.WriteFile(outsidePath, []byte("secret"), 0644); err != nil {
		t.Fatalf("writing outside file: %v", err)
	}

	rel, err := filepath.Rel(cfg.ResearchDir, outsidePath)
	if err != nil {
		t.Fatalf("computing relative path: %v", err)
	}

	result, err := c.CallTool(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "researcher_read",
			Arguments: map[string]any{"path": rel},
		},
	})
	if err != nil {
		t.Fatalf("calling read: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error for prefix-collision path escape")
	}
}

func TestMCPServer_Read_NotFound(t *testing.T) {
	cfg := testConfig(t)
	mock := &mockProvider{}
	c := setupClient(t, cfg, mock)

	result, err := c.CallTool(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "researcher_read",
			Arguments: map[string]any{"path": "nonexistent/file.md"},
		},
	})
	if err != nil {
		t.Fatalf("calling read: %v", err)
	}

	if !result.IsError {
		t.Error("expected error for missing file")
	}
}

func TestMCPServer_Ask(t *testing.T) {
	cfg := testConfig(t)
	mock := &mockProvider{responses: []string{
		"the answer is 42",
		`{"category": "general", "filename": "meaning-of-life"}`,
	}}
	c := setupClient(t, cfg, mock)

	result, err := c.CallTool(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "researcher_ask",
			Arguments: map[string]any{
				"question":    "what is the meaning of life?",
				"no_research": true,
			},
		},
	})
	if err != nil {
		t.Fatalf("calling ask: %v", err)
	}

	text := extractText(t, result)
	var resp map[string]string
	if err := json.Unmarshal([]byte(text), &resp); err != nil {
		t.Fatalf("parsing ask response: %v", err)
	}

	if resp["answer"] != "the answer is 42" {
		t.Errorf("answer = %q, want %q", resp["answer"], "the answer is 42")
	}
	if resp["saved_to"] == "" {
		t.Error("expected saved_to path, got empty")
	}
}

func TestMCPServer_Ask_NoSave(t *testing.T) {
	cfg := testConfig(t)
	mock := &mockProvider{response: "ephemeral answer"}
	c := setupClient(t, cfg, mock)

	result, err := c.CallTool(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "researcher_ask",
			Arguments: map[string]any{
				"question":    "quick question",
				"no_research": true,
				"no_save":     true,
			},
		},
	})
	if err != nil {
		t.Fatalf("calling ask: %v", err)
	}

	text := extractText(t, result)
	var resp map[string]string
	if err := json.Unmarshal([]byte(text), &resp); err != nil {
		t.Fatalf("parsing ask response: %v", err)
	}

	if resp["answer"] != "ephemeral answer" {
		t.Errorf("answer = %q, want %q", resp["answer"], "ephemeral answer")
	}
	if resp["saved_to"] != "" {
		t.Errorf("expected empty saved_to with no_save, got %q", resp["saved_to"])
	}
}

func TestMCPServer_Dive(t *testing.T) {
	cfg := testConfig(t)
	mock := &mockProvider{responses: []string{
		`{"category": "programming", "filename": "rust-language"}`,
		"Deep dive into Rust",
	}}
	c := setupClient(t, cfg, mock)

	result, err := c.CallTool(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "researcher_dive",
			Arguments: map[string]any{
				"topic": "Rust Language",
			},
		},
	})
	if err != nil {
		t.Fatalf("calling dive: %v", err)
	}

	text := extractText(t, result)
	var resp map[string]string
	if err := json.Unmarshal([]byte(text), &resp); err != nil {
		t.Fatalf("parsing dive response: %v", err)
	}

	if resp["report"] != "Deep dive into Rust" {
		t.Errorf("report = %q, want %q", resp["report"], "Deep dive into Rust")
	}
	if resp["saved_to"] == "" {
		t.Error("expected saved_to path, got empty")
	}
	if !strings.Contains(resp["saved_to"], "programming") {
		t.Errorf("saved_to = %q, expected to contain 'programming'", resp["saved_to"])
	}
}

func TestMCPServer_Review(t *testing.T) {
	cfg := testConfig(t)
	mock := &mockProvider{responses: []string{
		`{"category": "llm", "filename": "agents-review"}`,
		"Comprehensive review of LLM agents",
	}}
	c := setupClient(t, cfg, mock)

	result, err := c.CallTool(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "researcher_review",
			Arguments: map[string]any{
				"topic": "LLM Agents",
			},
		},
	})
	if err != nil {
		t.Fatalf("calling review: %v", err)
	}

	text := extractText(t, result)
	var resp map[string]string
	if err := json.Unmarshal([]byte(text), &resp); err != nil {
		t.Fatalf("parsing review response: %v", err)
	}

	if resp["review"] != "Comprehensive review of LLM agents" {
		t.Errorf("review = %q", resp["review"])
	}
	if resp["saved_to"] == "" {
		t.Error("expected saved_to path")
	}
}

func TestMCPServer_Compare(t *testing.T) {
	cfg := testConfig(t)
	mock := &mockProvider{responses: []string{
		`{"category": "frameworks", "filename": "react-vs-vue"}`,
		"React and Vue comparison",
	}}
	c := setupClient(t, cfg, mock)

	result, err := c.CallTool(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "researcher_compare",
			Arguments: map[string]any{
				"subject1": "React",
				"subject2": "Vue",
			},
		},
	})
	if err != nil {
		t.Fatalf("calling compare: %v", err)
	}

	text := extractText(t, result)
	var resp map[string]string
	if err := json.Unmarshal([]byte(text), &resp); err != nil {
		t.Fatalf("parsing compare response: %v", err)
	}

	if resp["comparison"] != "React and Vue comparison" {
		t.Errorf("comparison = %q, want %q", resp["comparison"], "React and Vue comparison")
	}
	if resp["saved_to"] == "" {
		t.Error("expected saved_to path, got empty")
	}
	if !strings.Contains(resp["saved_to"], "frameworks") {
		t.Errorf("saved_to = %q, expected to contain 'frameworks'", resp["saved_to"])
	}
}

func TestMCPServer_Compare_WithSources(t *testing.T) {
	cfg := testConfig(t)

	// Create source files
	src1 := filepath.Join(t.TempDir(), "doc1.md")
	src2 := filepath.Join(t.TempDir(), "doc2.md")
	os.WriteFile(src1, []byte("Document one content"), 0644)
	os.WriteFile(src2, []byte("Document two content"), 0644)

	mock := &mockProvider{responses: []string{
		`{"category": "comparisons", "filename": "doc-comparison"}`,
		"Source document comparison",
	}}
	c := setupClient(t, cfg, mock)

	result, err := c.CallTool(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "researcher_compare",
			Arguments: map[string]any{
				"subject1": "Doc1",
				"subject2": "Doc2",
				"sources":  src1 + "," + src2,
			},
		},
	})
	if err != nil {
		t.Fatalf("calling compare with sources: %v", err)
	}

	text := extractText(t, result)
	var resp map[string]string
	if err := json.Unmarshal([]byte(text), &resp); err != nil {
		t.Fatalf("parsing compare response: %v", err)
	}

	if resp["comparison"] != "Source document comparison" {
		t.Errorf("comparison = %q", resp["comparison"])
	}
}

func TestMCPServer_Enrich(t *testing.T) {
	cfg := testConfig(t)

	// Create a document to enrich
	llmDir := filepath.Join(cfg.ResearchDir, "llm")
	os.MkdirAll(llmDir, 0755)
	os.WriteFile(filepath.Join(llmDir, "agents.md"), []byte("# LLM Agents\nThin content."), 0644)

	mock := &mockProvider{response: "Enriched content with more depth"}
	c := setupClient(t, cfg, mock)

	result, err := c.CallTool(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "researcher_enrich",
			Arguments: map[string]any{
				"path": "llm/agents.md",
			},
		},
	})
	if err != nil {
		t.Fatalf("calling enrich: %v", err)
	}

	text := extractText(t, result)
	var resp map[string]string
	if err := json.Unmarshal([]byte(text), &resp); err != nil {
		t.Fatalf("parsing enrich response: %v", err)
	}

	if resp["enriched"] != "Enriched content with more depth" {
		t.Errorf("enriched = %q, want %q", resp["enriched"], "Enriched content with more depth")
	}
	if resp["saved_to"] == "" {
		t.Error("expected saved_to path, got empty")
	}
	if !strings.Contains(resp["saved_to"], "agents-enriched.md") {
		t.Errorf("saved_to = %q, expected to contain 'agents-enriched.md'", resp["saved_to"])
	}
}

func TestMCPServer_Enrich_NotFound(t *testing.T) {
	cfg := testConfig(t)
	mock := &mockProvider{}
	c := setupClient(t, cfg, mock)

	result, err := c.CallTool(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "researcher_enrich",
			Arguments: map[string]any{
				"path": "nonexistent/doc.md",
			},
		},
	})
	if err != nil {
		t.Fatalf("calling enrich: %v", err)
	}

	if !result.IsError {
		t.Error("expected error for missing document")
	}
}

func TestMCPServer_Search(t *testing.T) {
	cfg := testConfig(t)

	// Create a fake grepai that returns JSON results
	scriptDir := t.TempDir()
	results := `[{"file_path":"llm/agents.md","start_line":1,"end_line":10,"score":0.95,"content":"LLM agents"}]`
	script := filepath.Join(scriptDir, "fake-grepai")
	os.WriteFile(script, []byte("#!/bin/sh\ncat <<'JSONEOF'\n"+results+"\nJSONEOF\n"), 0755)
	cfg.Grepai = config.GrepaiConfig{Binary: script}

	mock := &mockProvider{}
	c := setupClient(t, cfg, mock)

	result, err := c.CallTool(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "researcher_search",
			Arguments: map[string]any{
				"query": "LLM agents",
				"limit": 5.0,
			},
		},
	})
	if err != nil {
		t.Fatalf("calling search: %v", err)
	}

	text := extractText(t, result)
	if !strings.Contains(text, "agents.md") {
		t.Errorf("expected result to contain 'agents.md', got %q", text)
	}
	if !strings.Contains(text, "0.95") {
		t.Errorf("expected result to contain score '0.95', got %q", text)
	}
}

func TestMCPServer_Context(t *testing.T) {
	cfg := testConfig(t)
	researchDir := cfg.ResearchDir

	// Create research files that the fake grepai will reference
	llmDir := filepath.Join(researchDir, "llm")
	os.MkdirAll(llmDir, 0755)
	os.WriteFile(filepath.Join(llmDir, "agents.md"), []byte("# LLM Agents\n\nResearch about autonomous agents."), 0644)

	// Create a fake grepai that returns JSON results referencing our test file
	scriptDir := t.TempDir()
	results := `[{"file_path":"llm/agents.md","start_line":1,"end_line":3,"score":0.92,"content":"LLM Agents research about autonomous agents"}]`
	script := filepath.Join(scriptDir, "fake-grepai")
	os.WriteFile(script, []byte("#!/bin/sh\ncat <<'JSONEOF'\n"+results+"\nJSONEOF\n"), 0755)
	cfg.Grepai = config.GrepaiConfig{Binary: script}

	mock := &mockProvider{}
	c := setupClient(t, cfg, mock)

	result, err := c.CallTool(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "researcher_context",
			Arguments: map[string]any{
				"topic": "autonomous agents",
			},
		},
	})
	if err != nil {
		t.Fatalf("calling context: %v", err)
	}

	text := extractText(t, result)
	var resp contextResult
	if err := json.Unmarshal([]byte(text), &resp); err != nil {
		t.Fatalf("parsing context response: %v", err)
	}

	if resp.Topic != "autonomous agents" {
		t.Errorf("topic = %q, want %q", resp.Topic, "autonomous agents")
	}
	if resp.Count != 1 {
		t.Errorf("count = %d, want 1", resp.Count)
	}
	if len(resp.Sources) != 1 {
		t.Fatalf("sources len = %d, want 1", len(resp.Sources))
	}
	if resp.Sources[0].FilePath != "llm/agents.md" {
		t.Errorf("source file_path = %q, want %q", resp.Sources[0].FilePath, "llm/agents.md")
	}
	if resp.Sources[0].Score != 0.92 {
		t.Errorf("source score = %f, want 0.92", resp.Sources[0].Score)
	}
	if resp.Sources[0].Freshness != "current" {
		t.Errorf("source freshness = %q, want %q", resp.Sources[0].Freshness, "current")
	}
	if resp.Context == "" {
		t.Error("expected non-empty formatted context")
	}
	if !strings.Contains(resp.Context, "LLM Agents") {
		t.Error("context should contain file content")
	}

	// No LLM calls should have been made
	if len(mock.calls) != 0 {
		t.Errorf("expected 0 LLM calls, got %d", len(mock.calls))
	}
}

func TestMCPServer_Context_EmptyResults(t *testing.T) {
	cfg := testConfig(t)

	// Create a fake grepai that returns empty results
	scriptDir := t.TempDir()
	script := filepath.Join(scriptDir, "fake-grepai")
	os.WriteFile(script, []byte("#!/bin/sh\necho '[]'\n"), 0755)
	cfg.Grepai = config.GrepaiConfig{Binary: script}

	mock := &mockProvider{}
	c := setupClient(t, cfg, mock)

	result, err := c.CallTool(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "researcher_context",
			Arguments: map[string]any{
				"topic": "nonexistent topic xyz",
			},
		},
	})
	if err != nil {
		t.Fatalf("calling context: %v", err)
	}

	text := extractText(t, result)
	var resp contextResult
	if err := json.Unmarshal([]byte(text), &resp); err != nil {
		t.Fatalf("parsing context response: %v", err)
	}

	if resp.Count != 0 {
		t.Errorf("count = %d, want 0", resp.Count)
	}
	if len(resp.Sources) != 0 {
		t.Errorf("sources len = %d, want 0", len(resp.Sources))
	}
	if resp.Context != "" {
		t.Errorf("context = %q, want empty", resp.Context)
	}
}

// extractText gets the text content from a tool result.
func extractText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	if len(result.Content) == 0 {
		t.Fatal("expected content in result, got none")
	}
	tc, ok := mcp.AsTextContent(result.Content[0])
	if !ok {
		t.Fatal("expected text content")
	}
	return tc.Text
}
