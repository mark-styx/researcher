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
	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/llm"
	"github.com/marklubin/researchguy/internal/search"
)

// namedProvider is a mockProvider that reports a chosen backend name.
type namedProvider struct {
	mockProvider
	name string
}

func (n *namedProvider) Name() string { return n.name }

func call(t *testing.T, cfg *config.Config, provider llm.Provider, tool string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	c := setupClient(t, cfg, provider)
	res, err := c.CallTool(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{Name: tool, Arguments: args},
	})
	if err != nil {
		t.Fatalf("calling %s: %v", tool, err)
	}
	return res
}

func decode(t *testing.T, res *mcp.CallToolResult, v any) {
	t.Helper()
	if res.IsError {
		t.Fatalf("tool error: %s", extractText(t, res))
	}
	if err := json.Unmarshal([]byte(extractText(t, res)), v); err != nil {
		t.Fatalf("decoding %q: %v", extractText(t, res), err)
	}
}

// grepaiScript records its args and prints results as grepai --json would.
func grepaiScript(t *testing.T, cfg *config.Config, results string) string {
	t.Helper()
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	script := filepath.Join(dir, "fake-grepai")
	body := "#!/bin/sh\necho \"$@\" > " + argsFile + "\ncat <<'JSONEOF'\n" + results + "\nJSONEOF\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg.Grepai.Binary = script
	return argsFile
}

func TestDive_HybridOptionsPassThrough(t *testing.T) {
	cfg := testConfig(t)
	hybrid := &namedProvider{name: "hybrid", mockProvider: mockProvider{responses: []string{`{"category":"c","filename":"f"}`, "report body"}}}
	var gotBackend string
	orig := newProvider
	newProvider = func(_ *config.Config, backend, _ string) (llm.Provider, error) {
		gotBackend = backend
		return hybrid, nil
	}
	t.Cleanup(func() { newProvider = orig })

	def := &mockProvider{response: "default"}
	res := call(t, cfg, def, "researchguy_dive", map[string]any{
		"topic": "t", "backend": "hybrid", "mode": "inquiry", "branches": 3, "no_research": true,
	})
	var out map[string]any
	decode(t, res, &out)

	if gotBackend != "hybrid" || out["backend"] != "hybrid" || out["warning"] != nil {
		t.Fatalf("backend = %q, out = %v", gotBackend, out)
	}
	if len(def.calls) != 0 {
		t.Fatal("default provider should not be used when backend is set")
	}
	last := hybrid.calls[len(hybrid.calls)-1]
	if last.Mode != "inquiry" || last.BranchCount != 3 {
		t.Fatalf("request mode/branches = %q/%d", last.Mode, last.BranchCount)
	}
}

func TestResearchTools_ModeWithoutHybridWarns(t *testing.T) {
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"researchguy_dive", map[string]any{"topic": "t"}},
		{"researchguy_review", map[string]any{"topic": "t"}},
		{"researchguy_compare", map[string]any{"subject1": "a", "subject2": "b"}},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			cfg := testConfig(t)
			tc.args["mode"] = "landscape"
			tc.args["no_research"] = true
			mock := &mockProvider{responses: []string{`{"category":"c","filename":"f"}`, "body"}}
			var out map[string]any
			decode(t, call(t, cfg, mock, tc.tool, tc.args), &out)
			if w, _ := out["warning"].(string); !strings.Contains(w, "not hybrid") {
				t.Fatalf("expected warning, got %v", out)
			}
			if mock.calls[len(mock.calls)-1].Mode != "landscape" {
				t.Fatal("mode not passed through")
			}
		})
	}
}

func TestResearchTools_RejectBadParams(t *testing.T) {
	orig := newProvider
	newProvider = func(*config.Config, string, string) (llm.Provider, error) { return nil, errors.New("no ollama") }
	t.Cleanup(func() { newProvider = orig })

	for name, args := range map[string]map[string]any{
		"mode":           {"topic": "t", "mode": "deep"},
		"branches":       {"topic": "t", "branches": -1},
		"backend":        {"topic": "t", "backend": "gpt"},
		"provider error": {"topic": "t", "backend": "ollama"},
	} {
		t.Run(name, func(t *testing.T) {
			res := call(t, testConfig(t), &mockProvider{}, "researchguy_dive", args)
			if !res.IsError {
				t.Fatalf("expected error, got %s", extractText(t, res))
			}
		})
	}
}

func TestSearchAndContext_Projects(t *testing.T) {
	cfg := testConfig(t)
	book := t.TempDir()
	if err := os.MkdirAll(filepath.Join(book, "research"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(book, "research", "notes.md"), []byte("book notes"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := filepath.Join(t.TempDir(), "workspace.yaml")
	if err := os.WriteFile(registry, []byte("workspaces:\n  ws:\n    projects:\n      - name: book\n        path: "+book+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg.Grepai.Workspace = "ws"
	cfg.Grepai.Project = "research"
	cfg.Grepai.WorkspaceFile = registry
	argsFile := grepaiScript(t, cfg, `[{"file_path":"ws/book/research/notes.md","start_line":1,"end_line":1,"score":0.8,"content":"book notes"}]`)

	mock := &mockProvider{}
	var hits []search.SearchResult
	decode(t, call(t, cfg, mock, "researchguy_search", map[string]any{"query": "q", "projects": []any{"book", "research"}}), &hits)
	args, _ := os.ReadFile(argsFile)
	if !strings.Contains(string(args), "--project book --project research") {
		t.Fatalf("grepai args = %s", args)
	}
	if len(hits) != 1 || hits[0].Project != "book" || hits[0].Path != filepath.Join(book, "research", "notes.md") {
		t.Fatalf("hits = %+v", hits)
	}

	var ctxOut search.ContextResult
	decode(t, call(t, cfg, mock, "researchguy_context", map[string]any{"topic": "q", "projects": []any{"book"}, "max_age": "none"}), &ctxOut)
	if ctxOut.Count != 1 || !strings.Contains(ctxOut.Context, "--- Source: book/research/notes.md ---\nbook notes") {
		t.Fatalf("context = %+v", ctxOut)
	}
	if len(mock.calls) != 0 {
		t.Fatalf("context/search made %d provider calls, want 0", len(mock.calls))
	}

	res := call(t, cfg, mock, "researchguy_context", map[string]any{"topic": "q", "max_age": "bogus"})
	if !res.IsError || !strings.Contains(extractText(t, res), "invalid max_age") {
		t.Fatalf("bad max_age: %s", extractText(t, res))
	}
}

func readRootFixture(t *testing.T) (*config.Config, string) {
	t.Helper()
	cfg := testConfig(t)
	root := t.TempDir()
	book := filepath.Join(root, "the_book")
	if err := os.MkdirAll(filepath.Join(book, "research"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(book, "research", "web.md"), []byte("book research"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := filepath.Join(t.TempDir(), "workspace.yaml")
	if err := os.WriteFile(registry, []byte("workspaces:\n  ws:\n    projects:\n      - name: the_book\n        path: "+book+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg.ReadRoots = []string{root}
	cfg.Grepai.Workspace = "ws"
	cfg.Grepai.WorkspaceFile = registry
	return cfg, book
}

func TestRead_ReadRoots(t *testing.T) {
	cfg, book := readRootFixture(t)
	for _, p := range []string{filepath.Join(book, "research", "web.md"), "ws/the_book/research/web.md"} {
		res := call(t, cfg, &mockProvider{}, "researchguy_read", map[string]any{"path": p})
		if res.IsError || extractText(t, res) != "book research" {
			t.Fatalf("read %q: %s", p, extractText(t, res))
		}
	}
}

func TestRead_ReadRootsDenied(t *testing.T) {
	cfg, book := readRootFixture(t)
	outside := filepath.Join(t.TempDir(), "secret.md")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(book, "research", "escape.md")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]string{
		"absolute outside":     outside,
		"symlink escape":       link,
		"workspace symlink":    "ws/the_book/research/escape.md",
		"traversal":            "../../etc/passwd",
		"unknown project":      "ws/nope/x.md",
		"workspace traversal":  "ws/the_book/../../../etc/passwd",
		"no read roots at all": filepath.Join(book, "research", "web.md"),
	} {
		t.Run(name, func(t *testing.T) {
			c := *cfg
			if name == "no read roots at all" {
				c.ReadRoots = nil
			}
			res := call(t, &c, &mockProvider{}, "researchguy_read", map[string]any{"path": p})
			if !res.IsError {
				t.Fatalf("expected denial, got %q", extractText(t, res))
			}
		})
	}
}
