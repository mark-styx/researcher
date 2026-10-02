package main

import (
	"fmt"
	"os"

	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/search"
	"github.com/spf13/cobra"
)

func contextCmd() *cobra.Command {
	var maxAge string
	var limit int
	var projects []string
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "context <topic>",
		Short: "Show existing research on a topic (no LLM call)",
		Long: `Search existing research via grepai, drop stale hits, and print the
formatted context an ask/dive would receive. Never calls an LLM.

With store.dsn set it also includes the store index's evidence (what
` + "`researchguy find`" + ` returns): passages of fetched documents and of reports, each
dated and labeled primary or synthesis. Store evidence isn't dropped for
age unless --max-age is given, which limits it by collection date.

Large files (book research) contribute only their matched chunks. Use
--project to search specific grepai workspace projects and --max-age none to
include older research.`,
		Example: `  researchguy context "Operation Mockingbird"
  researchguy context "Operation Mockingbird" --project research --project the_poisoned_well --max-age none --json`,
		GroupID: "research",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}
			result, err := search.BuildContext(cmd.Context(), cfg, args[0], search.ContextOptions{
				Limit:    limit,
				MaxAge:   maxAge,
				Projects: projects,
			})
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(result)
			}
			for _, n := range result.Notes {
				fmt.Fprintln(os.Stderr, "Note: "+n)
			}
			if result.Count == 0 {
				fmt.Println("No existing research found.")
				return nil
			}
			for _, s := range result.Sources {
				fmt.Printf("%.2f  %-8s %s  %s\n", s.Score, s.Freshness, s.Modified, s.FilePath)
			}
			for _, c := range result.Evidence {
				fmt.Printf("[%s] %s  %s\n", c.Ref, c.Age, c.URL)
			}
			fmt.Println()
			fmt.Print(result.Context)
			return nil
		},
	}

	cmd.Flags().StringVar(&maxAge, "max-age", "", "Max age for freshness filter (e.g. 90d, 2w, 24h, none). Default: ask.max_age from config")
	cmd.Flags().IntVar(&limit, "limit", 10, "Max search results to include")
	cmd.Flags().StringSliceVar(&projects, "project", nil, "grepai workspace project to search (repeatable; default: grepai.projects)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print JSON: {topic, sources, evidence, notes, context, count}")
	return cmd
}
