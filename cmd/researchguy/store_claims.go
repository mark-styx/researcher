package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/marklubin/researchguy/internal/claims"
	"github.com/marklubin/researchguy/internal/embed"
	"github.com/marklubin/researchguy/internal/store"
	"github.com/marklubin/researchguy/internal/store/retrieve"
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

func storeLinkCmd() *cobra.Command {
	var budget string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "link",
		Short: "Label how claims from different sources relate",
		Long: `Link compares each claim the linker hasn't checked with its nearest claims
from other origins (store.claims.link.neighbors, at least min_similarity
close, sharing a word) and has store.claims.link.model label each pair
same, supports, contradicts, refines, supersedes or unrelated. Labels are
logged to <store.dir>/links.jsonl and indexed; at most max_pairs_per_day
pairs are sent a day. The daemon runs this between research tasks.

` + "`researchguy store link set`" + ` records your own label for a pair, which
outranks the model's.`,
		Example: `  researchguy store link
  researchguy store link --budget 5m --json
  researchguy store link set C:123 C:456 supersedes --note "2025 figures"`,
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
			p, opts, err := claims.NewLinker(cfg)
			if err != nil {
				return err
			}
			opts.Progress = func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }
			fmt.Fprintf(os.Stderr, "Linking claims with %s...\n", opts.Model)
			stats, err := claims.Link(ctx, ix, st, p, opts)
			if jsonOut {
				if perr := printJSON(stats); perr != nil {
					return perr
				}
			} else {
				printLinkStats(stats)
			}
			if stats.Stopped && err == nil && ctx.Err() != nil && cmd.Context().Err() == nil {
				fmt.Fprintln(os.Stderr, "Stopped at --budget; run it again to continue.")
			}
			return err
		},
	}
	cmd.Flags().StringVar(&budget, "budget", "", "stop after this long (default: until done)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the result as JSON")
	cmd.AddCommand(storeLinkSetCmd())
	return cmd
}

func printLinkStats(s claims.LinkStats) {
	fmt.Printf("Checked %d claim(s); labeled %d pair(s) with %s, %d linked\n", s.Checked, s.Pairs, s.Model, s.Linked)
	for _, rel := range sortedKeys(s.ByRelation) {
		fmt.Printf("  %s: %d\n", rel, s.ByRelation[rel])
	}
	if s.Unanswered > 0 {
		fmt.Printf("  %d pair(s) the model's answer left out or got wrong\n", s.Unanswered)
	}
	if s.Capped {
		fmt.Println("Stopped at store.claims.link.max_pairs_per_day; the rest wait for tomorrow.")
	}
}

func storeLinkSetCmd() *cobra.Command {
	var note string
	var force, jsonOut bool
	cmd := &cobra.Command{
		Use:   "set <from> <to> <relation>",
		Short: "Record how two claims relate, over the model's label",
		Long: `Set logs your label for a pair of claims to links.jsonl, where it outranks
any model or rule label for the pair. relation is one of same, supports,
contradicts, refines, supersedes or unrelated; for refines and supersedes,
<from> is the claim that refines or supersedes <to>. Use unrelated to
overrule a wrong label.`,
		Example: `  researchguy store link set C:123 C:456 supersedes
  researchguy store link set 123 456 unrelated --note "different bridges"`,
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			from, err := parseID(args[0], retrieve.RefClaim)
			if err != nil {
				return err
			}
			to, err := parseID(args[1], retrieve.RefClaim)
			if err != nil {
				return err
			}
			link := store.Link{From: from, To: to, Relation: strings.ToLower(args[2]), Method: store.LinkHuman,
				Note: strings.TrimSpace(note), CreatedAt: time.Now().UTC()}
			if err := link.Check(); err != nil {
				return err
			}
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
			found, err := ix.ClaimsExist(ctx, []int64{from, to})
			if err != nil {
				return err
			}
			for _, id := range []int64{from, to} {
				if !found[id] && !force {
					return fmt.Errorf("no claim %s in the index (--force logs the link anyway)", retrieve.ClaimRef(id))
				}
			}
			if err := st.AppendLinks([]store.Link{link}); err != nil {
				return err
			}
			cs, err := ix.SyncClaims(ctx, st)
			if err != nil {
				return fmt.Errorf("logged, but indexing it failed: %w", err)
			}
			if jsonOut {
				return printJSON(map[string]any{"link": link, "sync": cs})
			}
			fmt.Printf("Logged %s %s %s\n", retrieve.ClaimRef(from), link.Relation, retrieve.ClaimRef(to))
			return nil
		},
	}
	cmd.Flags().StringVar(&note, "note", "", "why, in a sentence")
	cmd.Flags().BoolVar(&force, "force", false, "log the link even if a claim isn't indexed")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the link as JSON")
	return cmd
}
