package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/marklubin/researcher/internal/config"
	"github.com/spf13/cobra"
)

func showCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <project>",
		Short: "Show project details and files",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			project := args[0]

			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			researchDir := config.ExpandPath(cfg.ResearchDir)
			projectDir := filepath.Join(researchDir, project)

			info, err := os.Stat(projectDir)
			if err != nil {
				return fmt.Errorf("project %q not found: %w", project, err)
			}
			if !info.IsDir() {
				return fmt.Errorf("%q is not a directory", project)
			}

			fmt.Printf("Project: %s\n", project)
			fmt.Printf("Path:    %s\n\n", projectDir)

			entries, err := os.ReadDir(projectDir)
			if err != nil {
				return fmt.Errorf("reading project dir: %w", err)
			}

			fmt.Println("Files:")
			for _, e := range entries {
				if e.Name()[0] == '.' {
					continue
				}
				info, _ := e.Info()
				if info != nil {
					fmt.Printf("  %-40s %8d bytes\n", e.Name(), info.Size())
				}
			}
			return nil
		},
	}
}
