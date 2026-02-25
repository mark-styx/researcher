package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"

	"github.com/marklubin/researcher/internal/config"
	"github.com/spf13/cobra"
)

func listCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all research projects",
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

			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "PROJECT\tFILES\tPATH")

			type project struct {
				name  string
				files int
				path  string
			}

			var projects []project
			for _, e := range entries {
				if !e.IsDir() || e.Name()[0] == '.' {
					continue
				}
				p := filepath.Join(researchDir, e.Name())
				files, _ := os.ReadDir(p)
				count := 0
				for _, f := range files {
					if !f.IsDir() && filepath.Ext(f.Name()) == ".md" {
						count++
					}
				}
				projects = append(projects, project{e.Name(), count, p})
			}

			sort.Slice(projects, func(i, j int) bool {
				return projects[i].name < projects[j].name
			})

			for _, p := range projects {
				fmt.Fprintf(w, "%s\t%d\t%s\n", p.name, p.files, p.path)
			}
			w.Flush()
			return nil
		},
	}
}
