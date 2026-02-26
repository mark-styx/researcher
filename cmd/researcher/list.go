package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/marklubin/researcher/internal/config"
	"github.com/spf13/cobra"
)

func listCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all research files grouped by category",
		Long: `List all research files in the research directory, grouped by category.
Shows each file's category, name, and modification date.`,
		Example: `  researcher list`,
		GroupID: "project",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			researchDir := config.ExpandPath(cfg.ResearchDir)
			entries, err := os.ReadDir(researchDir)
			if err != nil {
				return fmt.Errorf("reading research dir: %w", err)
			}

			type fileEntry struct {
				category string
				name     string
				modified string
			}

			var files []fileEntry
			for _, e := range entries {
				if !e.IsDir() || e.Name()[0] == '.' {
					continue
				}

				catDir := filepath.Join(researchDir, e.Name())
				catFiles, err := os.ReadDir(catDir)
				if err != nil {
					continue
				}

				for _, f := range catFiles {
					if f.IsDir() || !strings.HasSuffix(f.Name(), ".md") {
						continue
					}
					info, err := f.Info()
					modified := ""
					if err == nil {
						modified = info.ModTime().Format("2006-01-02")
					}
					name := strings.TrimSuffix(f.Name(), ".md")
					files = append(files, fileEntry{
						category: e.Name(),
						name:     name,
						modified: modified,
					})
				}
			}

			if len(files) == 0 {
				fmt.Println("No research files found.")
				return nil
			}

			// Sort by category then name
			sort.Slice(files, func(i, j int) bool {
				if files[i].category != files[j].category {
					return files[i].category < files[j].category
				}
				return files[i].name < files[j].name
			})

			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "CATEGORY\tFILE\tMODIFIED")

			lastCat := ""
			for _, f := range files {
				cat := f.category
				if cat == lastCat {
					cat = "" // don't repeat category name
				} else {
					lastCat = f.category
				}
				fmt.Fprintf(w, "%s\t%s\t%s\n", cat, f.name, f.modified)
			}
			w.Flush()
			return nil
		},
	}
}
