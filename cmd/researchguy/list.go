package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/research"
	"github.com/spf13/cobra"
)

func listCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all research files grouped by category",
		Long: `List all research files in the research directory, grouped by category.
Shows each file's category, name, and modification date.`,
		Example: `  researchguy list`,
		GroupID: "project",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			researchDir := config.ExpandPath(cfg.ResearchDir)
			files, err := research.ListResearchFiles(researchDir)
			if err != nil {
				return fmt.Errorf("reading research dir: %w", err)
			}

			if len(files) == 0 {
				fmt.Println("No research files found.")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "CATEGORY\tFILE\tMODIFIED")

			lastCat := ""
			for _, f := range files {
				cat := f.Category
				if cat == lastCat {
					cat = "" // don't repeat category name
				} else {
					lastCat = f.Category
				}
				fmt.Fprintf(w, "%s\t%s\t%s\n", cat, f.Name, f.Modified.Format("2006-01-02"))
			}
			w.Flush()
			return nil
		},
	}
}
