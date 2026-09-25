// Package graph stores the node/edge model that underlies research memory:
// entities, sources, claims, funding-pattern observations, and reports as
// independently referenceable nodes, connected by typed edges. Node content
// lives in markdown files (so grepai keeps indexing it); this package only
// tracks structure and relationships.
package graph

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/sqlitedb"
)

// Node types.
const (
	NodeEntity         = "entity"
	NodeSource         = "source"
	NodeClaim          = "claim"
	NodeFundingPattern = "funding-pattern"
	NodeReport         = "report"
	// NodeLead is an unconfirmed breadcrumb (a forum comment, an offhand
	// mention) worth chasing but not evidence. Link it to what it concerns
	// with a references edge; once confirmed, a new claim supersedes it.
	NodeLead = "lead"
)

// ValidNodeTypes lists every accepted Node.Type value.
var ValidNodeTypes = []string{NodeEntity, NodeSource, NodeClaim, NodeFundingPattern, NodeReport, NodeLead}

// Edge types.
const (
	EdgeFunds            = "funds"
	EdgeAuthoredBy       = "authored-by"
	EdgeSupports         = "supports"
	EdgeContradicts      = "contradicts"
	EdgeSponsorsResearch = "sponsors-research"
	EdgeReferences       = "references"
	EdgeSupersedes       = "supersedes"
)

// ValidEdgeTypes lists every accepted Edge.Type value.
var ValidEdgeTypes = []string{EdgeFunds, EdgeAuthoredBy, EdgeSupports, EdgeContradicts, EdgeSponsorsResearch, EdgeReferences, EdgeSupersedes}

// Node is a single entity/source/claim/funding-pattern/report record.
// Content beyond the summary lives in the markdown file at Path, if set —
// "thin" nodes (e.g. some report nodes) may have no Path of their own.
type Node struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	Title     string         `json:"title"`
	Path      string         `json:"path,omitempty"` // relative to research_dir; empty for thin nodes
	Summary   string         `json:"summary,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// Edge is a typed, directed relationship between two nodes.
type Edge struct {
	ID        string         `json:"id"`
	FromID    string         `json:"from_id"`
	ToID      string         `json:"to_id"`
	Type      string         `json:"type"`
	CreatedAt time.Time      `json:"created_at"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// EdgeFilter narrows ListEdges. Zero-value fields are unconstrained.
type EdgeFilter struct {
	FromID string
	ToID   string
	Type   string
}

type Store struct {
	db *sql.DB
}

// NewStore opens the shared ~/.researchguy/tasks.db and ensures the graph
// tables exist alongside the scheduler's tables.
func NewStore(cfg *config.Config) (*Store, error) {
	dbPath := filepath.Join(config.Dir(), "tasks.db")
	db, err := sqlitedb.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrating graph tables: %w", err)
	}
	return &Store{db: db}, nil
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS nodes (
			id         TEXT PRIMARY KEY,
			type       TEXT NOT NULL,
			title      TEXT NOT NULL,
			path       TEXT,
			summary    TEXT,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			metadata   TEXT
		);
		CREATE INDEX IF NOT EXISTS idx_nodes_type ON nodes(type);

		CREATE TABLE IF NOT EXISTS edges (
			id         TEXT PRIMARY KEY,
			from_id    TEXT NOT NULL REFERENCES nodes(id),
			to_id      TEXT NOT NULL REFERENCES nodes(id),
			type       TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			metadata   TEXT
		);
		CREATE INDEX IF NOT EXISTS idx_edges_from ON edges(from_id);
		CREATE INDEX IF NOT EXISTS idx_edges_to ON edges(to_id);
		CREATE INDEX IF NOT EXISTS idx_edges_type ON edges(type);

		-- Stable identity keys (normalized source URL, book slug) so the same
		-- thing imported twice maps to one node. Additive: nodes is unchanged.
		CREATE TABLE IF NOT EXISTS node_keys (
			key     TEXT PRIMARY KEY,
			node_id TEXT NOT NULL REFERENCES nodes(id)
		);
		CREATE INDEX IF NOT EXISTS idx_node_keys_node ON node_keys(node_id);
	`)
	return err
}

// NodeByKey returns the node registered under key (see URLKey, BookKey), or
// sql.ErrNoRows when none is.
func (s *Store) NodeByKey(key string) (*Node, error) {
	return nodeByKey(context.Background(), s.db, key)
}

// SetNodeKey registers key for nodeID. A key already pointing at a
// different node is an error; re-registering the same pair is a no-op.
func (s *Store) SetNodeKey(key, nodeID string) error {
	return setNodeKey(context.Background(), s.db, key, nodeID)
}

// CitedByCounts returns, for each node with incoming edges of edgeType, how
// many distinct nodes point at it (e.g. how many books reference a source).
func (s *Store) CitedByCounts(edgeType string) (map[string]int, error) {
	rows, err := s.db.Query(`SELECT to_id, COUNT(DISTINCT from_id) FROM edges WHERE type = ? GROUP BY to_id`, edgeType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

func (s *Store) Close() error {
	return s.db.Close()
}

func isValidType(t string, valid []string) bool {
	for _, v := range valid {
		if t == v {
			return true
		}
	}
	return false
}

// CreateNode assigns an ID and timestamps if unset, validates Type, and inserts.
func (s *Store) CreateNode(n *Node) error {
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

	_, err = s.db.Exec(
		`INSERT INTO nodes (id, type, title, path, summary, created_at, updated_at, metadata)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		n.ID, n.Type, n.Title, n.Path, n.Summary, n.CreatedAt, n.UpdatedAt, metaJSON,
	)
	return err
}

// UpdateNode updates title/path/summary/metadata for an existing node and
// bumps updated_at. This is the hook a future recency/rollup pass would use
// to re-summarize a node as new information arrives, without losing the
// node's identity or its incoming/outgoing edges.
func (s *Store) UpdateNode(n *Node) error {
	metaJSON, err := marshalMetadata(n.Metadata)
	if err != nil {
		return err
	}
	n.UpdatedAt = time.Now()
	res, err := s.db.Exec(
		`UPDATE nodes SET title = ?, path = ?, summary = ?, updated_at = ?, metadata = ? WHERE id = ?`,
		n.Title, n.Path, n.Summary, n.UpdatedAt, metaJSON, n.ID,
	)
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("node %q not found", n.ID)
	}
	return nil
}

func (s *Store) GetNode(id string) (*Node, error) {
	row := s.db.QueryRow(
		`SELECT id, type, title, path, summary, created_at, updated_at, metadata FROM nodes WHERE id = ?`, id,
	)
	return scanNode(row)
}

// ListNodes returns nodes, optionally filtered by type. Empty typeFilter returns all.
func (s *Store) ListNodes(typeFilter string) ([]*Node, error) {
	var rows *sql.Rows
	var err error
	if typeFilter == "" {
		rows, err = s.db.Query(`SELECT id, type, title, path, summary, created_at, updated_at, metadata FROM nodes ORDER BY updated_at DESC`)
	} else {
		rows, err = s.db.Query(`SELECT id, type, title, path, summary, created_at, updated_at, metadata FROM nodes WHERE type = ? ORDER BY updated_at DESC`, typeFilter)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Node
	for rows.Next() {
		n, err := scanNodeRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ListStale scans nodes that have a Path and classifies each against
// researchDir: a node whose linked file's mtime is newer than the node's
// UpdatedAt is stale (new information arrived since the last summary); a
// node whose linked file no longer exists is orphaned. Thin nodes (empty
// Path) are excluded from both — they have nothing to re-summarize from.
// Nodes already flagged orphaned are skipped: there's nothing to resummarize
// until the file reappears, at which point a fresh mtime makes it stale again.
func (s *Store) ListStale(researchDir string) (stale, orphaned []*Node, err error) {
	rows, err := s.db.Query(`SELECT id, type, title, path, summary, created_at, updated_at, metadata FROM nodes WHERE path IS NOT NULL AND path != ''`)
	if err != nil {
		return nil, nil, err
	}
	var candidates []*Node
	for rows.Next() {
		n, serr := scanNodeRows(rows)
		if serr != nil {
			rows.Close()
			return nil, nil, serr
		}
		candidates = append(candidates, n)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, nil, err
	}
	rows.Close()

	for _, n := range candidates {
		alreadyOrphaned, _ := n.Metadata["orphaned"].(bool)

		info, statErr := os.Stat(filepath.Join(researchDir, n.Path))
		if statErr != nil {
			if !alreadyOrphaned {
				orphaned = append(orphaned, n)
			}
			continue
		}
		if info.ModTime().After(n.UpdatedAt) {
			stale = append(stale, n)
		}
	}
	return stale, orphaned, nil
}

// CreateEdge assigns an ID and timestamp if unset, validates Type and that
// both endpoints exist, and inserts.
func (s *Store) CreateEdge(e *Edge) error {
	if !isValidType(e.Type, ValidEdgeTypes) {
		return fmt.Errorf("invalid edge type %q", e.Type)
	}
	if e.FromID == "" || e.ToID == "" {
		return fmt.Errorf("edge requires both from_id and to_id")
	}
	for _, id := range []string{e.FromID, e.ToID} {
		if _, err := s.GetNode(id); err != nil {
			return fmt.Errorf("endpoint node %q not found: %w", id, err)
		}
	}
	if e.ID == "" {
		e.ID = uuid.New().String()
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now()
	}
	metaJSON, err := marshalMetadata(e.Metadata)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO edges (id, from_id, to_id, type, created_at, metadata) VALUES (?, ?, ?, ?, ?, ?)`,
		e.ID, e.FromID, e.ToID, e.Type, e.CreatedAt, metaJSON,
	)
	return err
}

// ListEdges returns edges matching filter. Zero-value filter fields are unconstrained.
func (s *Store) ListEdges(filter EdgeFilter) ([]*Edge, error) {
	query := `SELECT id, from_id, to_id, type, created_at, metadata FROM edges WHERE 1=1`
	var args []any
	if filter.FromID != "" {
		query += ` AND from_id = ?`
		args = append(args, filter.FromID)
	}
	if filter.ToID != "" {
		query += ` AND to_id = ?`
		args = append(args, filter.ToID)
	}
	if filter.Type != "" {
		query += ` AND type = ?`
		args = append(args, filter.Type)
	}
	query += ` ORDER BY created_at DESC`

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Edge
	for rows.Next() {
		var e Edge
		var metaJSON sql.NullString
		if err := rows.Scan(&e.ID, &e.FromID, &e.ToID, &e.Type, &e.CreatedAt, &metaJSON); err != nil {
			return nil, err
		}
		if e.Metadata, err = unmarshalMetadata(metaJSON); err != nil {
			return nil, err
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}

type scannable interface {
	Scan(dest ...any) error
}

func scanNode(row scannable) (*Node, error) {
	return scanNodeRows(row)
}

func scanNodeRows(row scannable) (*Node, error) {
	var n Node
	var path, summary sql.NullString
	var metaJSON sql.NullString
	if err := row.Scan(&n.ID, &n.Type, &n.Title, &path, &summary, &n.CreatedAt, &n.UpdatedAt, &metaJSON); err != nil {
		return nil, err
	}
	n.Path = path.String
	n.Summary = summary.String
	meta, err := unmarshalMetadata(metaJSON)
	if err != nil {
		return nil, err
	}
	n.Metadata = meta
	return &n, nil
}

func marshalMetadata(m map[string]any) (string, error) {
	if len(m) == 0 {
		return "", nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("marshaling metadata: %w", err)
	}
	return string(b), nil
}

func unmarshalMetadata(s sql.NullString) (map[string]any, error) {
	if !s.Valid || s.String == "" {
		return nil, nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s.String), &m); err != nil {
		return nil, fmt.Errorf("parsing metadata: %w", err)
	}
	return m, nil
}
