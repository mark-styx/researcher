package index

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/marklubin/researchguy/internal/graph"
	"github.com/marklubin/researchguy/internal/store"
	"github.com/pgvector/pgvector-go"
)

// Dims is the size of the index's vectors (passages.embedding), what
// nomic-embed-text produces.
const Dims = 768

// SetEmbedModel names the embedding model whose cached vectors ingest
// attaches to new passages. Blank leaves new passages for Embed.
func (ix *Index) SetEmbedModel(model string) { ix.embedModel = model }

// hash63 is the first 63 bits of the SHA-256 of s.
func hash63(s string) int64 {
	sum := sha256.Sum256([]byte(s))
	return int64(binary.BigEndian.Uint64(sum[:8]) &^ (1 << 63))
}

// DocumentID is a document's id, derived from its source's url_key and
// its text's hash.
func DocumentID(urlKey, textSHA string) int64 {
	return hash63("document\x00" + urlKey + "\x00" + textSHA)
}

// PassageID is a passage's id, derived from its document and span.
func PassageID(documentID int64, start, end int) int64 {
	return hash63(fmt.Sprintf("passage\x00%d\x00%d\x00%d", documentID, start, end))
}

// addFetchSources adds the sources fetch records name to sources: a cited
// URL can be in no capture.
func addFetchSources(sources map[string]*sourceRow, recs []store.FetchRecord) {
	for _, r := range recs {
		key, err := graph.NormalizeURL(r.URL)
		if err != nil {
			continue
		}
		row, ok := sources[key]
		if !ok {
			row = &sourceRow{url: strings.TrimSpace(r.URL), domain: domainOf(key), doi: doiOf(r.URL), first: r.AttemptedAt, last: r.AttemptedAt}
			sources[key] = row
		}
		if row.title == "" {
			row.title = r.Title
		}
		if row.doi == nil && r.DOI != "" {
			doi := r.DOI
			row.doi = &doi
		}
		if r.AttemptedAt.Before(row.first) {
			row.first = r.AttemptedAt
		}
		if r.AttemptedAt.After(row.last) {
			row.last = r.AttemptedAt
		}
	}
}

// sourceKind classifies a source from its URL and DOI. Anything not
// recognized is web.
func sourceKind(key string, doi *string) string {
	domain := domainOf(key)
	switch {
	case doi != nil:
		return "paper"
	case strings.HasSuffix(domain, "youtube.com") || domain == "youtu.be" || strings.HasSuffix(domain, "vimeo.com"):
		return "video"
	case strings.HasSuffix(domain, "courtlistener.com") || strings.HasSuffix(domain, "supremecourt.gov") || strings.HasSuffix(domain, "uscourts.gov"):
		return "court"
	case domain == "arxiv.org" || domain == "pubmed.ncbi.nlm.nih.gov" || strings.HasPrefix(key, "ncbi.nlm.nih.gov/pmc/") || domain == "pmc.ncbi.nlm.nih.gov":
		return "paper"
	}
	return "web"
}

type documentRow struct {
	id, sourceID int64
	sha          string
	kind         string
	contentType  string
	title        string
	chars        int
	first, last  time.Time
	pub          *store.Published
}

// ingestFetches writes a run's fetch records, the documents they got, and
// the passages of documents new to the index.
func (ix *Index) ingestFetches(ctx context.Context, tx pgx.Tx, snap snapshot, stats *RunStats) error {
	recs := snap.fetches.Records
	if len(recs) == 0 {
		return nil
	}
	docs := map[int64]*documentRow{}
	for _, r := range recs {
		if r.TextSHA256 == "" {
			continue
		}
		key, err := graph.NormalizeURL(r.URL)
		if err != nil {
			continue
		}
		id := DocumentID(key, r.TextSHA256)
		d, ok := docs[id]
		if !ok {
			kind := r.ContentKind
			if kind != store.KindAbstract {
				kind = store.KindFull
			}
			d = &documentRow{id: id, sourceID: SourceID(key), sha: r.TextSHA256, kind: kind, contentType: r.ContentType,
				chars: r.TextChars, first: r.AttemptedAt, last: r.AttemptedAt}
			docs[id] = d
		}
		if d.title == "" {
			d.title = r.Title
		}
		if r.Published != nil && (d.pub == nil || (d.pub.Weak && !r.Published.Weak)) {
			d.pub = r.Published
		}
		if r.AttemptedAt.Before(d.first) {
			d.first = r.AttemptedAt
		}
		if r.AttemptedAt.After(d.last) {
			d.last = r.AttemptedAt
		}
	}
	ids := make([]int64, 0, len(docs))
	for id := range docs {
		ids = append(ids, id)
	}
	slices.Sort(ids)

	// known documents are in the index already; split ones have passages
	// (a document whose text was missing when it was ingested has none yet).
	known, split := map[int64]bool{}, map[int64]bool{}
	if len(ids) > 0 {
		rows, err := tx.Query(ctx, `SELECT d.id, EXISTS (SELECT 1 FROM passages p WHERE p.document_id = d.id)
			FROM documents d WHERE d.id = ANY($1)`, ids)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id int64
			var hasPassages bool
			if err := rows.Scan(&id, &hasPassages); err != nil {
				rows.Close()
				return err
			}
			known[id], split[id] = true, hasPassages
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}

	var q []queued
	for _, id := range ids {
		d := docs[id]
		at, precision, from, weak := publishedCols(d.pub)
		q = append(q, queued{`
			INSERT INTO documents (id, source_id, sha256, content_kind, content_type, title, text_chars,
				first_fetched_at, last_fetched_at, published_at, published_precision, published_from, published_weak)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
			ON CONFLICT (id) DO UPDATE SET
				title = CASE WHEN documents.title = '' THEN EXCLUDED.title ELSE documents.title END,
				first_fetched_at = LEAST(documents.first_fetched_at, EXCLUDED.first_fetched_at),
				last_fetched_at = GREATEST(documents.last_fetched_at, EXCLUDED.last_fetched_at),
				published_at = CASE WHEN ` + betterDate + ` THEN EXCLUDED.published_at ELSE documents.published_at END,
				published_precision = CASE WHEN ` + betterDate + ` THEN EXCLUDED.published_precision ELSE documents.published_precision END,
				published_from = CASE WHEN ` + betterDate + ` THEN EXCLUDED.published_from ELSE documents.published_from END,
				published_weak = CASE WHEN ` + betterDate + ` THEN EXCLUDED.published_weak ELSE documents.published_weak END`,
			[]any{d.id, d.sourceID, d.sha, d.kind, d.contentType, d.title, d.chars, d.first, d.last, at, precision, from, weak}})
		if !known[id] {
			stats.Documents++
		}
	}
	for _, r := range recs {
		key, err := graph.NormalizeURL(r.URL)
		if err != nil {
			continue
		}
		var docID any
		if r.TextSHA256 != "" {
			docID = DocumentID(key, r.TextSHA256)
		}
		fetchURL := r.FetchURL
		if fetchURL == "" {
			fetchURL = r.URL
		}
		var status, rank, rawBytes any
		if r.HTTPStatus != 0 {
			status = r.HTTPStatus
		}
		if r.Rank > 0 {
			rank = r.Rank
		}
		var rawSHA any
		if r.RawSHA256 != "" {
			rawSHA, rawBytes = r.RawSHA256, r.RawBytes
		}
		q = append(q, queued{`
			INSERT INTO fetches (run_id, seq, source_id, reason, rank, via, fetch_url, attempted_at, attempts,
				duration_ms, http_status, final_url, content_type, error, raw_sha256, raw_bytes, document_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
			ON CONFLICT (run_id, seq) DO NOTHING`,
			[]any{snap.rec.ID, r.Seq, SourceID(key), r.Reason, rank, r.Via, fetchURL, r.AttemptedAt, max(r.Attempts, 1),
				r.DurationMS, status, r.FinalURL, r.ContentType, r.Error, rawSHA, rawBytes, docID}})
	}
	for _, id := range ids {
		if split[id] {
			continue
		}
		d := docs[id]
		text, err := snap.store.ReadText(d.sha)
		if err != nil {
			stats.MissingTexts++
			continue
		}
		for _, p := range store.Passages(text) {
			sha := store.HashText(p.Text)
			var vec, model any
			if ix.embedModel != "" {
				if v, ok, err := snap.store.ReadVector(ix.embedModel, sha); err == nil && ok && len(v) == Dims {
					vec, model = pgvector.NewVector(v), ix.embedModel
					stats.Embedded++
				}
			}
			q = append(q, queued{`
				INSERT INTO passages (id, document_id, ord, char_start, char_end, text, text_sha256, embedding, embed_model)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
				ON CONFLICT (id) DO NOTHING`,
				[]any{PassageID(id, p.Start, p.End), id, p.Ord, p.Start, p.End, p.Text, sha, vec, model}})
			stats.Passages++
		}
	}
	return sendAll(ctx, tx, q)
}

// betterDate is true when a document's incoming publication date should
// replace the one it has: it had none, or a weak one and this isn't.
const betterDate = `(EXCLUDED.published_at IS NOT NULL AND (documents.published_at IS NULL OR (documents.published_weak AND NOT EXCLUDED.published_weak)))`

// publishedCols turns a publication date into its documents columns. A
// year or month is stored as its first day; the precision says which.
func publishedCols(p *store.Published) (at, precision, from any, weak bool) {
	if p == nil {
		return nil, nil, nil, false
	}
	layout := map[string]string{"year": "2006", "month": "2006-01", "day": "2006-01-02"}[p.Precision]
	if layout == "" {
		return nil, nil, nil, false
	}
	t, err := time.Parse(layout, p.Date)
	if err != nil {
		return nil, nil, nil, false
	}
	return t, p.Precision, p.From, p.Weak
}
