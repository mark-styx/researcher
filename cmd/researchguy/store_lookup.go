package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/embed"
	"github.com/marklubin/researchguy/internal/fetch"
	"github.com/marklubin/researchguy/internal/store/retrieve"
	"github.com/spf13/cobra"
)

// openRetriever loads the config and opens the index for a lookup.
func openRetriever(ctx context.Context) (*config.Config, *retrieve.Retriever, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, fmt.Errorf("loading config: %w", err)
	}
	if err := requireDSN(cfg); err != nil {
		return nil, nil, err
	}
	r, err := retrieve.Open(ctx, cfg)
	return cfg, r, err
}

// parseID reads an id, with or without its ref prefix (P: or S:).
func parseID(s, prefix string) (int64, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), prefix+":")
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid id %q", s)
	}
	return id, nil
}

func storePassageCmd() *cobra.Command {
	var neighbors int
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "passage <id>",
		Short: "Show an indexed passage, its source and the passages around it",
		Example: `  researchguy store passage P:4821193310755112
  researchguy store passage 4821193310755112 --neighbors 2 --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			id, err := parseID(args[0], retrieve.RefPassage)
			if err != nil {
				return err
			}
			_, r, err := openRetriever(cmd.Context())
			if err != nil {
				return err
			}
			defer r.Close()
			p, err := r.Passage(cmd.Context(), id, neighbors)
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(p)
			}
			fmt.Printf("[%s] %s (%s, %s)\n%s\n%s\ndocument %d, characters %d-%d\n\n", p.Ref, p.Title, p.Domain, p.Kind, p.Age, p.URL,
				p.DocumentID, p.CharStart, p.CharEnd)
			for _, n := range p.Before {
				fmt.Printf("%s\n\n", n.Text)
			}
			fmt.Printf(">>> %s\n\n", p.Text)
			for _, n := range p.After {
				fmt.Printf("%s\n\n", n.Text)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&neighbors, "neighbors", 1, "Passages to show on each side (at most 5)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Print JSON")
	return cmd
}

func storeDocumentCmd() *cobra.Command {
	var offset, chars int
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "document <id>",
		Short: "Show an indexed document's text, versions and fetch attempts",
		Long: `Document prints a slice of a fetched document's text (from the store, the
record of what was fetched), its publication and collection dates, the
source's other versions, and the fetch attempts that got it.`,
		Example: `  researchguy store document 7075171269261622582
  researchguy store document 7075171269261622582 --offset 8000 --chars 8000 --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			id, err := parseID(args[0], "D")
			if err != nil {
				return err
			}
			_, r, err := openRetriever(cmd.Context())
			if err != nil {
				return err
			}
			defer r.Close()
			d, err := r.Document(cmd.Context(), id, offset, chars)
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(d)
			}
			pub := "undated"
			if d.Published != nil {
				pub = "published " + d.Published.Date + " (" + d.Published.From + ")"
			}
			fmt.Printf("%s\n%s [%s] %s, %s, %d chars\n%s; collected %s\n", d.Title, d.URL, d.SourceRef, d.Origin, d.ContentKind, d.TextChars,
				pub, d.Collected.Format("2006-01-02"))
			for _, v := range d.Versions {
				fmt.Printf("other version: document %d, %d chars, collected %s\n", v.DocumentID, v.TextChars, v.Collected.Format("2006-01-02"))
			}
			for _, f := range d.Fetches {
				fmt.Printf("fetched by run %s (%s, via %s) %s\n", f.RunID, f.Reason, f.Via, f.AttemptedAt.Format("2006-01-02 15:04"))
			}
			fmt.Printf("\n%s\n", d.Text)
			if d.Truncated {
				fmt.Fprintf(os.Stderr, "More text follows; continue with --offset %d.\n", d.Offset+len([]rune(d.Text)))
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&offset, "offset", 0, "Character to start at")
	cmd.Flags().IntVar(&chars, "chars", 8000, fmt.Sprintf("Characters to show (at most %d)", retrieve.MaxDocumentChars))
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Print JSON")
	return cmd
}

func storeSourceCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "source <url|id>",
		Short: "Show a source's documents, sightings, fetch attempts and citing runs",
		Long: `Source looks a source up by URL (any variant that normalizes to it, such
as with tracking parameters or www.), by id or by S:<id>, and lists the
documents fetched from it, the captures that saw it, every fetch attempt,
and the runs that cited it.`,
		Example: `  researchguy store source https://doi.org/10.1145/3290605.3300830
  researchguy store source S:6863701433181393939 --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			_, r, err := openRetriever(cmd.Context())
			if err != nil {
				return err
			}
			defer r.Close()
			s, err := r.Source(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(s)
			}
			fmt.Printf("[%s] %s\n%s (%s, %s)\nfirst seen %s, last seen %s\n", s.Ref, s.Title, s.URL, s.Domain, s.Kind,
				s.FirstSeen.Format("2006-01-02"), s.LastSeen.Format("2006-01-02"))
			fmt.Printf("%d document(s), %d sighting(s), %d fetch attempt(s)\n", len(s.Documents), len(s.Sightings), len(s.Fetches))
			for _, d := range s.Documents {
				fmt.Printf("  document %d: %s, %d chars, collected %s\n", d.DocumentID, d.ContentKind, d.TextChars, d.Collected.Format("2006-01-02"))
			}
			for _, f := range s.Fetches {
				status := f.Error
				if status == "" && f.HTTPStatus != nil {
					status = "HTTP " + strconv.Itoa(*f.HTTPStatus)
				}
				fmt.Printf("  fetch %s/%d %s via %s: %s\n", f.RunID, f.Seq, f.Reason, f.Via, status)
			}
			if len(s.CitedBy) > 0 {
				fmt.Printf("cited by: %s\n", strings.Join(s.CitedBy, ", "))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Print JSON")
	return cmd
}

func storeIngestURLCmd() *cobra.Command {
	var runID string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "ingest-url <url>",
		Short: "Fetch a URL into the store and the index now",
		Long: `Ingest-url fetches one URL the way the fetch stage does (a DOI that's
blocked is tried through OpenAlex), records the attempt in a run's
fetches.jsonl with reason ingest, indexes it, and embeds its passages.
Without --run it makes a run of kind manual for it. A page that can't be
fetched is recorded, and reported, not retried past a block.`,
		Example: `  researchguy store ingest-url https://www.nature.com/articles/d41586-024-00001-1
  researchguy store ingest-url https://doi.org/10.1037/a0039650 --run 20261002T174145Z-7761e0 --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			cfg, r, err := openRetriever(cmd.Context())
			if err != nil {
				return err
			}
			defer r.Close()
			if runID != "" {
				if err := checkRunID(r.Store, runID); err != nil {
					return err
				}
			}
			in := &retrieve.Ingester{Index: r.Index, Store: r.Store, Stage: fetch.NewStage(cfg.Store.Fetch, r.Store),
				Embed: embed.New(cfg), EmbedBudget: cfg.Store.Embed.BudgetDuration()}
			res, err := in.IngestURL(cmd.Context(), args[0], runID)
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(res)
			}
			if res.Note != "" {
				fmt.Fprintln(os.Stderr, "Note: "+res.Note)
			}
			fmt.Printf("Run %s: %d document(s)\n", res.RunID, len(res.Documents))
			for _, d := range res.Documents {
				fmt.Printf("  document %d [%s] via %s: %s, %d chars, %d passage(s)\n", d.DocumentID, d.SourceRef, d.Via, d.ContentKind, d.TextChars, len(d.Passages))
			}
			for _, e := range res.Errors {
				fmt.Printf("  failed: %s\n", e)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&runID, "run", "", "Record the fetch in this run instead of a new manual one")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Print JSON")
	return cmd
}
