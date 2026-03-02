package search

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/marklubin/researcher/internal/config"
)

// SearchResult represents a single result from grepai --json output.
type SearchResult struct {
	FilePath  string  `json:"file_path"`
	StartLine int     `json:"start_line"`
	EndLine   int     `json:"end_line"`
	Score     float64 `json:"score"`
	Content   string  `json:"content"`
}

// Query runs a grepai search against the research directory.
func Query(cfg *config.Config, query string, limit int) (string, error) {
	researchDir := config.ExpandPath(cfg.ResearchDir)

	args := []string{"search", query, "--limit", strconv.Itoa(limit)}
	cmd := exec.Command(cfg.Grepai.Binary, args...)
	cmd.Dir = researchDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("grepai search failed: %w\nstderr: %s", err, stderr.String())
	}

	return stdout.String(), nil
}

// QueryJSON runs a grepai search with --json output and returns structured results.
func QueryJSON(cfg *config.Config, query string, limit int) ([]SearchResult, error) {
	researchDir := config.ExpandPath(cfg.ResearchDir)

	args := []string{"search", query, "--limit", strconv.Itoa(limit), "--json"}
	cmd := exec.Command(cfg.Grepai.Binary, args...)
	cmd.Dir = researchDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("grepai search failed: %w\nstderr: %s", err, stderr.String())
	}

	var results []SearchResult
	if err := json.Unmarshal(stdout.Bytes(), &results); err != nil {
		return nil, fmt.Errorf("parsing grepai JSON: %w", err)
	}

	return results, nil
}

// ParseMaxAge parses a duration string like "90d", "2w", "24h" into time.Duration.
// Supported suffixes: d (days), w (weeks), h (hours).
// Returns an error for empty or unparseable strings.
func ParseMaxAge(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty max-age string")
	}

	suffix := s[len(s)-1]
	numStr := s[:len(s)-1]

	n, err := strconv.Atoi(numStr)
	if err != nil {
		return 0, fmt.Errorf("invalid max-age %q: %w", s, err)
	}
	if n <= 0 {
		return 0, fmt.Errorf("max-age must be positive: %q", s)
	}

	switch suffix {
	case 'd':
		return time.Duration(n) * 24 * time.Hour, nil
	case 'w':
		return time.Duration(n) * 7 * 24 * time.Hour, nil
	case 'h':
		return time.Duration(n) * time.Hour, nil
	default:
		return 0, fmt.Errorf("unknown max-age suffix %q in %q (use d, w, or h)", string(suffix), s)
	}
}

// FilterFresh returns only results whose files have been modified within maxAge.
// researchDir is the base directory; file paths in results are relative to it.
func FilterFresh(results []SearchResult, researchDir string, maxAge time.Duration) []SearchResult {
	cutoff := time.Now().Add(-maxAge)
	var fresh []SearchResult
	for _, r := range results {
		path := r.FilePath
		if !filepath.IsAbs(path) {
			path = filepath.Join(researchDir, path)
		}
		info, err := os.Stat(path)
		if err != nil {
			continue // skip files we can't stat
		}
		if info.ModTime().After(cutoff) {
			fresh = append(fresh, r)
		}
	}
	return fresh
}

// ReadContents reads the full file content for each unique file path in results.
// Returns a map from file path to content string, deduped by path.
// researchDir is the base directory for resolving relative paths.
func ReadContents(results []SearchResult, researchDir string) map[string]string {
	contents := make(map[string]string)
	for _, r := range results {
		path := r.FilePath
		if !filepath.IsAbs(path) {
			path = filepath.Join(researchDir, path)
		}
		if _, ok := contents[path]; ok {
			continue // already read
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue // skip unreadable files
		}
		contents[path] = string(data)
	}
	return contents
}

// FreshnessLabel returns a human-readable freshness label based on how recently
// a file was modified: "current" (<7d), "recent" (<30d), "aging" (<60d), "stale" (>60d).
func FreshnessLabel(modTime time.Time) string {
	age := time.Since(modTime)
	switch {
	case age < 7*24*time.Hour:
		return "current"
	case age < 30*24*time.Hour:
		return "recent"
	case age < 60*24*time.Hour:
		return "aging"
	default:
		return "stale"
	}
}

// FormatContext formats research contents into a context string for the LLM prompt.
// Paths are sorted for deterministic output.
func FormatContext(contents map[string]string) string {
	if len(contents) == 0 {
		return ""
	}

	paths := make([]string, 0, len(contents))
	for p := range contents {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	var b strings.Builder
	for _, p := range paths {
		b.WriteString(fmt.Sprintf("--- Source: %s ---\n", filepath.Base(p)))
		b.WriteString(contents[p])
		b.WriteString("\n\n")
	}
	return b.String()
}
