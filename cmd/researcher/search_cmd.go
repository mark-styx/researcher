package main

import (
	"fmt"

	"github.com/marklubin/researcher/internal/config"
	"github.com/marklubin/researcher/internal/search"
	"github.com/spf13/cobra"
)

func searchCmd() *cobra.Command {
	var limit int

	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Semantic search across research via grepai",
		Long: `Search across all research documents using grepai semantic search.
Requires grepai to be installed and indexed (run 'researcher init' first).
Returns ranked results matching the query.`,
		Example: `  researcher search "neural network architectures"
  researcher search "climate change policy" --limit 10`,
		GroupID: "research",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query := args[0]

			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			results, err := search.Query(cfg, query, limit)
			if err != nil {
				return fmt.Errorf("search failed: %w", err)
			}

			fmt.Print(results)
			return nil
		},
	}

	cmd.Flags().IntVar(&limit, "limit", 20, "Max results to return")
	return cmd
}
