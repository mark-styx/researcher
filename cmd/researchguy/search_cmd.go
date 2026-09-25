package main

import (
	"fmt"

	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/search"
	"github.com/spf13/cobra"
)

func searchCmd() *cobra.Command {
	var limit int
	var maxAge string
	var projects []string
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Semantic search across research via grepai",
		Long: `Search across all research documents using grepai semantic search.
Requires grepai to be installed and indexed (run 'researchguy init' first).
Returns ranked results matching the query.`,
		Example: `  researchguy search "neural network architectures"
  researchguy search "climate change policy" --limit 10
  researchguy search "Mockingbird" --project research --project the_poisoned_well --json`,
		GroupID: "research",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query := args[0]

			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			if !asJSON {
				if maxAge != "" {
					return fmt.Errorf("--max-age requires --json")
				}
				results, err := search.QueryProjects(cfg, query, limit, projects)
				if err != nil {
					return fmt.Errorf("search failed: %w", err)
				}
				fmt.Print(results)
				return nil
			}

			results, err := search.QueryJSONProjects(cfg, query, limit, projects)
			if err != nil {
				return fmt.Errorf("search failed: %w", err)
			}
			if maxAge != "" {
				d, err := search.ParseMaxAge(maxAge)
				if err != nil {
					return err
				}
				results = search.FilterFresh(results, config.ExpandPath(cfg.ResearchDir), d)
			}
			if results == nil {
				results = []search.SearchResult{}
			}
			return printJSON(results)
		},
	}

	cmd.Flags().IntVar(&limit, "limit", 20, "Max results to return")
	cmd.Flags().StringSliceVar(&projects, "project", nil, "grepai workspace project to search (repeatable; default: grepai.projects)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print results as JSON, each with its project and on-disk path")
	cmd.Flags().StringVar(&maxAge, "max-age", "", "With --json, drop results older than this (e.g. 90d)")
	return cmd
}
