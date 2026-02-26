package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/marklubin/researcher/internal/config"
	"github.com/spf13/cobra"
)

func showCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <category>[/<file>]",
		Short: "Show category contents or file details",
		Long: `Show details for a research category or a specific file.

With just a category name, lists all files in that category.
With category/file, shows details for the specific file.`,
		Example: `  researcher show llm
  researcher show llm/agentic-code`,
		GroupID: "project",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			arg := args[0]

			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			researchDir := config.ExpandPath(cfg.ResearchDir)

			// Check if argument contains a slash -> category/file
			if strings.Contains(arg, "/") {
				return showFile(researchDir, arg)
			}
			return showCategory(researchDir, arg)
		},
	}
}

func showCategory(researchDir, category string) error {
	catDir := filepath.Join(researchDir, category)

	info, err := os.Stat(catDir)
	if err != nil {
		return fmt.Errorf("category %q not found: %w", category, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%q is not a directory", category)
	}

	fmt.Printf("Category: %s\n", category)
	fmt.Printf("Path:     %s\n\n", catDir)

	entries, err := os.ReadDir(catDir)
	if err != nil {
		return fmt.Errorf("reading category dir: %w", err)
	}

	fmt.Println("Files:")
	for _, e := range entries {
		if e.Name()[0] == '.' {
			continue
		}
		info, _ := e.Info()
		if info != nil {
			fmt.Printf("  %-40s %8d bytes  %s\n", e.Name(), info.Size(),
				info.ModTime().Format("2006-01-02"))
		}
	}
	return nil
}

func showFile(researchDir, path string) error {
	parts := strings.SplitN(path, "/", 2)
	category := parts[0]
	file := parts[1]

	// Add .md extension if not present
	if !strings.HasSuffix(file, ".md") {
		file += ".md"
	}

	filePath := filepath.Join(researchDir, category, file)
	info, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("file %q not found in category %q: %w", file, category, err)
	}

	fmt.Printf("Category: %s\n", category)
	fmt.Printf("File:     %s\n", file)
	fmt.Printf("Path:     %s\n", filePath)
	fmt.Printf("Size:     %d bytes\n", info.Size())
	fmt.Printf("Modified: %s\n", info.ModTime().Format("2006-01-02 15:04"))
	return nil
}
