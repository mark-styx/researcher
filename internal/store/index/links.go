package index

import (
	"context"
	"time"
)

// LinkClaim is a claim as the linker shows it to a model: what it says,
// where it's from and when it's true as of.
type LinkClaim struct {
	ID        int64
	Document  int64
	Origin    int64
	Text      string
	Quote     string
	AsOf      string // YYYY, YYYY-MM or YYYY-MM-DD; blank when the document doesn't say
	Title     string
	Published string
	// Similarity is the cosine similarity to the claim it was found near;
	// zero for the claim itself.
	Similarity float64
}

// linkClaimCols are the columns scanLinkClaim reads, for claim c,
// document d and origin o.
const linkClaimCols = `c.id, c.document_id, o.origin_id, c.text, c.quote, c.as_of, c.as_of_precision,
	d.title, d.published_at, d.published_precision`

func scanLinkClaim(row interface{ Scan(...any) error }, extra ...any) (LinkClaim, error) {
	var c LinkClaim
	var asOf, published *time.Time
	var asOfP, publishedP *string
	err := row.Scan(append([]any{&c.ID, &c.Document, &c.Origin, &c.Text, &c.Quote, &asOf, &asOfP,
		&c.Title, &published, &publishedP}, extra...)...)
	c.AsOf = dateAt(asOf, asOfP)
	c.Published = dateAt(published, publishedP)
	return c, err
}

// dateAt formats a date to its precision: 2024, 2024-03 or 2024-03-09.
func dateAt(t *time.Time, precision *string) string {
	if t == nil {
		return ""
	}
	p := "day"
	if precision != nil {
		p = *precision
	}
	switch p {
	case "year":
		return t.Format("2006")
	case "month":
		return t.Format("2006-01")
	}
	return t.Format("2006-01-02")
}

// UncheckedClaims returns up to limit claims the linker hasn't compared
// with their nearest claims yet, newest first. Only claims with a found
// quote and a vector are linked.
func (ix *Index) UncheckedClaims(ctx context.Context, limit int) ([]LinkClaim, error) {
	rows, err := ix.pool.Query(ctx, `SELECT `+linkClaimCols+`
		FROM claims c JOIN documents d ON d.id = c.document_id JOIN origins o ON o.document_id = c.document_id
		WHERE c.link_checked_at IS NULL AND c.quote_verified AND c.embedding IS NOT NULL
		ORDER BY c.extracted_at DESC, c.id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LinkClaim
	for rows.Next() {
		c, err := scanLinkClaim(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// NearestClaims returns up to k claims nearest claim id, embedded with the
// same model, from other origins, at least minSimilarity close, and with
// no link to it either way, nearest first.
func (ix *Index) NearestClaims(ctx context.Context, id int64, k int, minSimilarity float64) ([]LinkClaim, error) {
	rows, err := ix.pool.Query(ctx, `SELECT `+linkClaimCols+`, 1 - (c.embedding <=> a.embedding) AS sim
		FROM claims a
		JOIN origins oa ON oa.document_id = a.document_id
		CROSS JOIN LATERAL (
			SELECT * FROM claims b
			WHERE b.id <> a.id AND b.quote_verified AND b.embedding IS NOT NULL AND b.embed_model = a.embed_model
			ORDER BY b.embedding <=> a.embedding LIMIT $2 * 4) c
		JOIN origins o ON o.document_id = c.document_id
		JOIN documents d ON d.id = c.document_id
		WHERE a.id = $1 AND o.origin_id <> oa.origin_id AND 1 - (c.embedding <=> a.embedding) >= $3
		  AND NOT EXISTS (SELECT 1 FROM claim_links l
			WHERE (l.from_claim = a.id AND l.to_claim = c.id) OR (l.from_claim = c.id AND l.to_claim = a.id))
		ORDER BY sim DESC, c.id LIMIT $2`, id, k, minSimilarity)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LinkClaim
	for rows.Next() {
		var sim float64
		c, err := scanLinkClaim(rows, &sim)
		if err != nil {
			return nil, err
		}
		c.Similarity = sim
		out = append(out, c)
	}
	return out, rows.Err()
}

// MarkLinkChecked records that the linker compared claims ids with their
// nearest claims.
func (ix *Index) MarkLinkChecked(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := ix.pool.Exec(ctx, `UPDATE claims SET link_checked_at = now() WHERE id = ANY($1)`, ids)
	return err
}

// ModelLinksSince counts the model links made since t, for the linker's
// daily cap.
func (ix *Index) ModelLinksSince(ctx context.Context, t time.Time) (int, error) {
	var n int
	err := ix.pool.QueryRow(ctx, `SELECT count(*) FROM claim_links WHERE method = 'model' AND created_at >= $1`, t).Scan(&n)
	return n, err
}

// ClaimsExist returns which of ids are indexed claims.
func (ix *Index) ClaimsExist(ctx context.Context, ids []int64) (map[int64]bool, error) {
	rows, err := ix.pool.Query(ctx, `SELECT id FROM claims WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
