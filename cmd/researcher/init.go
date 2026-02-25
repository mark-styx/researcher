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
			if cfg.Grepai.AutoIndex {
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
