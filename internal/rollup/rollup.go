// Package rollup gives the scheduler daemon a job for the knowledge graph:
// periodically re-summarize nodes whose linked content has changed, instead
// of leaving Summary stuck at whatever it was when the node was created.
// See docs/node-rollup-design.md for the full design.
package rollup

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/graph"
	"github.com/marklubin/researchguy/internal/llm"
)

const defaultMaxPerCycle = 5

// Rollup runs one pass at a time over stale/orphaned graph nodes. It never
// writes Summary directly — resummarization proposals go into
// Metadata["pending_summary"] with Metadata["needs_review"] = true, and a
// human confirms them via "researchguy graph approve".
type Rollup struct {
	cfg      *config.Config
	store    *graph.Store
	provider llm.Provider
	logger   *log.Logger
}

func New(cfg *config.Config, store *graph.Store, provider llm.Provider, logger *log.Logger) *Rollup {
	return &Rollup{cfg: cfg, store: store, provider: provider, logger: logger}
}

// Run performs one rollup pass: flags nodes whose linked file has
// disappeared as orphaned, then resummarizes up to graph.rollup.max_per_cycle
// nodes whose linked file changed since they were last summarized.
func (r *Rollup) Run(ctx context.Context) error {
	researchDir := config.ExpandPath(r.cfg.ResearchDir)

	stale, orphaned, err := r.store.ListStale(researchDir)
	if err != nil {
		return fmt.Errorf("listing stale nodes: %w", err)
	}

	for _, n := range orphaned {
		if n.Metadata == nil {
			n.Metadata = map[string]any{}
		}
		n.Metadata["orphaned"] = true
		if err := r.store.UpdateNode(n); err != nil {
			r.logf("marking node %s orphaned: %v", shortID(n.ID), err)
			continue
		}
		r.logf("node %s orphaned: linked file %q no longer exists", shortID(n.ID), n.Path)
	}

	maxPerCycle := r.cfg.Graph.Rollup.MaxPerCycle
	if maxPerCycle <= 0 {
		maxPerCycle = defaultMaxPerCycle
	}
	if len(stale) > maxPerCycle {
		r.logf("%d stale nodes found, processing %d this cycle (graph.rollup.max_per_cycle=%d)", len(stale), maxPerCycle, maxPerCycle)
		stale = stale[:maxPerCycle]
	}

	for _, n := range stale {
		// LLM or DB failure: leave the node untouched, skip, retry next
		// poll cycle. No new retry/backoff infrastructure — matches how
		// the existing scheduler already just tries again next tick.
		if err := r.resummarize(ctx, n, researchDir); err != nil {
			r.logf("resummarizing node %s: %v (will retry next cycle)", shortID(n.ID), err)
		}
	}
	return nil
}

func (r *Rollup) resummarize(ctx context.Context, n *graph.Node, researchDir string) error {
	content, err := os.ReadFile(filepath.Join(researchDir, n.Path))
	if err != nil {
		return fmt.Errorf("reading linked file: %w", err)
	}

	resp, err := r.provider.Complete(ctx, llm.Request{
		SystemPrompt: ResummarizationPrompt(n.Type),
		UserPrompt:   fmt.Sprintf("Node title: %s\n\n%s", n.Title, string(content)),
	})
	if err != nil {
		return fmt.Errorf("LLM call: %w", err)
	}

	if n.Metadata == nil {
		n.Metadata = map[string]any{}
	}
	n.Metadata["pending_summary"] = resp
	n.Metadata["needs_review"] = true
	if err := r.store.UpdateNode(n); err != nil {
		return fmt.Errorf("saving pending summary: %w", err)
	}
	r.logf("node %s resummarized, pending review", shortID(n.ID))
	return nil
}

func (r *Rollup) logf(format string, args ...any) {
	if r.logger != nil {
		r.logger.Printf("rollup: "+format, args...)
	}
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
