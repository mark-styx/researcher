package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/store"
	"github.com/marklubin/researchguy/internal/store/retrieve"
	"github.com/spf13/cobra"
)

// maxRelatedShown is how many of a claim's related claims find prints.
const maxRelatedShown = 3

// printClaimLines prints a claim card's flags, quote and related claims,
// indented under its heading line.
func printClaimLines(c retrieve.Card, related int) {
	if len(c.Flags) > 0 {
		fmt.Printf("   %s\n", strings.Join(c.Flags, ", "))
	}
	if c.QuoteVerified {
		fmt.Printf("   quote: %q\n", excerpt(c.Quote, 300))
	} else {
		fmt.Println("   quote not found in the document")
	}
	cl := c.Cluster
	if cl == nil {
		return
	}
	if cl.Origins > 1 {
		fmt.Printf("   said by %d independent origins (%s), last confirmed %s\n", cl.Origins, cl.SupportDates, cl.LastConfirmed.Format(time.DateOnly))
	}
	for i, rel := range cl.Related {
		if related >= 0 && i == related {
			fmt.Printf("   ...and %d more (researchguy store claim %s)\n", len(cl.Related)-i, c.Ref)
			break
		}
		fmt.Printf("   %s\n", relatedLine(rel))
	}
}

// relatedLine is one related claim on a line, with how the link was made.
func relatedLine(rel retrieve.Related) string {
	how := ""
	switch rel.Method {
	case store.LinkModel:
		how = "model-labeled"
		if rel.Model != "" {
			how += " by " + rel.Model
		}
		if rel.Confidence > 0 {
			how += fmt.Sprintf(", %.2f", rel.Confidence)
		}
	case store.LinkRule:
		how = "same quoted words"
	case store.LinkHuman:
		how = "set by hand"
	}
	s := fmt.Sprintf("%s [%s] (%s, %s; %s): %s", strings.ReplaceAll(rel.Relation, "_", " "), rel.Ref, rel.Domain, rel.Dated, how, excerpt(rel.Text, 160))
	if rel.Note != "" {
		s += " (" + excerpt(rel.Note, 120) + ")"
	}
	return s
}

func storeClaimCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "claim <id>",
		Short: "Show an indexed claim, its quote in context and the claims linked to it",
		Long: `Claim shows a claim the extractor found: its text, the quote that states it
(checked against the document, or marked not found), the passage it's in,
its flags and every claim linked to it, labeled with how the link was made.
A model's label is an inference; ` + "`researchguy store link set`" + ` overrules it.`,
		Example: `  researchguy store claim C:1959638450032253773
  researchguy store claim 1959638450032253773 --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			id, err := parseID(args[0], retrieve.RefClaim)
			if err != nil {
				return err
			}
			_, r, err := openRetriever(cmd.Context())
			if err != nil {
				return err
			}
			defer r.Close()
			c, err := r.Claim(cmd.Context(), id)
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(c)
			}
			title := c.Title
			if title == "" {
				title = c.URL
			}
			fmt.Printf("[%s] %s\n%s (%s)\n%s\n%s\n", c.Ref, c.Text, title, c.Domain, c.Age, c.URL)
			printClaimLines(c.Card, -1)
			if !c.QuoteVerified && c.ModelQuote != "" {
				fmt.Printf("   the model quoted: %q\n", excerpt(c.ModelQuote, 300))
			}
			fmt.Printf("extracted by %s on %s\n", c.Extractor, c.ExtractedAt.Format(time.DateOnly))
			if p := c.Passage; p != nil {
				fmt.Printf("\n[%s] characters %d-%d of document %d:\n%s\n", p.Ref, p.CharStart, p.CharEnd, p.DocumentID, p.Text)
			}
			if len(c.Versions) > 0 {
				fmt.Printf("\n%d other version(s) of the source; researchguy store source %s lists them\n", len(c.Versions), retrieve.SourceRef(c.SourceID))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Print JSON")
	return cmd
}

func timelineCmd() *cobra.Command {
	var f findFlags
	cmd := &cobra.Command{
		Use:   "timeline <query | C:id>",
		Short: "Lay out what sources said on a question in date order (no LLM call)",
		Long: `Timeline finds the claims matching the query, as find --kind claim would,
adds the claims linked to them, and lists them oldest first by the date
each is true as of: its own date when the source gives one, else the
publication date, else when it was collected. A claim that supersedes or
contradicts an earlier one is marked, which is where the evidence changed.

Given a claim ref (C:123) instead of a query, it starts from that claim.
Links a model labeled say so; they're inferences, not findings.`,
		Example: `  researchguy timeline "bridge toll price"
  researchguy timeline C:1959638450032253773 --json
  researchguy timeline "unemployment rate" --since 2020 --domain bls.gov`,
		GroupID: "research",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}
			if err := requireDSN(cfg); err != nil {
				return err
			}
			q, err := f.Query(args[0], time.Now())
			if err != nil {
				return err
			}
			r, err := retrieve.Open(cmd.Context(), cfg)
			if err != nil {
				return err
			}
			defer r.Close()
			res, err := r.Timeline(cmd.Context(), q)
			if err != nil {
				return err
			}
			if f.jsonOut {
				return printJSON(res)
			}
			printTimeline(res)
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.Since, "since", "", "Earliest date (YYYY, YYYY-MM, YYYY-MM-DD or an age such as 2y)")
	fl.StringVar(&f.Until, "until", "", "Latest date, inclusive")
	fl.StringVar(&f.DateField, "date", "", "Date --since/--until bound: published (default) or collected")
	fl.StringVar(&f.AsOf, "as-of", "", "Only what had been collected by this date")
	fl.StringVar(&f.RunID, "run", "", "Only claims from documents this run fetched")
	fl.StringVar(&f.Domain, "domain", "", "Only this domain and its subdomains")
	fl.Float64Var(&f.MinSimilarity, "min-similarity", 0, "Drop meaning matches less similar than this (0-1)")
	fl.IntVar(&f.Limit, "limit", 20, "Claims the query finds, before linked ones are added (at most 100)")
	fl.BoolVar(&f.jsonOut, "json", false, "Print JSON: {query, mode, note, entries, took_ms}")
	return cmd
}

// printTimeline prints a timeline for a person.
func printTimeline(res retrieve.TimelineResult) {
	if res.Note != "" {
		fmt.Fprintln(os.Stderr, "Note: "+res.Note)
	}
	if len(res.Entries) == 0 {
		fmt.Println("No claims.")
		return
	}
	for _, e := range res.Entries {
		mark := " "
		if !e.Matched {
			mark = "+"
		}
		fmt.Printf("%s %s [%s] %s\n", e.Dated, mark, e.Ref, excerpt(e.Text, 200))
		fmt.Printf("             %s, %s\n", e.Domain, strings.Join(e.Flags, ", "))
		for _, ch := range e.Changes {
			fmt.Printf("             %s\n", ch)
		}
	}
	fmt.Printf("%d claim(s), oldest first; + marks one reached through a link, %dms\n", len(res.Entries), res.TookMS)
}
