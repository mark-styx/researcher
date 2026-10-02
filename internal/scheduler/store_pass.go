package scheduler

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

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
// before fetching), indexes runs the index lacks, then embeds passages
// that have no vector.
func (s *Scheduler) storePass(ctx context.Context) {
	s.fetchPending(ctx)
	if s.cfg.Store.DSN == "" || ctx.Err() != nil {
		return
	}
	s.syncIndex(ctx)
	s.embedPending(ctx)
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
	stats, err := s.index.Embed(ectx, st, embed.New(s.cfg))
	if n := stats.Embedded + stats.FromCache; n > 0 {
		s.logger.Printf("Embedded %d passage(s) with %s (%d from cache, %d left)", n, stats.Model, stats.FromCache, stats.Remaining)
	}
	status := ""
	if err != nil && ectx.Err() == nil {
		status = err.Error()
	}
	s.logStatus(&s.embedStatus, "Embedding", status)
}
