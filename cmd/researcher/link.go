package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/marklubin/researcher/internal/config"
	"github.com/spf13/cobra"
)

func linkCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "link <path>",
		Short: "Symlink research dir to another location",
		Long: `Create a symlink from the given path to the research directory.
Useful for making research accessible from other locations like
your desktop or another project.`,
		Example: `  researcher link ~/Desktop/research
  researcher link /tmp/research-shortcut`,
		GroupID: "project",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := args[0]

			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			researchDir := config.ExpandPath(cfg.ResearchDir)

			// Resolve target to absolute path
			if !filepath.IsAbs(target) {
				wd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("getting working dir: %w", err)
				}
				target = filepath.Join(wd, target)
			}

			// Ensure target doesn't already exist
			if _, err := os.Stat(target); err == nil {
				return fmt.Errorf("target %q already exists", target)
			}

			// Create parent dirs
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return fmt.Errorf("creating parent dirs: %w", err)
			}

			if err := os.Symlink(researchDir, target); err != nil {
				return fmt.Errorf("creating symlink: %w", err)
			}

			fmt.Printf("Linked: %s → %s\n", target, researchDir)
			return nil
		},
	}
}
