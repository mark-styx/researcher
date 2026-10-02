package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/embed"
	"github.com/marklubin/researchguy/internal/fetch"
	"github.com/marklubin/researchguy/internal/store"
	"github.com/marklubin/researchguy/internal/store/index"
	"github.com/spf13/cobra"
)

// fetchResult is one run's fetch pass.
type fetchResult struct {
	RunID string `json:"run_id"`
	store.FetchSummary
	// Busy is set when another process was fetching for the run.
	Busy  bool   `json:"busy,omitempty"`
	Error string `json:"error,omitempty"`
}

// fetchReport is what `store fetch` did.
type fetchReport struct {
	Runs []fetchResult `json:"runs"`
	// Indexed is the fetched runs' ingest, when store.dsn is set.
	Indexed *index.SyncStats `json:"indexed,omitempty"`
}

func storeFetchCmd() *cobra.Command {
	var runID, budget string
	var force, jsonOut bool
	cmd := &cobra.Command{
		Use:   "fetch",
		Short: "Fetch the documents runs cited, opened and ranked highest",
		Long: `Fetch downloads the sources of every finished run whose fetching isn't
done: the pages its report and worker drafts cite, the pages it opened,
and the top store.fetch.top_results results of each search. Raw bytes go
under <store.dir>/blobs/, extracted text under <store.dir>/text/, and
every attempt, failures included, into the run's fetches.jsonl. A paper
whose publisher blocks the fetch is looked up on OpenAlex for an abstract
and an open-access copy.

URLs a run already tried are skipped unless --force is set, which needs
--run. With store.dsn set, the runs fetched are indexed afterwards;
` + "`researchguy store embed`" + ` embeds their passages. This runs whether or not
store.fetch.enabled is set.`,
		Example: `  researchguy store fetch
  researchguy store fetch --run 20261002T174145Z-7761e0 --force
  researchguy store fetch --budget 0 --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			if force && runID == "" {
				return fmt.Errorf("--force re-fetches every URL a run tried; name the run with --run")
			}
			cfg, st, err := loadStore()
			if err != nil {
				return err
			}
			if budget != "" {
				cfg.Store.Fetch.Budget = budget
			}
			ids := []string{runID}
			if runID != "" {
				if err := checkRunID(st, runID); err != nil {
					return err
				}
			} else if ids, err = st.PendingFetch(); err != nil {
				return err
			}
			ctx := cmd.Context()
			rep := runFetches(ctx, cfg, st, ids, force, !jsonOut)
			if cfg.Store.DSN != "" {
				var fetched []string
				for _, r := range rep.Runs {
					if r.Error == "" && !r.Busy {
						fetched = append(fetched, r.RunID)
					}
				}
				stats, err := indexRuns(ctx, cfg, st, fetched)
				if err != nil {
					return err
				}
				rep.Indexed = &stats
			}
			if jsonOut {
				if err := printJSON(rep); err != nil {
					return err
				}
			} else {
				printFetchReport(rep)
			}
			failed := 0
			for _, r := range rep.Runs {
				if r.Error != "" {
					failed++
				}
			}
			if failed > 0 {
				return fmt.Errorf("%d run(s) failed to fetch", failed)
			}
			if rep.Indexed != nil {
				return failedErr(*rep.Indexed)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&runID, "run", "", "fetch only this run")
	cmd.Flags().BoolVar(&force, "force", false, "fetch URLs again even when the run already tried them (needs --run)")
	cmd.Flags().StringVar(&budget, "budget", "", `time limit per run, overriding store.fetch.budget ("0" for none)`)
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the result as JSON")
	return cmd
}

// runFetches runs the fetch stage for each run, printing a line per run to
// stderr when progress is set.
func runFetches(ctx context.Context, cfg *config.Config, st *store.Store, ids []string, force, progress bool) fetchReport {
	rep := fetchReport{Runs: []fetchResult{}}
	stage := fetch.NewStage(cfg.Store.Fetch, st)
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		if progress {
			fmt.Fprintf(os.Stderr, "Fetching sources for run %s...\n", id)
		}
		sum, err := stage.Run(ctx, id, force)
		res := fetchResult{RunID: id, FetchSummary: sum}
		switch {
		case errors.Is(err, store.ErrFetchBusy):
			res.Busy = true
		case err != nil:
			res.Error = err.Error()
		}
		rep.Runs = append(rep.Runs, res)
	}
	return rep
}

// indexRuns ingests runs into the index, attaching cached vectors.
func indexRuns(ctx context.Context, cfg *config.Config, st *store.Store, ids []string) (index.SyncStats, error) {
	stats := index.SyncStats{Ingested: []index.RunStats{}}
	if len(ids) == 0 {
		return stats, nil
	}
	ix, err := openIndex(ctx, cfg)
	if err != nil {
		return stats, err
	}
	defer ix.Close()
	for _, id := range ids {
		rs, err := ix.IngestRun(ctx, st.RunDir(id), false)
		switch {
		case err != nil:
			if stats.Failed == nil {
				stats.Failed = map[string]string{}
			}
			stats.Failed[id] = err.Error()
		case rs.Skipped:
			stats.UpToDate++
		default:
			stats.Ingested = append(stats.Ingested, rs)
		}
	}
	return stats, nil
}

func printFetchReport(rep fetchReport) {
	if len(rep.Runs) == 0 {
		fmt.Println("No runs waiting on fetching.")
	}
	for _, r := range rep.Runs {
		switch {
		case r.Busy:
			fmt.Printf("%s: another process is fetching for it\n", r.RunID)
		case r.Error != "":
			fmt.Fprintf(os.Stderr, "Failed %s: %s\n", r.RunID, r.Error)
		default:
			fmt.Printf("%s: fetched %d of %d source(s), %d failed", r.RunID, r.Fetched, r.Queued, r.Failed)
			if r.Skipped > 0 {
				fmt.Printf(", %d tried before", r.Skipped)
			}
			if r.Remaining > 0 {
				fmt.Printf(", %d left for the next pass", r.Remaining)
			}
			fmt.Println()
		}
	}
	if rep.Indexed != nil && (len(rep.Indexed.Ingested) > 0 || len(rep.Indexed.Failed) > 0) {
		printSyncStats(*rep.Indexed, "Indexed")
	}
}

func storeEmbedCmd() *cobra.Command {
	var budget string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "embed",
		Short: "Embed indexed passages that have no vector yet",
		Long: `Embed gives every passage in the index a vector from store.embed.model
(through Ollama), and re-embeds passages a different model embedded. A
vector cached under <store.dir>/vectors/ is used instead of calling the
model, and new vectors are cached there, so a rebuild doesn't embed again.
Passages only reach the index through ingest, so run ` + "`researchguy store fetch`" + `
or ` + "`researchguy store ingest`" + ` first.`,
		Example: `  researchguy store embed
  researchguy store embed --budget 10m --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			cfg, st, err := loadStore()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			ix, err := openIndex(ctx, cfg)
			if err != nil {
				return err
			}
			defer ix.Close()
			if budget != "" && budget != "0" {
				d, err := time.ParseDuration(budget)
				if err != nil || d <= 0 {
					return fmt.Errorf("invalid --budget %q", budget)
				}
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, d)
				defer cancel()
			}
			emb := embed.New(cfg)
			if !jsonOut {
				fmt.Fprintf(os.Stderr, "Embedding with %s at %s...\n", emb.Model(), emb.Host)
			}
			stats, err := ix.Embed(ctx, st, emb)
			if jsonOut {
				if perr := printJSON(stats); perr != nil {
					return perr
				}
			} else {
				fmt.Printf("Embedded %d passage(s) with %s, %d from cache; %d left\n",
					stats.Embedded+stats.FromCache, stats.Model, stats.FromCache, stats.Remaining)
			}
			if err != nil && ctx.Err() != nil && cmd.Context().Err() == nil {
				fmt.Fprintln(os.Stderr, "Stopped at --budget; run it again to continue.")
				return nil
			}
			return err
		},
	}
	cmd.Flags().StringVar(&budget, "budget", "", "stop after this long (default: until done)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the result as JSON")
	return cmd
}
