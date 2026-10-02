package research

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/marklubin/researchguy/internal/cite"
	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/critique"
	"github.com/marklubin/researchguy/internal/llm"
	"github.com/marklubin/researchguy/internal/store"
	"github.com/marklubin/researchguy/internal/store/retrieve"
)

// maxSourcesListed caps the sources table in the aggregator's prompt.
const maxSourcesListed = 200

// beforeAggregate is the hybrid backend's step between its workers and its
// aggregator: fetch what the workers found (not for asks, as in fetchRun),
// index and embed it, and give the aggregator the run's sources table and
// the read-profile tools. Without a run or an index there's nothing to
// add, and it's nil.
func (r *Runner) beforeAggregate(task Task, run *store.Run) func(context.Context) (llm.AggregateInput, error) {
	if run == nil || r.cfg.Store.DSN == "" {
		return nil
	}
	return func(ctx context.Context) (llm.AggregateInput, error) {
		r.fetchRun(ctx, task, run)
		r.indexRun(ctx, run)
		lctx, cancel := context.WithTimeout(ctx, indexTimeout)
		defer cancel()
		rt, err := retrieve.Open(lctx, r.cfg)
		if err != nil {
			return llm.AggregateInput{}, err
		}
		defer rt.Close()
		sources, err := rt.RunSources(lctx, run.ID(), maxSourcesListed)
		if err != nil {
			return llm.AggregateInput{}, fmt.Errorf("listing the run's sources: %w", err)
		}
		in := llm.AggregateInput{RunID: run.ID(), Sources: sources.Table(), Cited: r.citedPassages}
		if r.cfg.Hybrid.AggregatorTools {
			in.MCP = readProfile()
		}
		return in, nil
	}
}

// readProfile is the MCP server an aggregator gets: this binary's read
// profile, on this config.
func readProfile() []llm.MCPServer {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: no read-profile tools for the aggregator: %v\n", err)
		return nil
	}
	return []llm.MCPServer{{Name: "researchguy", Command: exe, Args: []string{"mcp", "--profile", "read"},
		Env: map[string]string{"RESEARCHGUY_CONFIG_DIR": config.Dir()}}}
}

// citedPassages is the text of the store passages a draft cites, for the
// critics. A down index gives them none.
func (r *Runner) citedPassages(ctx context.Context, draft string) []critique.Evidence {
	lctx, cancel := context.WithTimeout(ctx, indexTimeout)
	defer cancel()
	rt, err := retrieve.Open(lctx, r.cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: the critics won't see the passages the draft cites: %v\n", err)
		return nil
	}
	defer rt.Close()
	return cite.Passages(lctx, draft, rt)
}

// checkCitations resolves every citation in the run's report, writes them
// to the run's citations.jsonl and adds the failures to the report's notes.
// [E<n>] is checked against the run's captures; passages and sources need
// the index, and without one they're left unchecked. Problems are
// warnings: the report stands either way.
func (r *Runner) checkCitations(ctx context.Context, task Task, run *store.Run, path string) {
	if run == nil || path == "" {
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: citation check: %v\n", err)
		return
	}
	report := strings.TrimSpace(store.ReportSection(b, run.ID()))
	captures, err := store.ReadCaptures(run.Dir())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: citation check: reading captures: %v\n", err)
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), indexTimeout)
	defer cancel()
	var rt *retrieve.Retriever
	if r.cfg.Store.DSN != "" {
		if rt, err = retrieve.Open(cctx, r.cfg); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: citation check without the index, passages and sources unchecked: %v\n", err)
			rt = nil
		} else {
			defer rt.Close()
		}
	}
	cs := cite.Check(cctx, report, captures, rt)
	if err := store.WriteCitations(run.Dir(), cs); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: writing run record: %v\n", err)
	}
	sum := cite.Summarize(cs)
	if section := cite.NotesSection(report, sum.Notes()); section != "" {
		// The run's section ends the file, watch updates included.
		out := strings.TrimRight(string(b), "\n") + section
		if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: adding citation notes to %s: %v\n", path, err)
		}
	}
	if !task.Quiet && sum.Citations > 0 {
		fmt.Fprintf(os.Stderr, "Citations: %d (%d unresolved, %d not checked); quotes: %d (%d not found, %d unverifiable)\n",
			sum.Citations, sum.Unresolved, sum.Unchecked, sum.Quotes, sum.QuotesNotFound, sum.QuotesUnverified)
	}
}
