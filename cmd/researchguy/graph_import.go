package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/marklubin/researchguy/internal/graph"
	"github.com/spf13/cobra"
)

func graphImportSourcesCmd() *cobra.Command {
	var book, file, title, path string
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "import-sources",
		Short: "Import a bookworm sources.json as a book report node linked to source nodes",
		Long: `Record a book's bibliography in the graph. The book becomes one report
node; each distinct source URL (normalized: http/https, www., trailing slash,
fragments, and tracking parameters don't count as differences) becomes one
source node shared across every book that cites it, with a references edge
from the book. Re-running is safe: existing nodes and edges are reused, and
per-book notes are appended, never overwritten. Entries without an http(s)
URL are counted and skipped.`,
		Example: `  researchguy graph import-sources --book the_poisoned_well \
    --file ~/sentinel/books/the_poisoned_well/research/sources.json \
    --title "The Poisoned Well" --path ~/sentinel/books/the_poisoned_well/research`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if book == "" || file == "" {
				return fmt.Errorf("--book and --file are required")
			}
			data, err := os.ReadFile(file)
			if err != nil {
				return fmt.Errorf("reading %s: %w", file, err)
			}
			var entries []graph.SourceEntry
			if err := json.Unmarshal(data, &entries); err != nil {
				return fmt.Errorf("parsing %s: %w", file, err)
			}
			if path != "" {
				if abs, err := filepath.Abs(path); err == nil {
					path = abs
				}
			}

			_, store, err := openGraphStore()
			if err != nil {
				return err
			}
			defer store.Close()

			stats, err := store.ImportSources(graph.BookRef{Slug: book, Title: title, ResearchPath: path}, entries)
			if err != nil {
				return fmt.Errorf("import failed: %w", err)
			}
			if asJSON {
				return printJSON(stats)
			}
			fmt.Printf("Imported %s: %d entries, %d unique sources (%d new, %d existing, %d updated), %d edges added (%d already present), %d without URL, %d invalid URL.\n",
				book, stats.Entries, stats.UniqueSources, stats.SourcesCreated, stats.SourcesReused, stats.SourcesUpdated,
				stats.EdgesCreated, stats.EdgesExisting, stats.SkippedNoURL, stats.InvalidURL)
			fmt.Printf("Book report node: %s\n", stats.ReportNodeID)
			return nil
		},
	}
	cmd.Flags().StringVar(&book, "book", "", "Book slug, e.g. the_poisoned_well (required)")
	cmd.Flags().StringVar(&file, "file", "", "Path to the book's research/sources.json (required)")
	cmd.Flags().StringVar(&title, "title", "", "Book title for the report node (default: slug)")
	cmd.Flags().StringVar(&path, "path", "", "Book research directory, stored in the report node's metadata")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print import stats as JSON")
	return cmd
}
