package search

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/marklubin/researchguy/internal/config"
)

// MaxWholeFileBytes caps how large a hit file can be before BuildContext
// includes only the matched chunks instead of the whole file. Book research
// files run to hundreds of KB; reports are usually well under this.
const MaxWholeFileBytes = 40_000

const excerptChars = 500

// ContextSource describes one file that contributed to a context result.
type ContextSource struct {
	FilePath  string  `json:"file_path"`
	Project   string  `json:"project,omitempty"`
	Path      string  `json:"path,omitempty"`
	Score     float64 `json:"score"`
	Freshness string  `json:"freshness"`
	Modified  string  `json:"modified"`
	Excerpt   string  `json:"excerpt"`
}

// ContextResult is the retrieval-only answer to "what do we already have on
// this topic". Building it never calls an LLM.
type ContextResult struct {
	Topic   string          `json:"topic"`
	Sources []ContextSource `json:"sources"`
	Context string          `json:"context"`
	Count   int             `json:"count"`
}

// ContextOptions tunes BuildContext. Zero values use defaults: limit 10,
// max age from config (or 90d), and the configured project list.
type ContextOptions struct {
	Limit    int
	MaxAge   string
	Projects []string
}

// BuildContext searches existing research for topic, drops stale hits,
// and formats the rest (whole files when small, matched chunks when large).
func BuildContext(cfg *config.Config, topic string, opts ContextOptions) (ContextResult, error) {
	empty := ContextResult{Topic: topic, Sources: []ContextSource{}}

	maxAgeStr := opts.MaxAge
	if maxAgeStr == "" {
		maxAgeStr = cfg.Ask.MaxAge
	}
	if maxAgeStr == "" {
		maxAgeStr = "90d"
	}
	maxAge, err := ParseMaxAge(maxAgeStr)
	if err != nil {
		return empty, fmt.Errorf("invalid max_age: %w", err)
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}

	results, err := QueryJSONProjects(cfg, topic, limit, opts.Projects)
	if err != nil {
		return empty, err
	}
	researchDir := config.ExpandPath(cfg.ResearchDir)
	fresh := FilterFresh(results, researchDir, maxAge)
	if len(fresh) == 0 {
		return empty, nil
	}

	resolver := NewResolver(cfg)
	type fileHits struct {
		label, path string
		chunks      []SearchResult
	}
	byPath := map[string]*fileHits{}
	var sources []ContextSource
	for _, r := range fresh {
		path := resultPath(r, researchDir)
		if fh, ok := byPath[path]; ok {
			fh.chunks = append(fh.chunks, r)
			continue
		}
		byPath[path] = &fileHits{label: resolver.Label(r.FilePath), path: path, chunks: []SearchResult{r}}

		var freshness, modified string
		if info, err := os.Stat(path); err == nil {
			freshness = FreshnessLabel(info.ModTime())
			modified = info.ModTime().Format("2006-01-02")
		}
		sources = append(sources, ContextSource{
			FilePath:  r.FilePath,
			Project:   r.Project,
			Path:      r.Path,
			Score:     r.Score,
			Freshness: freshness,
			Modified:  modified,
			Excerpt:   truncateRunes(r.Content, excerptChars),
		})
	}

	files := make([]*fileHits, 0, len(byPath))
	for _, fh := range byPath {
		files = append(files, fh)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].label < files[j].label })

	var b strings.Builder
	for _, fh := range files {
		body, ok := readWhole(fh.path)
		if !ok {
			body = joinChunks(fh.chunks)
		}
		if body == "" {
			continue
		}
		fmt.Fprintf(&b, "--- Source: %s ---\n%s\n\n", fh.label, body)
	}

	return ContextResult{Topic: topic, Sources: sources, Context: b.String(), Count: len(sources)}, nil
}

// readWhole returns the file content if it is readable and small enough.
func readWhole(path string) (string, bool) {
	info, err := os.Stat(path)
	if err != nil || info.Size() > MaxWholeFileBytes {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(data), true
}

func joinChunks(chunks []SearchResult) string {
	sort.Slice(chunks, func(i, j int) bool { return chunks[i].StartLine < chunks[j].StartLine })
	var parts []string
	for _, c := range chunks {
		if strings.TrimSpace(c.Content) == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("[lines %d-%d]\n%s", c.StartLine, c.EndLine, c.Content))
	}
	return strings.Join(parts, "\n\n")
}

// truncateRunes cuts s to at most n bytes on a rune boundary, adding "..."
// when it cuts.
func truncateRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}
