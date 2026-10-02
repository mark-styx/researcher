package index

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/marklubin/researchguy/internal/store"
	"github.com/pgvector/pgvector-go"
)

// unembedded matches passages without a vector from model $1, or without
// any vector when $1 is blank.
const unembedded = `(embedding IS NULL OR ($1 <> '' AND embed_model IS DISTINCT FROM $1))`

// Embedder is what Embed needs from an embedding model (internal/embed).
type Embedder interface {
	Model() string
	EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error)
}

// EmbedStats is what Embed did.
type EmbedStats struct {
	Model     string `json:"model"`
	Embedded  int    `json:"embedded"`   // vectors the model made
	FromCache int    `json:"from_cache"` // vectors read from the store's cache
	Remaining int64  `json:"remaining"`
}

// embedBatch is how many passages one Embed round takes.
const embedBatch = 64

// Embed gives every passage without one a vector from emb: the store's
// cached vector when there is one, otherwise the model's, which is then
// cached so a rebuild doesn't call the model again. Passages embedded with
// another model are re-embedded after all the unembedded ones. It stops at
// the first model error or when ctx ends, and whatever it finished stays.
// Concurrent calls take different passages.
func (ix *Index) Embed(ctx context.Context, st *store.Store, emb Embedder) (EmbedStats, error) {
	return ix.embed(ctx, st, emb, nil)
}

// EmbedDocuments is Embed for the passages of the given documents only,
// for a caller waiting on just those. Remaining still counts the whole
// index.
func (ix *Index) EmbedDocuments(ctx context.Context, st *store.Store, emb Embedder, docIDs []int64) (EmbedStats, error) {
	if docIDs == nil {
		docIDs = []int64{}
	}
	return ix.embed(ctx, st, emb, docIDs)
}

// EmbedClaims is Embed for claims: their text, so a claim's nearest
// claims can be found.
func (ix *Index) EmbedClaims(ctx context.Context, st *store.Store, emb Embedder) (EmbedStats, error) {
	return ix.embedTable(ctx, st, emb, "claims", nil)
}

// embed embeds the passages of docIDs, or of every document when docIDs
// is nil.
func (ix *Index) embed(ctx context.Context, st *store.Store, emb Embedder, docIDs []int64) (EmbedStats, error) {
	return ix.embedTable(ctx, st, emb, "passages", docIDs)
}

// embedTable embeds the rows of table (passages or claims, which share
// text, text_sha256, embedding and embed_model) in documents docIDs, or in
// every document when docIDs is nil.
func (ix *Index) embedTable(ctx context.Context, st *store.Store, emb Embedder, table string, docIDs []int64) (EmbedStats, error) {
	model := emb.Model()
	stats := EmbedStats{Model: model}
	var err error
	scope := `($2::bigint[] IS NULL OR document_id = ANY($2))`
	passes := []struct {
		query string
		args  []any
	}{
		{`SELECT id, text, text_sha256 FROM ` + table + ` WHERE embedding IS NULL AND ` + scope + `
			ORDER BY id LIMIT $1 FOR UPDATE SKIP LOCKED`, []any{embedBatch, docIDs}},
		{`SELECT id, text, text_sha256 FROM ` + table + ` WHERE embed_model IS DISTINCT FROM $3 AND ` + scope + `
			ORDER BY id LIMIT $1 FOR UPDATE SKIP LOCKED`, []any{embedBatch, docIDs, model}},
	}
	for _, pass := range passes {
		for {
			var n int
			n, err = ix.embedRound(ctx, st, emb, table, pass.query, pass.args, &stats)
			if err != nil || n == 0 {
				break
			}
		}
		if err != nil {
			break
		}
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if qerr := ix.pool.QueryRow(cctx, `SELECT count(*) FROM `+table+` WHERE `+unembedded, model).Scan(&stats.Remaining); qerr != nil && err == nil {
		err = qerr
	}
	return stats, err
}

// embedRound embeds one batch of the rows query selects from table,
// locked so a concurrent Embed takes others. It returns how many it
// embedded.
func (ix *Index) embedRound(ctx context.Context, st *store.Store, emb Embedder, table, query string, args []any, stats *EmbedStats) (int, error) {
	model := emb.Model()
	n, cached, made := 0, 0, 0
	err := pgx.BeginFunc(ctx, ix.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return err
		}
		type pending struct {
			id        int64
			text, sha string
			vec       []float32
		}
		var ps []pending
		for rows.Next() {
			var p pending
			if err := rows.Scan(&p.id, &p.text, &p.sha); err != nil {
				rows.Close()
				return err
			}
			ps = append(ps, p)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(ps) == 0 {
			return nil
		}
		var texts []string
		var missing []int
		for i := range ps {
			v, ok, err := st.ReadVector(model, ps[i].sha)
			if err == nil && ok && len(v) == Dims {
				ps[i].vec = v
				cached++
				continue
			}
			texts = append(texts, ps[i].text)
			missing = append(missing, i)
		}
		if len(texts) > 0 {
			vs, err := emb.EmbedDocuments(ctx, texts)
			if err != nil {
				return err
			}
			if len(vs) != len(texts) {
				return fmt.Errorf("embedding model %s returned %d vectors for %d texts", model, len(vs), len(texts))
			}
			for j, v := range vs {
				if len(v) != Dims {
					return fmt.Errorf("embedding model %s makes %d-dimension vectors; the index holds %d", model, len(v), Dims)
				}
				p := &ps[missing[j]]
				p.vec = v
				if err := st.PutVector(model, p.sha, v); err != nil {
					return fmt.Errorf("caching vector: %w", err)
				}
				made++
			}
		}
		b := &pgx.Batch{}
		for _, p := range ps {
			b.Queue(`UPDATE `+table+` SET embedding = $2, embed_model = $3 WHERE id = $1`, p.id, pgvector.NewVector(p.vec), model)
		}
		if err := tx.SendBatch(ctx, b).Close(); err != nil {
			return err
		}
		n = len(ps)
		return nil
	})
	if err == nil {
		stats.FromCache += cached
		stats.Embedded += made
	}
	return n, err
}

// Unembedded counts passages without a vector from model (any vector
// when model is blank).
func (ix *Index) Unembedded(ctx context.Context, model string) (int64, error) {
	var n int64
	err := ix.pool.QueryRow(ctx, `SELECT count(*) FROM passages WHERE `+unembedded, model).Scan(&n)
	return n, err
}
