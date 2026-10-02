package retrieve

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// RunSource is a source a run's captures saw or its fetch stage tried.
type RunSource struct {
	ID     int64  `json:"source_id"`
	Ref    string `json:"ref"`
	URL    string `json:"url"`
	Domain string `json:"domain"`
	Title  string `json:"title,omitempty"`
	Kind   string `json:"kind"`
	// Seen are the captures (E<seq>) that saw the source, in order.
	Seen []string `json:"seen,omitempty"`
	// Tried is whether the run's fetch stage tried the source, and
	// FetchError why its best attempt failed.
	Tried      bool   `json:"tried"`
	FetchError string `json:"fetch_error,omitempty"`
	// The document the run fetched, when one was.
	DocumentID  int64      `json:"document_id,omitempty"`
	ContentKind string     `json:"content_kind,omitempty"`
	Passages    int        `json:"passages,omitempty"`
	Published   *Date      `json:"published,omitempty"`
	Collected   *time.Time `json:"collected,omitempty"`
}

// RunSourcesResult is a run's sources, fetched ones first.
type RunSourcesResult struct {
	RunID   string      `json:"run_id"`
	Total   int         `json:"total"`
	Sources []RunSource `json:"sources"`
}

// maxSeen caps the capture refs listed per source.
const maxSeen = 5

// RunSources lists the sources of a run, at most limit of them (0 is no
// limit): fetched documents first, then failed fetches, then the rest, each
// in the order the run first saw them.
func (r *Retriever) RunSources(ctx context.Context, runID string, limit int) (RunSourcesResult, error) {
	res := RunSourcesResult{RunID: runID}
	rows, err := r.Index.Pool().Query(ctx, `
		WITH seen AS (
			SELECT source_id, array_agg(seq ORDER BY seq) AS seqs
			FROM (SELECT DISTINCT source_id, seq FROM sightings WHERE run_id = $1) g
			GROUP BY source_id
		), first AS (
			SELECT DISTINCT ON (source_id) source_id, seq, ord
			FROM sightings WHERE run_id = $1 ORDER BY source_id, seq, ord
		), tried AS (
			SELECT DISTINCT ON (source_id) source_id, document_id, http_status, error
			FROM fetches WHERE run_id = $1
			ORDER BY source_id, (document_id IS NOT NULL) DESC, attempted_at DESC, seq DESC
		), ids AS (
			SELECT source_id FROM seen UNION SELECT source_id FROM tried
		)
		SELECT s.id, s.url, s.domain, s.title, s.kind, coalesce(seen.seqs, '{}'), t.source_id IS NOT NULL,
			t.http_status, coalesce(t.error, ''), d.id, d.content_kind, d.published_at, d.published_precision,
			d.published_from, coalesce(d.published_weak, false), d.first_fetched_at,
			(SELECT count(*) FROM passages p WHERE p.document_id = d.id)
		FROM ids JOIN sources s ON s.id = ids.source_id
		LEFT JOIN seen ON seen.source_id = s.id
		LEFT JOIN tried t ON t.source_id = s.id
		LEFT JOIN documents d ON d.id = t.document_id
		LEFT JOIN first ON first.source_id = s.id
		ORDER BY (d.id IS NOT NULL) DESC, (t.source_id IS NOT NULL) DESC, first.seq NULLS LAST, first.ord, s.id`, runID)
	if err != nil {
		return res, err
	}
	all, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (RunSource, error) {
		var s RunSource
		var seqs []int32
		var status *int
		var docID *int64
		var kind, precision, from *string
		var pubAt, collected *time.Time
		var weak bool
		err := row.Scan(&s.ID, &s.URL, &s.Domain, &s.Title, &s.Kind, &seqs, &s.Tried, &status, &s.FetchError,
			&docID, &kind, &pubAt, &precision, &from, &weak, &collected, &s.Passages)
		s.Ref = SourceRef(s.ID)
		for i, q := range seqs {
			if i == maxSeen {
				s.Seen = append(s.Seen, fmt.Sprintf("+%d more", len(seqs)-maxSeen))
				break
			}
			s.Seen = append(s.Seen, RefCapture+strconv.Itoa(int(q)))
		}
		if docID != nil {
			s.DocumentID, s.ContentKind, s.Collected = *docID, *kind, collected
			s.Published = dateOf(pubAt, precision, from, weak)
		} else if s.Tried && s.FetchError == "" && status != nil {
			s.FetchError = "HTTP " + strconv.Itoa(*status)
		}
		return s, err
	})
	if err != nil {
		return res, err
	}
	res.Total = len(all)
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	res.Sources = all
	return res, nil
}

// Table is the run's sources as a markdown table for a prompt.
func (res RunSourcesResult) Table() string {
	if len(res.Sources) == 0 {
		return "(no sources recorded for this run)\n"
	}
	var b strings.Builder
	b.WriteString("| Source | Domain | Title | Published | Collected | Text | Seen in |\n")
	b.WriteString("|---|---|---|---|---|---|---|\n")
	for _, s := range res.Sources {
		published, collected := "", ""
		if s.DocumentID != 0 {
			published = "undated"
			if s.Published != nil {
				published = s.Published.Date
				if s.Published.Weak {
					published += " (weak)"
				}
			}
			collected = s.Collected.Format(time.DateOnly)
		}
		fmt.Fprintf(&b, "| [%s] | %s | %s | %s | %s | %s | %s |\n", s.Ref, cell(s.Domain, 40), cell(s.Title, 90),
			published, collected, textStatus(s), strings.Join(s.Seen, ", "))
	}
	if res.Total > len(res.Sources) {
		fmt.Fprintf(&b, "\n%d more sources not listed.\n", res.Total-len(res.Sources))
	}
	return b.String()
}

// textStatus says what text the run has for a source.
func textStatus(s RunSource) string {
	switch {
	case s.DocumentID != 0:
		kind := s.ContentKind
		if kind == "abstract" {
			kind = "abstract only"
		}
		noun := "passages"
		if s.Passages == 1 {
			noun = "passage"
		}
		return fmt.Sprintf("%s, %d %s", kind, s.Passages, noun)
	case s.Tried:
		return cell("fetch failed: "+s.FetchError, 60)
	default:
		return "not fetched"
	}
}

// cell makes s safe for one markdown table cell, at most max characters.
func cell(s string, max int) string {
	s = strings.Join(strings.Fields(strings.ReplaceAll(s, "|", "/")), " ")
	if r := []rune(s); len(r) > max {
		s = string(r[:max-3]) + "..."
	}
	return s
}
