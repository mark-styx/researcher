package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/marklubin/researcher/internal/config"
	"github.com/spf13/cobra"
)

func initCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Initialize researcher config, directories, and grepai",
		Long: `Initialize the researcher environment. Creates the config directory
(~/.researcher), writes a default config.yaml if none exists, creates
the research directory, and optionally sets up grepai indexing.`,
		Example: `  researcher init`,
		GroupID: "setup",
		RunE: func(cmd *cobra.Command, args []string) error {
			configDir := config.Dir()
			if err := os.MkdirAll(configDir, 0755); err != nil {
				return fmt.Errorf("creating config dir: %w", err)
			}

			configPath := config.FilePath()
			if _, err := os.Stat(configPath); os.IsNotExist(err) {
				if err := os.WriteFile(configPath, []byte(config.DefaultYAML), 0644); err != nil {
					return fmt.Errorf("writing default config: %w", err)
				}
				fmt.Println("Created config:", configPath)
			} else {
				fmt.Println("Config already exists:", configPath)
			}

			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			researchDir := config.ExpandPath(cfg.ResearchDir)
			if err := os.MkdirAll(researchDir, 0755); err != nil {
				return fmt.Errorf("creating research dir: %w", err)
			}
			fmt.Println("Research dir:", researchDir)

			// Set up grepai if auto_index is enabled
			if cfg.Grepai.Workspace != "" {
				// research_dir is registered as a project inside a grepai workspace.
				// The workspace's own daemon indexes it; bootstrapping a standalone
				// GOB index or a second background watcher here would create a
				// duplicate index that search never queries (search always passes
				// --workspace once configured).
				fmt.Printf("Grepai workspace mode: %s/%s. Skipping standalone init and watcher.\n", cfg.Grepai.Workspace, cfg.Grepai.Project)
			} else if cfg.Grepai.AutoIndex {
				grepaiDir := filepath.Join(researchDir, ".grepai")
				if _, err := os.Stat(grepaiDir); os.IsNotExist(err) {
					fmt.Println("Initializing grepai index...")
					c := exec.Command(cfg.Grepai.Binary, "init", "--provider", "ollama", "--backend", "gob", "--yes")
					c.Dir = researchDir
					c.Stdout = os.Stdout
					c.Stderr = os.Stderr
					if err := c.Run(); err != nil {
						fmt.Fprintf(os.Stderr, "Warning: grepai init failed: %v\n", err)
					}
				}

				// Start watcher in background
				fmt.Println("Starting grepai watcher...")
				c := exec.Command(cfg.Grepai.Binary, "watch", "--background")
				c.Dir = researchDir
				c.Stdout = os.Stdout
				c.Stderr = os.Stderr
				if err := c.Run(); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: grepai watch --background failed: %v\n", err)
				}
			}

			fmt.Println("\nResearcher initialized successfully.")
			return nil
		},
	}
}
