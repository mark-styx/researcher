package rollup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/graph"
	"github.com/marklubin/researchguy/internal/llm"
)

type fakeProvider struct {
	resp string
	err  error
	n    int // number of Complete calls
}

func (f *fakeProvider) Complete(ctx context.Context, req llm.Request) (string, error) {
	f.n++
	if f.err != nil {
		return "", f.err
	}
	return f.resp, nil
}

func (f *fakeProvider) Name() string { return "fake" }

func testStore(t *testing.T) (*graph.Store, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("RESEARCHGUY_CONFIG_DIR", dir)
	store, err := graph.NewStore(&config.Config{})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store, dir
}

func staleNode(t *testing.T, store *graph.Store, researchDir, path string) *graph.Node {
	t.Helper()
	if err := os.WriteFile(filepath.Join(researchDir, path), []byte("v2 content"), 0644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	n := &graph.Node{Type: graph.NodeReport, Title: "Topic", Path: path}
	if err := store.CreateNode(n); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	future := n.UpdatedAt.Add(1 * time.Hour)
	if err := os.Chtimes(filepath.Join(researchDir, path), future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	return n
}

func TestRun_ResummarizesStaleNode(t *testing.T) {
	store, researchDir := testStore(t)
	n := staleNode(t, store, researchDir, "topic.md")

	cfg := &config.Config{ResearchDir: researchDir, Graph: config.GraphConfig{Rollup: config.GraphRollupConfig{MaxPerCycle: 5}}}
	provider := &fakeProvider{resp: "updated summary"}
	r := New(cfg, store, provider, nil)

	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got, err := store.GetNode(n.ID)
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if got.Metadata["pending_summary"] != "updated summary" {
		t.Errorf("pending_summary = %v, want %q", got.Metadata["pending_summary"], "updated summary")
	}
	if got.Metadata["needs_review"] != true {
		t.Errorf("needs_review = %v, want true", got.Metadata["needs_review"])
	}
	if got.Summary != "" {
		t.Errorf("Summary = %q, want untouched (empty)", got.Summary)
	}
}

func TestRun_MarksOrphanedNode(t *testing.T) {
	store, researchDir := testStore(t)
	n := &graph.Node{Type: graph.NodeReport, Title: "Gone", Path: "missing.md"}
	if err := store.CreateNode(n); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}

	cfg := &config.Config{ResearchDir: researchDir}
	r := New(cfg, store, &fakeProvider{}, nil)

	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got, err := store.GetNode(n.ID)
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if got.Metadata["orphaned"] != true {
		t.Errorf("orphaned = %v, want true", got.Metadata["orphaned"])
	}
}

func TestRun_LLMFailureLeavesNodeUntouched(t *testing.T) {
	store, researchDir := testStore(t)
	n := staleNode(t, store, researchDir, "topic.md")

	cfg := &config.Config{ResearchDir: researchDir}
	r := New(cfg, store, &fakeProvider{err: errors.New("backend unavailable")}, nil)

	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run should not propagate per-node LLM errors: %v", err)
	}

	got, err := store.GetNode(n.ID)
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if got.Metadata["pending_summary"] != nil {
		t.Errorf("pending_summary should be unset after LLM failure, got %v", got.Metadata["pending_summary"])
	}
	if got.Metadata["needs_review"] != nil {
		t.Errorf("needs_review should be unset after LLM failure, got %v", got.Metadata["needs_review"])
	}
}

func TestRun_RespectsMaxPerCycle(t *testing.T) {
	store, researchDir := testStore(t)
	staleNode(t, store, researchDir, "a.md")
	staleNode(t, store, researchDir, "b.md")
	staleNode(t, store, researchDir, "c.md")

	cfg := &config.Config{ResearchDir: researchDir, Graph: config.GraphConfig{Rollup: config.GraphRollupConfig{MaxPerCycle: 2}}}
	provider := &fakeProvider{resp: "summary"}
	r := New(cfg, store, provider, nil)

	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if provider.n != 2 {
		t.Errorf("Complete called %d times, want 2 (max_per_cycle)", provider.n)
	}
}

func TestRun_UnchangedNodeNotResummarized(t *testing.T) {
	store, researchDir := testStore(t)
	path := filepath.Join(researchDir, "topic.md")
	if err := os.WriteFile(path, []byte("v1"), 0644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	n := &graph.Node{Type: graph.NodeReport, Title: "Topic", Path: "topic.md"}
	if err := store.CreateNode(n); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	past := n.UpdatedAt.Add(-1 * time.Hour)
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	cfg := &config.Config{ResearchDir: researchDir}
	provider := &fakeProvider{resp: "summary"}
	r := New(cfg, store, provider, nil)

	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if provider.n != 0 {
		t.Errorf("Complete called %d times, want 0 (nothing stale)", provider.n)
	}
}

func TestResummarizationPrompt_FundingPatternHasNonClaimClause(t *testing.T) {
	p := ResummarizationPrompt(graph.NodeFundingPattern)
	if !strings.Contains(p, "EXPLICIT NON-CLAIM") || !strings.Contains(p, "SUFFICIENCY") {
		t.Errorf("funding-pattern prompt missing non-claim/sufficiency framing:\n%s", p)
	}
}

func TestResummarizationPrompt_OtherTypesDoNotHaveClause(t *testing.T) {
	p := ResummarizationPrompt(graph.NodeEntity)
	if strings.Contains(p, "EXPLICIT NON-CLAIM") {
		t.Errorf("entity prompt should not carry the funding-pattern clause:\n%s", p)
	}
}
