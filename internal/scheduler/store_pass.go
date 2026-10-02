package scheduler

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/marklubin/researchguy/internal/claims"
	"github.com/marklubin/researchguy/internal/embed"
	"github.com/marklubin/researchguy/internal/fetch"
	runstore "github.com/marklubin/researchguy/internal/store"
)

// maxFetchRuns bounds how many runs one store pass fetches sources for,
// newest first, so a backlog doesn't hold up indexing.
const maxFetchRuns = 4

// startStorePass runs a store pass in the background unless one is still
// running. Stop cancels it and waits for it.
func (s *Scheduler) startStorePass(ctx context.Context) {
	if !s.passing.CompareAndSwap(false, true) {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer s.passing.Store(false)
		s.storePass(ctx)
	}()
}

// storePass catches the store and index up: it fetches sources for runs
// that haven't had them fetched (asks, runs whose budget ran out, runs from
// before fetching), indexes runs the index lacks, embeds passages that
// have no vector, extracts claims from documents waiting on them, then
// labels how new claims relate to their nearest claims.
func (s *Scheduler) storePass(ctx context.Context) {
	s.fetchPending(ctx)
	if s.cfg.Store.DSN == "" || ctx.Err() != nil {
		return
	}
	s.syncIndex(ctx)
	s.embedPending(ctx)
	s.extractClaims(ctx)
	s.linkClaims(ctx)
}

// fetchPending runs the fetch stage for up to maxFetchRuns finished runs
// whose fetching isn't done. A run another process is fetching is skipped.
func (s *Scheduler) fetchPending(ctx context.Context) {
	if !s.cfg.Store.Fetch.Enabled {
		return
	}
	status := ""
	defer func() { s.logStatus(&s.fetchStatus, "Source fetch", status) }()
	st, err := runstore.Open(s.cfg.Store.Dir)
	if err != nil {
		status = err.Error()
		return
	}
	ids, err := st.PendingFetch()
	if err != nil {
		status = err.Error()
		return
	}
	slices.Reverse(ids) // run ids sort by start second
	stage := fetch.NewStage(s.cfg.Store.Fetch, st)
	var problems []string
	done := 0
	for _, id := range ids {
		if done == maxFetchRuns || ctx.Err() != nil {
			break
		}
		sum, err := stage.Run(ctx, id, false)
		if errors.Is(err, runstore.ErrFetchBusy) {
			continue
		}
		done++
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", id, err))
			continue
		}
		if sum.Records > 0 || sum.Remaining > 0 {
			s.logger.Printf("Fetched sources for run %s: %d of %d (%d failed, %d left)", id, sum.Fetched, sum.Queued, sum.Failed, sum.Remaining)
		}
	}
	if len(problems) > 0 {
		status = fmt.Sprintf("%d run(s) failed to fetch; %s", len(problems), strings.Join(problems, "; "))
	}
}

// embedPending embeds passages without a vector from the configured model,
// within store.embed.budget; the rest wait for the next pass. Running out
// of budget isn't a problem, a down model is.
func (s *Scheduler) embedPending(ctx context.Context) {
	if s.index == nil {
		return
	}
	st, err := runstore.Open(s.cfg.Store.Dir)
	if err != nil {
		s.logStatus(&s.embedStatus, "Embedding", err.Error())
		return
	}
	ectx, cancel := context.WithTimeout(ctx, s.cfg.Store.Embed.BudgetDuration())
	defer cancel()
	emb := embed.New(s.cfg)
	stats, err := s.index.Embed(ectx, st, emb)
	if n := stats.Embedded + stats.FromCache; n > 0 {
		s.logger.Printf("Embedded %d passage(s) with %s (%d from cache, %d left)", n, stats.Model, stats.FromCache, stats.Remaining)
	}
	if err == nil {
		cs, cerr := s.index.EmbedClaims(ectx, st, emb)
		err = cerr
		if n := cs.Embedded + cs.FromCache; n > 0 {
			s.logger.Printf("Embedded %d claim(s) with %s (%d from cache, %d left)", n, cs.Model, cs.FromCache, cs.Remaining)
		}
	}
	status := ""
	if err != nil && ectx.Err() == nil {
		status = err.Error()
	}
	s.logStatus(&s.embedStatus, "Embedding", status)
}

// extractClaims extracts claims from indexed documents waiting on them,
// within store.claims.budget. Research tasks come first, since both want
// the GPU: it doesn't start while a task runs and stops before its next
// chunk when one starts. What it finished is kept either way.
func (s *Scheduler) extractClaims(ctx context.Context) {
	c := s.cfg.Store.Claims
	if s.index == nil || !c.Enabled || ctx.Err() != nil {
		return
	}
	busy := func() bool { return len(s.sem) > 0 }
	if busy() {
		return
	}
	status := ""
	defer func() { s.logStatus(&s.claimsStatus, "Claim extraction", status) }()
	st, err := runstore.Open(s.cfg.Store.Dir)
	if err != nil {
		status = err.Error()
		return
	}
	ex := claims.New(s.cfg)
	if ex.Model == "" {
		status = "no model; set store.claims.model or ollama.utility_model"
		return
	}
	cctx, cancel := context.WithTimeout(ctx, c.BudgetDuration())
	defer cancel()
	stats, err := claims.Run(cctx, s.index, st, ex, claims.Options{MaxAttempts: c.MaxAttempts, Pause: busy, Embedder: embed.New(s.cfg)})
	if stats.Extracted > 0 {
		s.logger.Printf("Extracted %d claim(s) from %d text(s) with %s (%d left, %d with failed chunks)",
			stats.Claims, stats.Extracted, ex.Model, stats.Waiting-stats.Extracted+stats.Failed, stats.Failed)
	}
	if err != nil {
		status = err.Error()
	}
}

// linkClaims labels how claims the linker hasn't checked relate to their
// nearest claims from other origins, within store.claims.budget and the
// link's daily cap. Like extraction, it waits for research tasks.
func (s *Scheduler) linkClaims(ctx context.Context) {
	c := s.cfg.Store.Claims
	if s.index == nil || !c.Enabled || !c.Link.Enabled || ctx.Err() != nil {
		return
	}
	busy := func() bool { return len(s.sem) > 0 }
	if busy() {
		return
	}
	status := ""
	defer func() { s.logStatus(&s.linkStatus, "Claim linking", status) }()
	st, err := runstore.Open(s.cfg.Store.Dir)
	if err != nil {
		status = err.Error()
		return
	}
	p, opts, err := claims.NewLinker(s.cfg)
	if err != nil {
		status = err.Error()
		return
	}
	opts.Pause = busy
	lctx, cancel := context.WithTimeout(ctx, c.BudgetDuration())
	defer cancel()
	stats, err := claims.Link(lctx, s.index, st, p, opts)
	if stats.Pairs > 0 {
		s.logger.Printf("Labeled %d claim pair(s) with %s: %d linked, %d unanswered", stats.Pairs, opts.Model, stats.Linked, stats.Unanswered)
	}
	if err != nil && lctx.Err() == nil {
		status = err.Error()
	}
}
