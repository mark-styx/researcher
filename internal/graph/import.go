package graph

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// querier is satisfied by *sql.DB, *sql.Conn, and *sql.Tx.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func nodeByKey(ctx context.Context, q querier, key string) (*Node, error) {
	row := q.QueryRowContext(ctx,
		`SELECT n.id, n.type, n.title, n.path, n.summary, n.created_at, n.updated_at, n.metadata
		 FROM node_keys k JOIN nodes n ON n.id = k.node_id WHERE k.key = ?`, key)
	return scanNode(row)
}

func setNodeKey(ctx context.Context, q querier, key, nodeID string) error {
	var existing string
	err := q.QueryRowContext(ctx, `SELECT node_id FROM node_keys WHERE key = ?`, key).Scan(&existing)
	switch {
	case err == nil && existing == nodeID:
		return nil
	case err == nil:
		return fmt.Errorf("key %q already belongs to node %s", key, existing)
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	_, err = q.ExecContext(ctx, `INSERT INTO node_keys (key, node_id) VALUES (?, ?)`, key, nodeID)
	return err
}

func insertNode(ctx context.Context, q querier, n *Node) error {
	if !isValidType(n.Type, ValidNodeTypes) {
		return fmt.Errorf("invalid node type %q", n.Type)
	}
	if n.Title == "" {
		return fmt.Errorf("node title is required")
	}
	if n.ID == "" {
		n.ID = uuid.New().String()
	}
	now := time.Now()
	if n.CreatedAt.IsZero() {
		n.CreatedAt = now
	}
	n.UpdatedAt = now
	metaJSON, err := marshalMetadata(n.Metadata)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx,
		`INSERT INTO nodes (id, type, title, path, summary, created_at, updated_at, metadata)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		n.ID, n.Type, n.Title, n.Path, n.Summary, n.CreatedAt, n.UpdatedAt, metaJSON)
	return err
}

func updateNodeMetadata(ctx context.Context, q querier, n *Node) error {
	metaJSON, err := marshalMetadata(n.Metadata)
	if err != nil {
		return err
	}
	n.UpdatedAt = time.Now()
	_, err = q.ExecContext(ctx, `UPDATE nodes SET metadata = ?, updated_at = ? WHERE id = ?`, metaJSON, n.UpdatedAt, n.ID)
	return err
}

// SourceEntry is one entry of a bookworm research/sources.json.
type SourceEntry struct {
	Title  string `json:"title"`
	Author string `json:"author"`
	URL    string `json:"url"`
	Type   string `json:"type"`
	Date   string `json:"date"`
	Notes  string `json:"notes"`
}

// BookRef identifies the book whose sources are being imported.
type BookRef struct {
	Slug         string // e.g. "the_poisoned_well"
	Title        string // report node title; defaults to Slug
	ResearchPath string // absolute path to the book's research dir, kept in metadata
}

// ImportStats reports what ImportSources did.
type ImportStats struct {
	Book           string `json:"book"`
	ReportNodeID   string `json:"report_node_id"`
	ReportCreated  bool   `json:"report_created"`
	Entries        int    `json:"entries"`
	SkippedNoURL   int    `json:"skipped_no_url"`
	InvalidURL     int    `json:"invalid_url"`
	UniqueSources  int    `json:"unique_sources"`
	SourcesCreated int    `json:"sources_created"`
	SourcesReused  int    `json:"sources_reused"`
	SourcesUpdated int    `json:"sources_updated"`
	EdgesCreated   int    `json:"edges_created"`
	EdgesExisting  int    `json:"edges_existing"`
}

// ImportSources records a book's bibliography in the graph: one report node
// for the book, one source node per normalized URL (shared across books),
// and a references edge from the book to each source. It is idempotent, and
// runs in one IMMEDIATE transaction so concurrent writers wait instead of
// interleaving. Entries without a usable http(s) URL are counted and skipped.
func (s *Store) ImportSources(book BookRef, entries []SourceEntry) (ImportStats, error) {
	stats := ImportStats{Book: book.Slug, Entries: len(entries)}
	if strings.TrimSpace(book.Slug) == "" {
		return stats, fmt.Errorf("book slug is required")
	}

	// Group entries by normalized URL, keeping first-seen order.
	type group struct {
		key     string
		entries []SourceEntry
	}
	var groups []*group
	byKey := map[string]*group{}
	for _, e := range entries {
		if strings.TrimSpace(e.URL) == "" {
			stats.SkippedNoURL++
			continue
		}
		key, err := URLKey(e.URL)
		if err != nil {
			stats.InvalidURL++
			continue
		}
		g, ok := byKey[key]
		if !ok {
			g = &group{key: key}
			byKey[key] = g
			groups = append(groups, g)
		}
		g.entries = append(g.entries, e)
	}
	stats.UniqueSources = len(groups)

	ctx := context.Background()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return stats, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return stats, fmt.Errorf("starting import transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			conn.ExecContext(ctx, "ROLLBACK")
		}
	}()

	if err := adoptUnkeyedSources(ctx, conn); err != nil {
		return stats, fmt.Errorf("adopting existing source nodes: %w", err)
	}

	report, created, err := findOrCreateByKey(ctx, conn, BookKey(book.Slug), func() *Node {
		title := book.Title
		if title == "" {
			title = book.Slug
		}
		meta := map[string]any{"book": book.Slug, "kind": "book"}
		if book.ResearchPath != "" {
			meta["research_path"] = book.ResearchPath
		}
		return &Node{Type: NodeReport, Title: title, Metadata: meta}
	})
	if err != nil {
		return stats, fmt.Errorf("book report node: %w", err)
	}
	stats.ReportNodeID, stats.ReportCreated = report.ID, created

	for _, g := range groups {
		first := g.entries[0]
		src, created, err := findOrCreateByKey(ctx, conn, g.key, func() *Node {
			title := strings.TrimSpace(first.Title)
			if title == "" {
				title = first.URL
			}
			return &Node{Type: NodeSource, Title: title, Metadata: map[string]any{
				"url":            strings.TrimSpace(first.URL),
				"normalized_url": strings.TrimPrefix(g.key, "url:"),
			}}
		})
		if err != nil {
			return stats, fmt.Errorf("source %s: %w", g.key, err)
		}
		if created {
			stats.SourcesCreated++
		} else {
			stats.SourcesReused++
		}
		if mergeSourceMetadata(src, book.Slug, g.entries) {
			if err := updateNodeMetadata(ctx, conn, src); err != nil {
				return stats, fmt.Errorf("updating source %s: %w", src.ID, err)
			}
			if !created {
				stats.SourcesUpdated++
			}
		}

		var one int
		err = conn.QueryRowContext(ctx,
			`SELECT 1 FROM edges WHERE from_id = ? AND to_id = ? AND type = ? LIMIT 1`,
			report.ID, src.ID, EdgeReferences).Scan(&one)
		switch {
		case err == nil:
			stats.EdgesExisting++
			continue
		case !errors.Is(err, sql.ErrNoRows):
			return stats, err
		}
		if _, err := conn.ExecContext(ctx,
			`INSERT INTO edges (id, from_id, to_id, type, created_at, metadata) VALUES (?, ?, ?, ?, ?, ?)`,
			uuid.New().String(), report.ID, src.ID, EdgeReferences, time.Now(), `{"book":`+jsonString(book.Slug)+`}`); err != nil {
			return stats, fmt.Errorf("creating edge: %w", err)
		}
		stats.EdgesCreated++
	}

	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return stats, fmt.Errorf("committing import: %w", err)
	}
	committed = true
	return stats, nil
}

// adoptUnkeyedSources registers URL keys for source nodes created without
// one (e.g. by `graph add-node`), so an import reuses them instead of making
// duplicates. Nodes with no usable metadata.url, or whose key another node
// already holds, are left alone.
func adoptUnkeyedSources(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx,
		`SELECT id, metadata FROM nodes WHERE type = ? AND id NOT IN (SELECT node_id FROM node_keys) ORDER BY created_at`, NodeSource)
	if err != nil {
		return err
	}
	type pending struct{ id, key string }
	var todo []pending
	for rows.Next() {
		var id string
		var metaJSON sql.NullString
		if err := rows.Scan(&id, &metaJSON); err != nil {
			rows.Close()
			return err
		}
		meta, err := unmarshalMetadata(metaJSON)
		if err != nil {
			continue
		}
		raw, _ := meta["url"].(string)
		if key, err := URLKey(raw); err == nil {
			todo = append(todo, pending{id, key})
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, p := range todo {
		if _, err := conn.ExecContext(ctx, `INSERT OR IGNORE INTO node_keys (key, node_id) VALUES (?, ?)`, p.key, p.id); err != nil {
			return err
		}
	}
	return nil
}

func findOrCreateByKey(ctx context.Context, q querier, key string, build func() *Node) (*Node, bool, error) {
	n, err := nodeByKey(ctx, q, key)
	if err == nil {
		return n, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	n = build()
	if err := insertNode(ctx, q, n); err != nil {
		return nil, false, err
	}
	if err := setNodeKey(ctx, q, key, n.ID); err != nil {
		return nil, false, err
	}
	return n, true, nil
}

// mergeSourceMetadata folds one book's entries for a source into its
// metadata: the book joins "books", its distinct notes go under
// notes[book], and empty author/type/date fields are filled in. Notes from
// other books are never overwritten. Reports whether anything changed.
func mergeSourceMetadata(n *Node, book string, entries []SourceEntry) bool {
	if n.Metadata == nil {
		n.Metadata = map[string]any{}
	}
	changed := false

	books := stringList(n.Metadata["books"])
	if !contains(books, book) {
		books = append(books, book)
		sort.Strings(books)
		n.Metadata["books"] = toAnyList(books)
		changed = true
	}

	notes, _ := n.Metadata["notes"].(map[string]any)
	if notes == nil {
		notes = map[string]any{}
	}
	bookNotes := stringList(notes[book])
	for _, e := range entries {
		if note := strings.TrimSpace(e.Notes); note != "" && !contains(bookNotes, note) {
			bookNotes = append(bookNotes, note)
			changed = true
		}
	}
	if len(bookNotes) > 0 {
		notes[book] = toAnyList(bookNotes)
		n.Metadata["notes"] = notes
	}

	for _, field := range []struct {
		key string
		get func(SourceEntry) string
	}{
		{"author", func(e SourceEntry) string { return e.Author }},
		{"type", func(e SourceEntry) string { return e.Type }},
		{"date", func(e SourceEntry) string { return e.Date }},
	} {
		if cur, _ := n.Metadata[field.key].(string); cur != "" {
			continue
		}
		for _, e := range entries {
			if v := strings.TrimSpace(field.get(e)); v != "" {
				n.Metadata[field.key] = v
				changed = true
				break
			}
		}
	}
	return changed
}

func stringList(v any) []string {
	switch tv := v.(type) {
	case []string:
		return append([]string(nil), tv...)
	case []any:
		out := make([]string, 0, len(tv))
		for _, x := range tv {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func toAnyList(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
