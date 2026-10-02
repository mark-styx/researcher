package cite

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/marklubin/researchguy/internal/critique"
	"github.com/marklubin/researchguy/internal/store"
	"github.com/marklubin/researchguy/internal/store/retrieve"
)

// target is what a marker cites: its text, and whether a quote missing
// from it can still be true.
type target struct {
	resolved *bool
	texts    []string
	partial  string // why the text may not hold every quote, "" when it's complete
	note     string
}

// Check resolves every citation in report. Captures are the report's run's;
// r looks up passages and sources, and nil leaves them unchecked.
func Check(ctx context.Context, report string, captures []store.Capture, r *retrieve.Retriever) []store.Citation {
	bySeq := make(map[string]store.Capture, len(captures))
	for _, c := range captures {
		bySeq[strconv.Itoa(c.Seq)] = c
	}
	cache := map[string]target{}
	var out []store.Citation
	for _, m := range Parse(report) {
		t, ok := cache[m.Marker]
		if !ok {
			switch m.Kind {
			case KindCapture:
				t = captureTarget(bySeq, m.ID)
			case KindPassage:
				t = passageTarget(ctx, r, m.ID)
			case KindSource:
				t = sourceTarget(ctx, r, m.ID)
			}
			cache[m.Marker] = t
		}
		c := store.Citation{Ord: m.Ord, Marker: m.Marker, TargetKind: m.Kind, TargetID: m.ID, ReportOffset: m.Offset,
			Group: m.Group, Quote: m.Quote, Resolved: t.resolved, Note: t.note}
		if m.Quote != "" {
			c.QuoteStatus = quoteStatus(m.Quote, t)
		}
		out = append(out, c)
	}
	return out
}

func quoteStatus(quote string, t target) string {
	switch {
	case t.resolved == nil:
		return store.QuoteUnchecked
	case !*t.resolved:
		return store.QuoteNotFound
	}
	for _, text := range t.texts {
		if Found(quote, text) {
			return store.QuoteFound
		}
	}
	if t.partial != "" {
		return store.QuoteUnverifiable
	}
	return store.QuoteNotFound
}

func resolved(b bool) *bool { return &b }

func captureTarget(bySeq map[string]store.Capture, seq string) target {
	c, ok := bySeq[seq]
	if !ok {
		return target{resolved: resolved(false), note: "no capture E" + seq + " in the run"}
	}
	var b strings.Builder
	b.WriteString(c.Content)
	for _, res := range c.Results {
		b.WriteString("\n" + res.Title + "\n" + res.Snippet)
	}
	t := target{resolved: resolved(true), texts: []string{b.String()}}
	if c.Action == "search" {
		t.partial = "search results hold snippets only"
	}
	return t
}

func passageTarget(ctx context.Context, r *retrieve.Retriever, id string) target {
	pid, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return target{resolved: resolved(false), note: "not a passage id"}
	}
	if r == nil {
		return target{note: "no index to look passages up in"}
	}
	// A quote may run across a passage boundary.
	p, err := r.Passage(ctx, pid, 1)
	if errors.Is(err, retrieve.ErrNotFound) {
		return target{resolved: resolved(false), note: "no such passage in the store"}
	}
	if err != nil {
		return target{note: "looking up the passage: " + err.Error()}
	}
	var parts []string
	for _, n := range p.Before {
		parts = append(parts, n.Text)
	}
	parts = append(parts, p.Text)
	for _, n := range p.After {
		parts = append(parts, n.Text)
	}
	t := target{resolved: resolved(true), texts: []string{strings.Join(parts, "\n")}}
	if p.ContentKind == store.KindAbstract {
		t.partial = "the store has only the abstract"
	}
	if p.Kind == retrieve.KindReport {
		t.note = "cites a researchguy report, not primary evidence"
	}
	return t
}

func sourceTarget(ctx context.Context, r *retrieve.Retriever, id string) target {
	if r == nil {
		return target{note: "no index to look sources up in"}
	}
	s, err := r.Source(ctx, retrieve.RefSource+":"+id)
	if errors.Is(err, retrieve.ErrNotFound) {
		return target{resolved: resolved(false), note: "no such source in the store"}
	}
	if err != nil {
		return target{note: "looking up the source: " + err.Error()}
	}
	t := target{resolved: resolved(true)}
	if s.Kind == retrieve.KindReport {
		t.note = "cites a researchguy report, not primary evidence"
	}
	full := false
	for _, v := range s.Documents {
		text, kind, err := r.DocumentText(ctx, v.DocumentID)
		if err != nil {
			continue
		}
		t.texts = append(t.texts, text)
		full = full || kind == store.KindFull
	}
	switch {
	case len(t.texts) == 0:
		t.partial = "the store has no text for the source"
	case !full:
		t.partial = "the store has only the abstract"
	}
	return t
}

// Summary counts a check's results. Quotes are counted once per group of
// citations, found when any citation in the group holds the quote.
type Summary struct {
	Citations, Resolved, Unresolved, Unchecked            int
	Quotes, QuotesFound, QuotesNotFound, QuotesUnverified int
	// Failures are citations that don't resolve and quotes none of their
	// citations hold.
	Failures []string
}

// Summarize counts cs and lists its failures.
func Summarize(cs []store.Citation) Summary {
	var s Summary
	type group struct {
		quote   string
		markers []string
		status  string
	}
	var groups []*group
	byGroup := map[int]*group{}
	rank := map[string]int{"": 0, store.QuoteNotFound: 1, store.QuoteUnchecked: 2, store.QuoteUnverifiable: 3, store.QuoteFound: 4}
	for _, c := range cs {
		s.Citations++
		switch {
		case c.Resolved == nil:
			s.Unchecked++
		case *c.Resolved:
			s.Resolved++
		default:
			s.Unresolved++
			s.Failures = append(s.Failures, fmt.Sprintf("[%s] doesn't resolve: %s.", c.Marker, c.Note))
		}
		if c.Quote == "" {
			continue
		}
		g := byGroup[c.Group]
		if g == nil {
			g = &group{quote: c.Quote}
			byGroup[c.Group] = g
			groups = append(groups, g)
		}
		g.markers = append(g.markers, c.Marker)
		if rank[c.QuoteStatus] > rank[g.status] {
			g.status = c.QuoteStatus
		}
	}
	for _, g := range groups {
		s.Quotes++
		switch g.status {
		case store.QuoteFound:
			s.QuotesFound++
		case store.QuoteNotFound:
			s.QuotesNotFound++
			s.Failures = append(s.Failures, fmt.Sprintf("%q [%s]: the quote isn't in the cited text.", clip(g.quote, 160), strings.Join(g.markers, ", ")))
		case store.QuoteUnverifiable:
			s.QuotesUnverified++
		}
	}
	return s
}

// Notes is the report section for the check's failures, "" when there are
// none.
func (s Summary) Notes() string {
	if len(s.Failures) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d citations: %d resolved, %d unresolved, %d not checked. %d quotes: %d found, %d not found, %d unverifiable (abstract, snippets or no text).\n\n",
		s.Citations, s.Resolved, s.Unresolved, s.Unchecked, s.Quotes, s.QuotesFound, s.QuotesNotFound, s.QuotesUnverified)
	for _, f := range s.Failures {
		b.WriteString("- " + f + "\n")
	}
	return b.String()
}

// AppendNotes adds the check's notes to a report: under its critic notes
// when it has them, else as their own section.
func AppendNotes(report, notes string) string {
	if notes == "" {
		return report
	}
	report = strings.TrimRight(report, "\n")
	if strings.Contains(report, "\n## Critic Notes\n") {
		return report + "\n\n### Citation Check\n\n" + notes
	}
	return report + "\n\n---\n\n## Citation Check\n\n" + notes
}

// maxCitedPassages caps the passages Passages returns.
const maxCitedPassages = 50

// Passages returns the store passages a draft cites, as evidence the
// critics can judge those citations against.
func Passages(ctx context.Context, draft string, r *retrieve.Retriever) []critique.Evidence {
	if r == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []critique.Evidence
	for _, m := range Parse(draft) {
		if m.Kind != KindPassage || seen[m.Marker] || len(out) == maxCitedPassages {
			continue
		}
		seen[m.Marker] = true
		id, err := strconv.ParseInt(m.ID, 10, 64)
		if err != nil {
			continue
		}
		p, err := r.Passage(ctx, id, 0)
		if err != nil {
			continue
		}
		label := strings.TrimSpace(p.Domain + " " + p.Title + " (" + p.Age + ")")
		if p.Kind == retrieve.KindReport {
			label += ", researchguy synthesis"
		}
		out = append(out, critique.Evidence{ID: m.Marker, Label: label, Content: p.Text})
	}
	return out
}

func clip(s string, max int) string {
	if r := []rune(s); len(r) > max {
		return string(r[:max-3]) + "..."
	}
	return s
}
