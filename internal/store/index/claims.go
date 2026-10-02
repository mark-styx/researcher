package index

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/marklubin/researchguy/internal/quote"
	"github.com/marklubin/researchguy/internal/store"
	"github.com/pgvector/pgvector-go"
)

// querier is what claim syncing needs from a pool or a transaction.
type querier interface {
	Begin(ctx context.Context) (pgx.Tx, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
}

// ClaimStats is what SyncClaims did.
type ClaimStats struct {
	Extractions int `json:"extractions"` // extraction files indexed, per document
	Claims      int `json:"claims"`
	Verified    int `json:"verified"` // of those, quotes found in the text
	// Waiting are extraction files whose text no indexed document has
	// yet.
	Waiting   int               `json:"waiting"`
	RuleLinks int64             `json:"rule_links"`
	Links     int               `json:"links"` // logged links read
	BadLinks  []int             `json:"bad_link_lines,omitempty"`
	Origins   *OriginStats      `json:"origins,omitempty"`
	Failed    map[string]string `json:"failed,omitempty"` // extraction file: error
}

// MinKeyWords is the shortest quote two claims can be linked `same` by
// for quoting the same words. Shorter spans are common phrases.
const MinKeyWords = 6

// SyncClaims indexes the store's claim extractions that are new or changed
// for every primary document with their text, links claims that quote the
// same words, reads new lines of the link log, and regroups origins when
// anything changed. A file that fails is in Failed and the rest go on.
func (ix *Index) SyncClaims(ctx context.Context, st *store.Store) (ClaimStats, error) {
	return ix.syncClaims(ctx, ix.pool, st)
}

func (ix *Index) syncClaims(ctx context.Context, q querier, st *store.Store) (ClaimStats, error) {
	var stats ClaimStats
	pending, waiting, err := pendingExtractions(ctx, q, st)
	if err != nil {
		return stats, err
	}
	stats.Waiting = waiting
	var touched []int64
	for _, pf := range pending {
		f, todo := pf.file, pf.docs
		e, err := store.ReadExtractionFile(f)
		if err == nil && e.TextSHA != f.TextSHA {
			err = fmt.Errorf("file is for text %s", e.TextSHA)
		}
		var text string
		if err == nil {
			text, err = st.ReadText(f.TextSHA)
		}
		for _, d := range todo {
			if err == nil {
				var n, verified int
				n, verified, err = ix.ingestExtraction(ctx, q, st, d.id, text, e, f)
				stats.Claims += n
				stats.Verified += verified
			}
			if err != nil {
				if stats.Failed == nil {
					stats.Failed = map[string]string{}
				}
				stats.Failed[f.Path] = err.Error()
				break
			}
			stats.Extractions++
			touched = append(touched, d.id)
		}
	}
	if len(touched) > 0 {
		tag, err := q.Exec(ctx, ruleLinks, touched, MinKeyWords)
		if err != nil {
			return stats, fmt.Errorf("linking claims that quote the same words: %w", err)
		}
		stats.RuleLinks = tag.RowsAffected()
	}

	n, bad, err := ix.readLinkLog(ctx, q, st)
	stats.Links, stats.BadLinks = n, bad
	if err != nil {
		return stats, err
	}
	stale := len(touched) > 0 || n > 0
	if !stale {
		if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM documents d LEFT JOIN origins o ON o.document_id = d.id
			WHERE d.origin = 'primary' AND o.document_id IS NULL)`).Scan(&stale); err != nil {
			return stats, err
		}
	}
	if stale {
		os, err := ix.groupOrigins(ctx, q, st)
		if err != nil {
			return stats, fmt.Errorf("grouping origins: %w", err)
		}
		stats.Origins = &os
	}
	return stats, nil
}

type docRef struct {
	id  int64
	sha string
}

// pendingExtraction is an extraction file and the documents with its text
// that don't have it indexed as it is.
type pendingExtraction struct {
	file store.ExtractionFile
	docs []docRef
}

// pendingExtractions lists the store's extraction files that are new or
// changed for a primary document, and counts those for a text no document
// in the index has.
func pendingExtractions(ctx context.Context, q querier, st *store.Store) ([]pendingExtraction, int, error) {
	bySHA := map[string][]docRef{}
	rows, err := q.Query(ctx, `SELECT d.id, d.sha256, coalesce(e.extractor_dir, ''), coalesce(e.file_bytes, -1)
		FROM documents d LEFT JOIN extractions e ON e.document_id = d.id WHERE d.origin = 'primary'`)
	if err != nil {
		return nil, 0, fmt.Errorf("listing documents: %w", err)
	}
	type key struct {
		doc int64
		dir string
	}
	indexed := map[key]int64{}
	listed := map[int64]bool{}
	for rows.Next() {
		var id, size int64
		var sha, dir string
		if err := rows.Scan(&id, &sha, &dir, &size); err != nil {
			rows.Close()
			return nil, 0, err
		}
		if !listed[id] {
			listed[id] = true
			bySHA[sha] = append(bySHA[sha], docRef{id: id, sha: sha})
		}
		if dir != "" {
			indexed[key{id, dir}] = size
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	files, err := st.Extractions()
	if err != nil {
		return nil, 0, fmt.Errorf("listing extractions: %w", err)
	}
	var out []pendingExtraction
	waiting := 0
	for _, f := range files {
		docs := bySHA[f.TextSHA]
		if len(docs) == 0 {
			waiting++
			continue
		}
		var todo []docRef
		for _, d := range docs {
			if size, ok := indexed[key{d.id, f.Dir}]; !ok || size != f.Size {
				todo = append(todo, d)
			}
		}
		if len(todo) > 0 {
			out = append(out, pendingExtraction{file: f, docs: todo})
		}
	}
	return out, waiting, nil
}

// PendingExtractions counts the store's extraction files the index doesn't
// have as they are, and those for a text no indexed document has.
func (ix *Index) PendingExtractions(ctx context.Context, st *store.Store) (pending, waiting int, err error) {
	files, waiting, err := pendingExtractions(ctx, ix.pool, st)
	return len(files), waiting, err
}

// ClaimCounts counts the index's claims and the work waiting on them.
type ClaimCounts struct {
	Claims int64 `json:"claims"`
	// Unverified claims have a quote that wasn't found in their document.
	Unverified int64 `json:"unverified"`
	// Unembedded claims lack a vector from the index's embedding model.
	Unembedded int64 `json:"unembedded"`
	// Unlinked are verified, embedded claims the linker hasn't checked.
	Unlinked   int64 `json:"unlinked"`
	Links      int64 `json:"links"`
	ModelLinks int64 `json:"model_links"`
}

// ClaimCounts counts claims and claim links.
func (ix *Index) ClaimCounts(ctx context.Context) (ClaimCounts, error) {
	var c ClaimCounts
	err := ix.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM claims), (SELECT count(*) FROM claims WHERE NOT quote_verified),
		(SELECT count(*) FROM claims WHERE `+unembedded+`),
		(SELECT count(*) FROM claims WHERE link_checked_at IS NULL AND quote_verified AND embedding IS NOT NULL),
		(SELECT count(*) FROM claim_links), (SELECT count(*) FROM claim_links WHERE method = 'model')`, ix.embedModel).
		Scan(&c.Claims, &c.Unverified, &c.Unembedded, &c.Unlinked, &c.Links, &c.ModelLinks)
	return c, err
}

// ruleLinks links verified claims in documents $1 to verified claims in
// other documents with the same quote key: `same`, by rule.
const ruleLinks = `INSERT INTO claim_links (from_claim, to_claim, relation, method, created_at)
	SELECT DISTINCT LEAST(a.id, b.id), GREATEST(a.id, b.id), 'same', 'rule', GREATEST(a.extracted_at, b.extracted_at)
	FROM claims a JOIN claims b ON b.quote_key = a.quote_key AND b.document_id <> a.document_id AND b.quote_verified
	WHERE a.quote_verified AND a.document_id = ANY($1)
	  AND array_length(string_to_array(a.quote_key, ' '), 1) >= $2
	ON CONFLICT DO NOTHING`

// ClaimID is a claim's id, derived from its document, its extractor, the
// chunk it came from and its place in the chunk, so a re-extraction that
// fills in a failed chunk keeps the other claims' ids.
func ClaimID(documentID int64, extractor string, chunk, ord int) int64 {
	return hash63(fmt.Sprintf("claim\x00%d\x00%s\x00%d\x00%d", documentID, extractor, chunk, ord))
}

// ingestExtraction replaces document docID's claims from extraction e,
// anchoring each quote in the document's text.
func (ix *Index) ingestExtraction(ctx context.Context, q querier, st *store.Store, docID int64, text string, e store.Extraction, f store.ExtractionFile) (claims, verified int, err error) {
	type span struct {
		id         int64
		start, end int
	}
	var passages []span
	rows, err := q.Query(ctx, `SELECT id, char_start, char_end FROM passages WHERE document_id = $1 ORDER BY ord`, docID)
	if err != nil {
		return 0, 0, err
	}
	for rows.Next() {
		var p span
		if err := rows.Scan(&p.id, &p.start, &p.end); err != nil {
			rows.Close()
			return 0, 0, err
		}
		passages = append(passages, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	runes := []rune(text)

	err = pgx.BeginFunc(ctx, q, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM claim_links WHERE method = 'rule' AND (from_claim IN (SELECT id FROM claims WHERE document_id = $1)
			OR to_claim IN (SELECT id FROM claims WHERE document_id = $1))`, docID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM claims WHERE document_id = $1 AND extractor = $2`, docID, e.Extractor); err != nil {
			return err
		}
		b := &pgx.Batch{}
		ords := map[int]int{}
		seen := map[int64]bool{}
		for _, c := range e.Claims {
			ord := ords[c.Chunk]
			ords[c.Chunk]++
			claimText := strings.TrimSpace(c.Text)
			if claimText == "" {
				continue
			}
			id := ClaimID(docID, e.Extractor, c.Chunk, ord)
			if seen[id] {
				continue
			}
			seen[id] = true
			var startCol, endCol, passage, key any
			start, end, ok := quote.Locate(c.Quote, text)
			if ok {
				verified++
				startCol, endCol = start, end
				for _, p := range passages {
					if start >= p.start && start < p.end {
						passage = p.id
						break
					}
				}
				if k := quote.Key(string(runes[start:end])); k != "" {
					key = k
				}
			}
			asOf, precision := asOfDate(c.AsOf)
			sha := store.HashText(claimText)
			var vec any
			var model any
			if ix.embedModel != "" {
				if v, ok, err := st.ReadVector(ix.embedModel, sha); err == nil && ok && len(v) == Dims {
					vec, model = pgvector.NewVector(v), ix.embedModel
				}
			}
			b.Queue(`INSERT INTO claims (id, document_id, passage_id, extractor, chunk, ord, text, quote, quote_verified,
				char_start, char_end, quote_key, as_of, as_of_precision, volatile, extracted_at, text_sha256, embedding, embed_model)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)`,
				id, docID, passage, e.Extractor, c.Chunk, ord, claimText, c.Quote, ok,
				startCol, endCol, key, asOf, precision, c.Volatile, e.ExtractedAt, sha, vec, model)
			claims++
		}
		b.Queue(`INSERT INTO extractions (document_id, extractor_dir, extractor, file_bytes, complete, claims, extracted_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (document_id, extractor_dir) DO UPDATE SET extractor = EXCLUDED.extractor, file_bytes = EXCLUDED.file_bytes,
				complete = EXCLUDED.complete, claims = EXCLUDED.claims, extracted_at = EXCLUDED.extracted_at`,
			docID, f.Dir, e.Extractor, f.Size, e.Complete(), claims, e.ExtractedAt)
		return tx.SendBatch(ctx, b).Close()
	})
	if err != nil {
		return 0, 0, err
	}
	return claims, verified, nil
}

var isoAsOf = regexp.MustCompile(`^(\d{4})(?:-(\d{2})(?:-(\d{2}))?)?$`)

// asOfDate reads an extractor's as_of: YYYY, YYYY-MM or YYYY-MM-DD.
// Anything else is no date.
func asOfDate(s string) (any, any) {
	m := isoAsOf.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return nil, nil
	}
	layout, precision, v := "2006", "year", m[1]
	switch {
	case m[3] != "":
		layout, precision, v = "2006-01-02", "day", m[0]
	case m[2] != "":
		layout, precision, v = "2006-01", "month", m[0]
	}
	t, err := time.Parse(layout, v)
	if err != nil {
		return nil, nil
	}
	return t, precision
}

// readLinkLog upserts the link log's lines since the last read. A log
// shorter than what was read was replaced, and is read from the start.
func (ix *Index) readLinkLog(ctx context.Context, q querier, st *store.Store) (int, []int, error) {
	var offset int64
	err := q.QueryRow(ctx, `SELECT value FROM store_state WHERE key = 'links_offset'`).Scan(&offset)
	if err != nil && err != pgx.ErrNoRows {
		return 0, nil, err
	}
	if st.LinksSize() < offset {
		offset = 0
	}
	log, err := st.ReadLinks(offset)
	if err != nil {
		return 0, nil, fmt.Errorf("reading the link log: %w", err)
	}
	if log.Size == offset {
		return 0, log.BadLines, nil
	}
	b := &pgx.Batch{}
	for _, l := range log.Links {
		b.Queue(`INSERT INTO claim_links (from_claim, to_claim, relation, method, model, confidence, note, created_at)
			VALUES ($1, $2, $3, $4, nullif($5, ''), nullif($6, 0::real), nullif($7, ''), $8)
			ON CONFLICT (from_claim, to_claim, method) DO UPDATE SET relation = EXCLUDED.relation, model = EXCLUDED.model,
				confidence = EXCLUDED.confidence, note = EXCLUDED.note, created_at = EXCLUDED.created_at
			WHERE EXCLUDED.created_at >= claim_links.created_at`,
			l.From, l.To, l.Relation, l.Method, l.Model, float32(l.Confidence), l.Note, l.CreatedAt)
	}
	b.Queue(`INSERT INTO store_state (key, value) VALUES ('links_offset', $1)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, log.Size)
	if err := q.SendBatch(ctx, b).Close(); err != nil {
		return 0, log.BadLines, fmt.Errorf("indexing links: %w", err)
	}
	return len(log.Links), log.BadLines, nil
}

// ClaimCandidate is a text waiting for claims: primary documents with
// this text have no complete extraction in the index.
type ClaimCandidate struct {
	TextSHA string
	Title   string
	Chars   int
	Cited   bool // a report cites one of its documents
}

// MinClaimChars is the shortest text worth extracting claims from.
const MinClaimChars = 200

// ClaimCandidates lists the texts of primary documents with no complete
// extraction from extractorDir, one per text: texts a report cites first,
// then the most recently fetched.
func (ix *Index) ClaimCandidates(ctx context.Context, extractorDir string) ([]ClaimCandidate, error) {
	rows, err := ix.pool.Query(ctx, `WITH cited AS (
			SELECT p.document_id AS id FROM citations c JOIN passages p ON p.id::text = c.target_id WHERE c.target_kind = 'passage'
			UNION SELECT d.id FROM citations c JOIN documents d ON d.source_id::text = c.target_id WHERE c.target_kind = 'source')
		SELECT d.sha256, max(d.title), max(d.text_chars), bool_or(d.id IN (SELECT id FROM cited)) AS cited, max(d.last_fetched_at) AS fetched
		FROM documents d
		WHERE d.origin = 'primary' AND d.text_chars >= $2
		  AND NOT EXISTS (SELECT 1 FROM extractions e WHERE e.document_id = d.id AND e.extractor_dir = $1 AND e.complete)
		GROUP BY d.sha256
		ORDER BY cited DESC, fetched DESC, d.sha256`, extractorDir, MinClaimChars)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ClaimCandidate
	for rows.Next() {
		var c ClaimCandidate
		var fetched time.Time
		if err := rows.Scan(&c.TextSHA, &c.Title, &c.Chars, &c.Cited, &fetched); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
