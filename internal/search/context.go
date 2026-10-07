package search

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/store"
	"github.com/marklubin/researchguy/internal/store/retrieve"
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
	// Evidence are passages of fetched documents and of reports from the
	// store index (store.dsn), each dated and labeled primary or synthesis.
	Evidence []retrieve.Card `json:"evidence,omitempty"`
	// Notes say what limited the result, such as the index being down.
	Notes   []string `json:"notes,omitempty"`
	Context string   `json:"context"`
	Count   int      `json:"count"`
}

// ContextOptions tunes BuildContext. Zero values use defaults: limit 10,
// max age from config (or 90d), and the configured project list.
type ContextOptions struct {
	Limit int
	// MaxAge drops report files older than it (default ask.max_age). Store
	// evidence is never dropped for age by default; a MaxAge passed here
	// also limits it, by collection date.
	MaxAge   string
	Projects []string
	// Retriever searches the store index; nil opens one for the call when
	// store.dsn is set.
	Retriever *retrieve.Retriever
}

// BuildContext searches existing research for topic: report files through
// grepai, stale ones dropped, formatted whole when small and as matched
// chunks when large; and, with store.dsn set, the store index's evidence,
// dated and labeled. A store failure is a note, not an error.
func BuildContext(ctx context.Context, cfg *config.Config, topic string, opts ContextOptions) (ContextResult, error) {
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

	// Only an explicit max age limits store evidence.
	var storeAge time.Duration
	if strings.TrimSpace(opts.MaxAge) != "" {
		storeAge = maxAge
	}
	empty.Evidence, empty.Notes = storeEvidence(ctx, cfg, topic, limit, storeAge, opts.Retriever)

	results, err := QueryJSONProjects(cfg, topic, limit, opts.Projects)
	if err != nil {
		if len(empty.Evidence) == 0 {
			return empty, err
		}
		empty.Notes = append(empty.Notes, fmt.Sprintf("report search failed: %v", err))
		results = nil
	}
	researchDir := config.ExpandPath(cfg.ResearchDir)
	fresh := FilterFresh(results, researchDir, maxAge)
	if len(fresh) == 0 {
		return finishContext(empty, nil, nil), nil
	}

	// Highest score first, so the best copy of repeated text wins.
	sort.SliceStable(fresh, func(i, j int) bool { return fresh[i].Score > fresh[j].Score })

	resolver := NewResolver(cfg)
	type fileHits struct {
		label, path string
		source      ContextSource
		chunks      []SearchResult
		whole       string
	}
	var order []*fileHits
	byPath := map[string]*fileHits{}
	seenBodies := map[string]bool{}
	for _, r := range fresh {
		body := chunkBody(r.Content)
		if body != "" && seenBodies[body] {
			continue
		}
		seenBodies[body] = true

		path := resultPath(r, researchDir)
		if fh, ok := byPath[path]; ok {
			fh.chunks = append(fh.chunks, r)
			continue
		}
		var freshness, modified string
		if info, err := os.Stat(path); err == nil {
			freshness = FreshnessLabel(info.ModTime())
			modified = info.ModTime().Format("2006-01-02")
		}
		fh := &fileHits{
			label:  resolver.Label(r.FilePath),
			path:   path,
			chunks: []SearchResult{r},
			source: ContextSource{
				FilePath:  r.FilePath,
				Project:   r.Project,
				Path:      r.Path,
				Score:     r.Score,
				Freshness: freshness,
				Modified:  modified,
				Excerpt:   truncateRunes(r.Content, excerptChars),
			},
		}
		fh.whole, _ = readWhole(path)
		byPath[path] = fh
		order = append(order, fh)
	}

	// Chunks already contained in a file included whole add nothing.
	var wholes []string
	for _, fh := range order {
		if fh.whole != "" {
			wholes = append(wholes, fh.whole)
		}
	}
	type part struct{ label, body string }
	var parts []part
	var sources []ContextSource
	for _, fh := range order {
		body := fh.whole
		if body == "" {
			var kept []SearchResult
			for _, c := range fh.chunks {
				if !containedIn(chunkBody(c.Content), wholes) {
					kept = append(kept, c)
				}
			}
			body = capChunks(joinChunks(kept))
		}
		if strings.TrimSpace(body) == "" {
			continue
		}
		parts = append(parts, part{fh.label, body})
		sources = append(sources, fh.source)
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].label < parts[j].label })

	var b strings.Builder
	for _, p := range parts {
		fmt.Fprintf(&b, "--- Source: %s ---\n%s\n\n", p.label, p.body)
	}
	res := empty
	if len(sources) > 0 {
		res.Sources = sources
	}
	return finishContext(res, wholes, []string{b.String()}), nil
}

// storeEvidence finds topic in the store index. maxAge > 0 keeps what was
// collected within it. Failures come back as notes.
func storeEvidence(ctx context.Context, cfg *config.Config, topic string, limit int, maxAge time.Duration, r *retrieve.Retriever) ([]retrieve.Card, []string) {
	if strings.TrimSpace(cfg.Store.DSN) == "" {
		return nil, nil
	}
	if r == nil {
		octx, cancel := context.WithTimeout(ctx, 15*time.Second)
		var err error
		r, err = retrieve.Open(octx, cfg)
		cancel()
		if err != nil {
			return nil, []string{fmt.Sprintf("store index unavailable, so only report files were searched: %v", err)}
		}
		defer r.Close()
	}
	q := retrieve.Query{Text: topic, Limit: limit}
	if maxAge > 0 {
		since := time.Now().UTC().Add(-maxAge)
		q.Since, q.DateField = &since, retrieve.DateCollected
	}
	res, err := r.Find(ctx, q)
	if err != nil {
		return nil, []string{fmt.Sprintf("store search failed: %v", err)}
	}
	var notes []string
	if res.Note != "" {
		notes = append(notes, "store search "+res.Note)
	}
	return res.Cards, notes
}

// finishContext drops evidence already in the report files included
// whole, appends the evidence section, and counts.
func finishContext(res ContextResult, wholes, parts []string) ContextResult {
	var kept []retrieve.Card
	for _, c := range res.Evidence {
		if c.Kind == retrieve.KindReport && containedIn(strings.TrimSpace(c.Text), wholes) {
			continue
		}
		kept = append(kept, c)
	}
	res.Evidence = kept
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p)
	}
	if len(kept) > 0 {
		b.WriteString(FormatEvidence(kept))
	}
	res.Context = b.String()
	res.Count = len(res.Sources) + len(res.Evidence)
	return res
}

// FormatEvidence formats store cards for a prompt: each with its ref to
// cite, source, kind and dates.
func FormatEvidence(cards []retrieve.Card) string {
	var b strings.Builder
	for _, c := range cards {
		kind := "primary evidence"
		if c.Kind == retrieve.KindReport {
			kind = "researchguy synthesis, not primary evidence"
		} else if c.ContentKind == "abstract" {
			kind = "primary evidence, abstract only"
		}
		where := c.URL
		if c.Kind == retrieve.KindReport && len(c.Runs) > 0 {
			where = "report of run " + c.Runs[0]
		}
		title := c.Title
		if title == "" {
			title = c.Domain
		}
		if c.Kind == retrieve.KindClaim {
			writeClaimEvidence(&b, c, title)
			continue
		}
		fmt.Fprintf(&b, "--- Evidence [%s]: %s (%s; %s) ---\n%s\n%s\n\n", c.Ref, title, kind, c.Age, where, strings.TrimSpace(c.Text))
	}
	return b.String()
}

// writeClaimEvidence formats a claim card: the claim, the quote it was
// found in, its flags and the claims linked to it, each with how the link
// was made, since a model's label is an inference, not a finding.
func writeClaimEvidence(b *strings.Builder, c retrieve.Card, title string) {
	flags := "no cluster flags"
	if len(c.Flags) > 0 {
		flags = strings.Join(c.Flags, ", ")
	}
	fmt.Fprintf(b, "--- Evidence [%s]: claim from %s (primary evidence; %s; %s) ---\n%s\n%s\n", c.Ref, title, flags, c.Age, c.URL, strings.TrimSpace(c.Text))
	if c.QuoteVerified {
		fmt.Fprintf(b, "Quote: %q\n", c.Quote)
	}
	if cl := c.Cluster; cl != nil {
		if cl.Origins > 1 {
			fmt.Fprintf(b, "Said by %d independent origins (%s)\n", cl.Origins, cl.SupportDates)
		}
		for _, rel := range cl.Related {
			how := rel.Method
			if rel.Method == store.LinkModel {
				how = "model-labeled, an inference"
			}
			fmt.Fprintf(b, "%s [%s] (%s, %s; %s): %s\n", strings.ReplaceAll(rel.Relation, "_", " "), rel.Ref, rel.Domain, rel.Dated, how, strings.TrimSpace(rel.Text))
		}
	}
	b.WriteString("\n")
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

// chunkBody strips grepai's "File: <path>" header line from a chunk so the
// same text under two paths compares equal.
func chunkBody(content string) string {
	if first, rest, ok := strings.Cut(content, "\n"); ok && strings.HasPrefix(first, "File: ") {
		content = rest
	}
	return strings.TrimSpace(content)
}

func containedIn(body string, texts []string) bool {
	if body == "" {
		return false
	}
	for _, t := range texts {
		if strings.Contains(t, body) {
			return true
		}
	}
	return false
}

// capChunks cuts a file's joined chunks to MaxWholeFileBytes. grepai's
// chunk of a minified file is the one line it has, hundreds of KB long, and
// a file included as chunks gets no more room than one included whole.
func capChunks(body string) string {
	if len(body) <= MaxWholeFileBytes {
		return body
	}
	return TruncateBytes(body, MaxWholeFileBytes) + fmt.Sprintf("\n[chunks cut at %d bytes]", MaxWholeFileBytes)
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

// TruncateBytes cuts s to at most n bytes without splitting a UTF-8 rune.
func TruncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// truncateRunes is TruncateBytes plus "..." when it cuts.
func truncateRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return TruncateBytes(s, n) + "..."
}
