package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/marklubin/researchguy/internal/claims"
	"github.com/marklubin/researchguy/internal/embed"
	"github.com/spf13/cobra"
)

func storeExtractCmd() *cobra.Command {
	var budget string
	var limit int
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "extract",
		Short: "Extract checkable claims from indexed documents",
		Long: `Extract asks store.claims.model (through Ollama) for the checkable claims
in each indexed primary document that has none yet, documents a report
cites first, then the most recently fetched. Each claim comes with the
quote the model says states it; the index checks the quote against the
document's text, and a claim whose quote isn't found is never shown as
quoted.

Extractions are written to <store.dir>/claims/ and indexed as they finish,
so stopping early keeps the work done. A chunk the model fails on is
retried on later runs, up to store.claims.max_attempts. The daemon runs
this in the background between research tasks.`,
		Example: `  researchguy store extract --limit 5
  researchguy store extract --budget 30m --json`,
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
			if limit < 0 {
				return fmt.Errorf("invalid --limit %d", limit)
			}
			ex := claims.New(cfg)
			if ex.Model == "" {
				return fmt.Errorf("no model to extract claims with; set store.claims.model or ollama.utility_model")
			}
			fmt.Fprintf(os.Stderr, "Extracting claims with %s at %s...\n", ex.Model, ex.Host)
			stats, err := claims.Run(ctx, ix, st, ex, claims.Options{
				Limit:       limit,
				MaxAttempts: cfg.Store.Claims.MaxAttempts,
				Embedder:    embed.New(cfg),
				Progress: func(format string, args ...any) {
					fmt.Fprintf(os.Stderr, format+"\n", args...)
				},
			})
			if jsonOut {
				if perr := printJSON(stats); perr != nil {
					return perr
				}
			} else {
				printExtractStats(stats)
			}
			if stats.Stopped && err == nil && ctx.Err() != nil && cmd.Context().Err() == nil {
				fmt.Fprintln(os.Stderr, "Stopped at --budget; run it again to continue.")
			}
			return err
		},
	}
	cmd.Flags().StringVar(&budget, "budget", "", "stop after this long (default: until done)")
	cmd.Flags().IntVar(&limit, "limit", 0, "extract at most this many documents' texts (0: no limit)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the result as JSON")
	return cmd
}

func printExtractStats(s claims.Stats) {
	fmt.Printf("Extracted %d claim(s) from %d of %d waiting text(s) with %s\n", s.Claims, s.Extracted, s.Waiting, s.Extractor)
	if s.Failed > 0 {
		fmt.Printf("  %d text(s) have chunks the model failed on; they're retried next run\n", s.Failed)
	}
	if s.Exhausted > 0 {
		fmt.Printf("  %d text(s) are out of attempts (store.claims.max_attempts)\n", s.Exhausted)
	}
	if s.Missing > 0 {
		fmt.Printf("  %d text(s) are indexed but missing from the store\n", s.Missing)
	}
	if c := s.Sync; c != nil {
		fmt.Printf("Indexed %d claim(s), %d with their quote found; %d linked by shared quotes\n", c.Claims, c.Verified, c.RuleLinks)
		for path, msg := range c.Failed {
			fmt.Printf("  %s: %s\n", path, msg)
		}
	}
	if e := s.Embed; e != nil && e.Embedded+e.FromCache > 0 {
		fmt.Printf("Embedded %d claim(s) with %s, %d from cache\n", e.Embedded+e.FromCache, e.Model, e.FromCache)
	}
}
