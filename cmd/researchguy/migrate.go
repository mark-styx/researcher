package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/llm"
	"github.com/marklubin/researchguy/internal/research"
	"github.com/spf13/cobra"
)

func migrateCmd() *cobra.Command {
	var dryRun bool
	var backend, model string

	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Reorganize flat research directories into categories",
		Long: `Migrate scans the research directory for flat slug directories containing
a single README.md file. It uses the LLM to categorize each topic and moves
the file into the appropriate category directory with a descriptive filename.

Use --dry-run to preview changes without moving any files.`,
		Example: `  researchguy migrate --dry-run
  researchguy migrate
  researchguy migrate --backend ollama --model llama3`,
		GroupID: "project",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			researchDir := config.ExpandPath(cfg.ResearchDir)

			candidates, err := findMigrationCandidates(researchDir)
			if err != nil {
				return fmt.Errorf("scanning research dir: %w", err)
			}

			if len(candidates) == 0 {
				fmt.Println("No directories to migrate.")
				return nil
			}

			fmt.Printf("Found %d directories to migrate.\n\n", len(candidates))

			var provider llm.Provider
			if !dryRun {
				provider, err = llm.NewProvider(cfg, backend, model)
				if err != nil {
					return fmt.Errorf("creating LLM provider: %w", err)
				}
			} else {
				// For dry-run, still need a provider for categorization preview
				provider, err = llm.NewProvider(cfg, backend, model)
				if err != nil {
					return fmt.Errorf("creating LLM provider: %w", err)
				}
			}

			ctx := context.Background()
			cats, _ := research.ExistingCategories(researchDir)

			for _, c := range candidates {
				topic := c.topic
				loc, err := research.Categorize(ctx, provider, topic, cats)
				if err != nil {
					fmt.Fprintf(os.Stderr, "  Warning: categorization failed for %q: %v (skipping)\n", c.dirName, err)
					continue
				}

				targetDir := filepath.Join(researchDir, loc.Category)
				filename := research.UniqueFilename(targetDir, loc.Filename) + ".md"
				targetPath := filepath.Join(targetDir, filename)

				if dryRun {
					fmt.Printf("  [dry-run] %s/README.md -> %s/%s\n", c.dirName, loc.Category, filename)
				} else {
					if err := os.MkdirAll(targetDir, 0755); err != nil {
						fmt.Fprintf(os.Stderr, "  Error creating dir %s: %v\n", targetDir, err)
						continue
					}

					srcPath := filepath.Join(researchDir, c.dirName, "README.md")
					if err := os.Rename(srcPath, targetPath); err != nil {
						fmt.Fprintf(os.Stderr, "  Error moving %s: %v\n", srcPath, err)
						continue
					}

					// Remove the now-empty directory
					os.Remove(filepath.Join(researchDir, c.dirName))

					fmt.Printf("  Migrated: %s/README.md -> %s/%s\n", c.dirName, loc.Category, filename)
				}

				// Add new category to the list for subsequent calls
				if !containsStr(cats, loc.Category) {
					cats = append(cats, loc.Category)
				}
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview changes without moving files")
	cmd.Flags().StringVar(&backend, "backend", "", "LLM backend (claude, ollama, hybrid)")
	cmd.Flags().StringVar(&model, "model", "", "Model override")
	return cmd
}

type migrationCandidate struct {
	dirName string
	topic   string
}

// findMigrationCandidates finds directories containing exactly one file named README.md.
func findMigrationCandidates(researchDir string) ([]migrationCandidate, error) {
	entries, err := os.ReadDir(researchDir)
	if err != nil {
		return nil, err
	}

	var candidates []migrationCandidate
	for _, e := range entries {
		if !e.IsDir() || e.Name()[0] == '.' {
			continue
		}

		dirPath := filepath.Join(researchDir, e.Name())
		files, err := os.ReadDir(dirPath)
		if err != nil {
			continue
		}

		// Check for exactly 1 file named README.md
		if len(files) == 1 && files[0].Name() == "README.md" {
			topic := extractTopic(filepath.Join(dirPath, "README.md"))
			if topic == "" {
				topic = e.Name() // fall back to dir name
			}
			candidates = append(candidates, migrationCandidate{
				dirName: e.Name(),
				topic:   topic,
			})
		}
	}

	return candidates, nil
}

// extractTopic reads the first line of a markdown file and extracts the heading text.
func extractTopic(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "# ") {
			return strings.TrimPrefix(line, "# ")
		}
	}
	return ""
}

func containsStr(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
