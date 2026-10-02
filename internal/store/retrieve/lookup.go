package retrieve

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/marklubin/researchguy/internal/graph"
	"github.com/marklubin/researchguy/internal/store/index"
)

// ErrNotFound is returned for an id or URL the index doesn't have.
var ErrNotFound = errors.New("not in the index")

// Neighbor is a passage next to the one asked for.
type Neighbor struct {
	PassageID int64  `json:"passage_id"`
	Ord       int    `json:"ord"`
	CharStart int    `json:"char_start"`
	CharEnd   int    `json:"char_end"`
	Text      string `json:"text"`
}

// PassageResult is a passage as a card, with the passages around it.
type PassageResult struct {
	Card
	Ord    int        `json:"ord"`
	Before []Neighbor `json:"before,omitempty"`
	After  []Neighbor `json:"after,omitempty"`
}

// Passage looks up a passage and up to neighbors passages on each side.
func (r *Retriever) Passage(ctx context.Context, id int64, neighbors int) (PassageResult, error) {
	cards, err := r.cards(ctx, []int64{id})
	if err != nil {
		return PassageResult{}, err
	}
	if len(cards) == 0 {
		return PassageResult{}, fmt.Errorf("passage %d: %w", id, ErrNotFound)
	}
	res := PassageResult{Card: cards[0]}
	res.Age = ageLabel(&res.Card, r.now())
	if err := r.Index.Pool().QueryRow(ctx, `SELECT ord FROM passages WHERE id = $1`, id).Scan(&res.Ord); err != nil {
		return res, err
	}
	neighbors = min(max(neighbors, 0), 5)
	if neighbors == 0 {
		return res, nil
	}
	rows, err := r.Index.Pool().Query(ctx, `SELECT id, ord, char_start, char_end, text FROM passages
		WHERE document_id = $1 AND ord BETWEEN $2 AND $3 AND id <> $4 ORDER BY ord`,
		res.DocumentID, res.Ord-neighbors, res.Ord+neighbors, id)
	if err != nil {
		return res, err
	}
	near, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Neighbor])
	if err != nil {
		return res, err
	}
	for _, n := range near {
		if n.Ord < res.Ord {
			res.Before = append(res.Before, n)
		} else {
			res.After = append(res.After, n)
		}
	}
	return res, nil
}

// Version is one document of a source: a distinct text it was fetched
// with.
type Version struct {
	DocumentID  int64     `json:"document_id"`
	ContentKind string    `json:"content_kind"`
	Origin      string    `json:"origin"`
	TextChars   int       `json:"text_chars"`
	Published   *Date     `json:"published,omitempty"`
	Collected   time.Time `json:"collected"`
	LastFetched time.Time `json:"last_fetched"`
}

// Attempt is one fetch attempt.
type Attempt struct {
	RunID       string    `json:"run_id"`
	Seq         int       `json:"seq"`
	Reason      string    `json:"reason"`
	Rank        *int      `json:"rank,omitempty"`
	Via         string    `json:"via"`
	FetchURL    string    `json:"fetch_url"`
	AttemptedAt time.Time `json:"attempted_at"`
	HTTPStatus  *int      `json:"http_status,omitempty"`
	Error       string    `json:"error,omitempty"`
	DocumentID  *int64    `json:"document_id,omitempty"`
}

// DocumentResult is a document's metadata and a slice of its text.
type DocumentResult struct {
	Version
	SourceID   int64  `json:"source_id"`
	SourceRef  string `json:"source_ref"`
	URL        string `json:"url"`
	Domain     string `json:"domain"`
	Title      string `json:"title,omitempty"`
	SourceKind string `json:"source_kind"`
	DOI        string `json:"doi,omitempty"`
	SHA256     string `json:"sha256"`
	// Offset and Text are the slice asked for, in characters; Truncated
	// says the text goes on past it.
	Offset    int    `json:"offset"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated,omitempty"`
	// Versions are the source's other documents; Fetches the attempts that
	// got this one.
	Versions []Version `json:"versions,omitempty"`
	Fetches  []Attempt `json:"fetches,omitempty"`
}

// MaxDocumentChars caps one Document slice.
const MaxDocumentChars = 50000

// Document looks up a document and returns chars characters of its text
// from offset (default 8000, at most MaxDocumentChars). The text comes
// from the store, which is the record; the index only says where it is.
func (r *Retriever) Document(ctx context.Context, id int64, offset, chars int) (DocumentResult, error) {
	var d DocumentResult
	var pubAt *time.Time
	var precision, from, doi *string
	var weak bool
	var docTitle, srcTitle string
	err := r.Index.Pool().QueryRow(ctx, `SELECT d.id, d.content_kind, d.origin, d.text_chars, d.published_at, d.published_precision,
		d.published_from, d.published_weak, d.first_fetched_at, d.last_fetched_at, d.sha256, d.title,
		s.id, s.url, s.domain, s.title, s.kind, s.doi
		FROM documents d JOIN sources s ON s.id = d.source_id WHERE d.id = $1`, id).
		Scan(&d.DocumentID, &d.ContentKind, &d.Origin, &d.TextChars, &pubAt, &precision, &from, &weak, &d.Collected, &d.LastFetched,
			&d.SHA256, &docTitle, &d.SourceID, &d.URL, &d.Domain, &srcTitle, &d.SourceKind, &doi)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, fmt.Errorf("document %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return d, err
	}
	d.Published = dateOf(pubAt, precision, from, weak)
	d.SourceRef = SourceRef(d.SourceID)
	d.Title = docTitle
	if d.Title == "" {
		d.Title = srcTitle
	}
	if doi != nil {
		d.DOI = *doi
	}
	if chars <= 0 {
		chars = 8000
	}
	chars = min(chars, MaxDocumentChars)
	text, err := r.Store.ReadText(d.SHA256)
	if err != nil {
		return d, fmt.Errorf("document %d's text: %w", id, err)
	}
	runes := []rune(text)
	d.Offset = min(max(offset, 0), len(runes))
	end := min(d.Offset+chars, len(runes))
	d.Text, d.Truncated = string(runes[d.Offset:end]), end < len(runes)

	if d.Versions, err = r.versions(ctx, d.SourceID, id); err != nil {
		return d, err
	}
	d.Fetches, err = r.attempts(ctx, `WHERE f.document_id = $1`, id)
	return d, err
}

func (r *Retriever) versions(ctx context.Context, sourceID, except int64) ([]Version, error) {
	rows, err := r.Index.Pool().Query(ctx, `SELECT id, content_kind, origin, text_chars, published_at, published_precision,
		published_from, published_weak, first_fetched_at, last_fetched_at
		FROM documents WHERE source_id = $1 AND id <> $2 ORDER BY first_fetched_at DESC, id`, sourceID, except)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Version, error) {
		var v Version
		var pubAt *time.Time
		var precision, from *string
		var weak bool
		err := row.Scan(&v.DocumentID, &v.ContentKind, &v.Origin, &v.TextChars, &pubAt, &precision, &from, &weak, &v.Collected, &v.LastFetched)
		v.Published = dateOf(pubAt, precision, from, weak)
		return v, err
	})
}

// maxListed caps the attempts and sightings a lookup lists.
const maxListed = 50

func (r *Retriever) attempts(ctx context.Context, where string, arg any) ([]Attempt, error) {
	rows, err := r.Index.Pool().Query(ctx, `SELECT f.run_id, f.seq, f.reason, f.rank, f.via, f.fetch_url, f.attempted_at,
		f.http_status, f.error, f.document_id FROM fetches f `+where+`
		ORDER BY f.attempted_at DESC, f.run_id DESC, f.seq DESC LIMIT `+strconv.Itoa(maxListed), arg)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Attempt])
}

// Sighting is a capture that saw a source.
type Sighting struct {
	RunID   string `json:"run_id"`
	Seq     int    `json:"seq"`
	Ref     string `json:"ref"` // E<seq> within the run
	Role    string `json:"role"`
	Rank    *int   `json:"rank,omitempty"`
	Title   string `json:"title,omitempty"`
	Snippet string `json:"snippet,omitempty"`
}

// SourceResult is a source and its history: its documents, the captures
// that saw it, the attempts to fetch it, and the runs that cited it.
type SourceResult struct {
	ID        int64      `json:"source_id"`
	Ref       string     `json:"ref"`
	URL       string     `json:"url"`
	URLKey    string     `json:"url_key"`
	Domain    string     `json:"domain"`
	Title     string     `json:"title,omitempty"`
	Kind      string     `json:"kind"`
	DOI       string     `json:"doi,omitempty"`
	FirstSeen time.Time  `json:"first_seen"`
	LastSeen  time.Time  `json:"last_seen"`
	Documents []Version  `json:"documents"`
	Sightings []Sighting `json:"sightings,omitempty"`
	Fetches   []Attempt  `json:"fetches,omitempty"`
	// CitedBy are runs that cited the source in a worker draft or their
	// report.
	CitedBy []string `json:"cited_by,omitempty"`
}

// Source looks up a source by URL (any variant that normalizes to it), by
// id, or by its S:<id> ref.
func (r *Retriever) Source(ctx context.Context, ref string) (SourceResult, error) {
	ref = strings.TrimSpace(ref)
	var where string
	var arg any
	if id, err := strconv.ParseInt(strings.TrimPrefix(ref, RefSource+":"), 10, 64); err == nil {
		where, arg = `id = $1`, id
	} else if strings.HasPrefix(ref, index.ReportKey("")) {
		where, arg = `url_key = $1`, ref
	} else {
		key, err := graph.NormalizeURL(ref)
		if err != nil {
			return SourceResult{}, fmt.Errorf("%q is not a source id or URL: %w", ref, err)
		}
		where, arg = `url_key = $1`, key
	}
	var s SourceResult
	var doi *string
	err := r.Index.Pool().QueryRow(ctx, `SELECT id, url, url_key, domain, title, kind, doi, first_seen_at, last_seen_at
		FROM sources WHERE `+where, arg).Scan(&s.ID, &s.URL, &s.URLKey, &s.Domain, &s.Title, &s.Kind, &doi, &s.FirstSeen, &s.LastSeen)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, fmt.Errorf("source %s: %w", ref, ErrNotFound)
	}
	if err != nil {
		return s, err
	}
	s.Ref = SourceRef(s.ID)
	if doi != nil {
		s.DOI = *doi
	}
	if s.Documents, err = r.versions(ctx, s.ID, 0); err != nil {
		return s, err
	}
	rows, err := r.Index.Pool().Query(ctx, `SELECT g.run_id, g.seq, g.role, g.rank, g.title, g.snippet
		FROM sightings g JOIN captures c ON c.run_id = g.run_id AND c.seq = g.seq
		WHERE g.source_id = $1 ORDER BY c.captured_at DESC, g.run_id DESC, g.seq DESC LIMIT `+strconv.Itoa(maxListed), s.ID)
	if err != nil {
		return s, err
	}
	if s.Sightings, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (Sighting, error) {
		var g Sighting
		err := row.Scan(&g.RunID, &g.Seq, &g.Role, &g.Rank, &g.Title, &g.Snippet)
		g.Ref = RefCapture + strconv.Itoa(g.Seq)
		return g, err
	}); err != nil {
		return s, err
	}
	if s.Fetches, err = r.attempts(ctx, `WHERE f.source_id = $1`, s.ID); err != nil {
		return s, err
	}
	rows, err = r.Index.Pool().Query(ctx, `SELECT DISTINCT run_id FROM fetches WHERE source_id = $1 AND reason = 'cited' ORDER BY run_id DESC`, s.ID)
	if err != nil {
		return s, err
	}
	s.CitedBy, err = pgx.CollectRows(rows, pgx.RowTo[string])
	return s, err
}
