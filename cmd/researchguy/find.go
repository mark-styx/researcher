package main

import (
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/store/retrieve"
	"github.com/spf13/cobra"
)

// findFlags are find's flags.
type findFlags struct {
	retrieve.Args
	jsonOut bool
}

func findCmd() *cobra.Command {
	var f findFlags
	cmd := &cobra.Command{
		Use:   "find <query>",
		Short: "Search fetched documents, claims and reports in the store index (no LLM call)",
		Long: `Find searches the passages of every document the fetch stage got and of
every report, and the claims extracted from documents, ranking by full-text
match and by meaning (store.embed.model through Ollama), fused. Each card has
its source, publication date (when the source states one), when it was
collected, and whether it is primary evidence (passage), a claim with the
quote that states it, or researchguy's own synthesis (report). Nothing is
left out for its age unless a filter asks.

A claim card has flags from the claims linked to it: reinforced (two or more
independent origins say it), single_origin, contested, newer_contradiction,
superseded and possibly_outdated (a volatile claim older than
store.volatile_max_age). A link a model labeled says so; it's an inference.
Only claims whose quote was found in the document are returned.

When the embedding model is unavailable the search is full-text only, and
it says so. Cite a card as [P:<id>] or [C:<id>] in a report; ` + "`researchguy store passage`" + `
shows the text around it and ` + "`researchguy store document`" + ` the whole document.

Dates take YYYY, YYYY-MM, YYYY-MM-DD or an age (90d, 2w, 1y). --since and
--until bound the publication date unless --date collected; a publication
bound leaves out undated documents. --as-of keeps what had been collected
by then.`,
		Example: `  researchguy find "nicotine patch trial outcomes"
  researchguy find "court ruling on data brokers" --since 2023 --kind passage
  researchguy find "funding sources" --run 20261002T174145Z-7761e0 --json
  researchguy find "pfas regulation" --prefer-recent 1y --domain epa.gov`,
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
			res, err := r.Find(cmd.Context(), q)
			if err != nil {
				return err
			}
			if f.jsonOut {
				return printJSON(res)
			}
			printFind(res)
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringSliceVar(&f.Kinds, "kind", nil, "passage (fetched documents), claim (claims extracted from them) or report (researchguy's synthesis); default all")
	fl.StringVar(&f.Since, "since", "", "Earliest date (YYYY, YYYY-MM, YYYY-MM-DD or an age such as 2y)")
	fl.StringVar(&f.Until, "until", "", "Latest date, inclusive")
	fl.StringVar(&f.DateField, "date", "", "Date --since/--until bound: published (default) or collected")
	fl.StringVar(&f.AsOf, "as-of", "", "Only what had been collected by this date")
	fl.StringVar(&f.RunID, "run", "", "Only documents this run fetched, and its report")
	fl.StringVar(&f.Domain, "domain", "", "Only this domain and its subdomains")
	fl.StringVar(&f.PreferRecent, "prefer-recent", "", "Halve a card's score per this much age (such as 1y); off by default")
	fl.Float64Var(&f.MinSimilarity, "min-similarity", 0, "Drop meaning matches less similar than this (0-1)")
	fl.IntVar(&f.Limit, "limit", 10, "Cards to return (at most 100)")
	fl.BoolVar(&f.jsonOut, "json", false, "Print JSON: {query, mode, embed_model, note, cards, took_ms}")
	return cmd
}

// printFind prints find's cards for a person.
func printFind(res retrieve.Result) {
	if res.Note != "" {
		fmt.Fprintln(os.Stderr, "Note: "+res.Note)
	}
	if len(res.Cards) == 0 {
		fmt.Println("No matches.")
		return
	}
	for i, c := range res.Cards {
		title := c.Title
		if title == "" {
			title = c.URL
		}
		kind := c.Kind
		if c.ContentKind == "abstract" {
			kind += ", abstract only"
		}
		if c.Kind == retrieve.KindClaim {
			fmt.Printf("%d. [%s] %s\n", i+1, c.Ref, excerpt(c.Text, 300))
			fmt.Printf("   %s (%s, %s)\n   %s\n   %s\n", title, c.Domain, kind, c.Age, c.URL)
			printClaimLines(c, maxRelatedShown)
			fmt.Println()
			continue
		}
		fmt.Printf("%d. [%s] %s (%s, %s)\n", i+1, c.Ref, title, c.Domain, kind)
		fmt.Printf("   %s\n", c.Age)
		if !strings.HasPrefix(c.URL, "researchguy:") {
			fmt.Printf("   %s\n", c.URL)
		} else if len(c.Runs) > 0 {
			fmt.Printf("   report of run %s\n", c.Runs[0])
		}
		fmt.Printf("   %s\n\n", excerpt(c.Text, 300))
	}
	mode := "full-text and meaning"
	if res.Mode == retrieve.ModeFullText {
		mode = "full-text only"
	}
	fmt.Printf("%d card(s), %s, %dms\n", len(res.Cards), mode, res.TookMS)
}

// excerpt is the first n characters of s on one line.
func excerpt(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "..."
}
