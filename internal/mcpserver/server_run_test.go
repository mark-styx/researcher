package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/marklubin/researchguy/internal/llm"
	"github.com/marklubin/researchguy/internal/store"
)

// failingProvider fails every completion, like an aggregator that errors
// after the workers have collected evidence.
type failingProvider struct{}

func (failingProvider) Name() string { return "failing" }
func (failingProvider) Complete(context.Context, llm.Request) (string, error) {
	return "", errors.New("aggregator failed")
}

func callTool(t *testing.T, env *mcpTestEnv, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := env.client.CallTool(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{Name: name, Arguments: args},
	})
	if err != nil {
		t.Fatalf("calling %s: %v", name, err)
	}
	return result
}

type mcpTestEnv struct {
	storeDir string
	client   interface {
		CallTool(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)
	}
}

func storeEnv(t *testing.T, provider llm.Provider) (*mcpTestEnv, string) {
	t.Helper()
	cfg := testConfig(t)
	cfg.Store.Dir = filepath.Join(t.TempDir(), "store")
	return &mcpTestEnv{storeDir: cfg.Store.Dir, client: setupClient(t, cfg, provider)}, cfg.ResearchDir
}

func assertRunDir(t *testing.T, env *mcpTestEnv, id, dir string) {
	t.Helper()
	if id == "" || dir != filepath.Join(env.storeDir, "runs", id) {
		t.Fatalf("run_id %q run_dir %q, want a run under %s", id, dir, env.storeDir)
	}
	if _, err := store.ReadRecord(dir); err != nil {
		t.Errorf("run record at %s: %v", dir, err)
	}
}

func TestMCPServer_AskReturnsRun(t *testing.T) {
	env, _ := storeEnv(t, &mockProvider{responses: []string{
		"the answer",
		`{"category": "general", "filename": "answer"}`,
	}})
	result := callTool(t, env, "researchguy_ask", map[string]any{"question": "q?", "no_research": true})

	var resp map[string]string
	if err := json.Unmarshal([]byte(extractText(t, result)), &resp); err != nil {
		t.Fatal(err)
	}
	assertRunDir(t, env, resp["run_id"], resp["run_dir"])
}

func TestMCPServer_EnrichReturnsRun(t *testing.T) {
	env, researchDir := storeEnv(t, &mockProvider{response: "enriched"})
	if err := os.MkdirAll(filepath.Join(researchDir, "llm"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(researchDir, "llm", "agents.md"), []byte("# Agents"), 0o644); err != nil {
		t.Fatal(err)
	}
	result := callTool(t, env, "researchguy_enrich", map[string]any{"path": "llm/agents.md"})

	var resp map[string]string
	if err := json.Unmarshal([]byte(extractText(t, result)), &resp); err != nil {
		t.Fatal(err)
	}
	assertRunDir(t, env, resp["run_id"], resp["run_dir"])
}

func TestMCPServer_DiveReturnsRun(t *testing.T) {
	env, _ := storeEnv(t, &mockProvider{response: "report"})
	result := callTool(t, env, "researchguy_dive", map[string]any{"topic": "t", "no_research": true})

	var resp map[string]any
	if err := json.Unmarshal([]byte(extractText(t, result)), &resp); err != nil {
		t.Fatal(err)
	}
	id, _ := resp["run_id"].(string)
	dir, _ := resp["run_dir"].(string)
	assertRunDir(t, env, id, dir)
}

func TestMCPServer_FailedTaskNamesRunRecord(t *testing.T) {
	env, _ := storeEnv(t, failingProvider{})
	result := callTool(t, env, "researchguy_dive", map[string]any{"topic": "t", "no_research": true})
	if !result.IsError {
		t.Fatal("want an error result")
	}
	text := extractText(t, result)
	if !strings.Contains(text, "aggregator failed") {
		t.Errorf("error = %q, want the provider error", text)
	}
	_, dir, ok := strings.Cut(text, "run record: ")
	if !ok {
		t.Fatalf("error = %q, want the run record dir", text)
	}
	rec, err := store.ReadRecord(strings.TrimSpace(dir))
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != store.StatusFailed {
		t.Errorf("run status = %q, want failed", rec.Status)
	}
}

func TestMCPServer_NoStoreNoRunFields(t *testing.T) {
	c := setupClient(t, testConfig(t), &mockProvider{responses: []string{
		"the answer",
		`{"category": "general", "filename": "answer"}`,
	}})
	result := callTool(t, &mcpTestEnv{client: c}, "researchguy_ask", map[string]any{"question": "q?", "no_research": true})

	var resp map[string]string
	if err := json.Unmarshal([]byte(extractText(t, result)), &resp); err != nil {
		t.Fatal(err)
	}
	if _, ok := resp["run_id"]; ok {
		t.Errorf("ask without a store returned run_id: %v", resp)
	}

	c = setupClient(t, testConfig(t), failingProvider{})
	result = callTool(t, &mcpTestEnv{client: c}, "researchguy_dive", map[string]any{"topic": "t", "no_research": true})
	if text := extractText(t, result); strings.Contains(text, "run record") {
		t.Errorf("error names a run record that doesn't exist: %q", text)
	}
}
