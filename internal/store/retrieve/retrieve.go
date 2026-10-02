// Package retrieve answers queries over the store index: ranked evidence
// passages with their sources and dates, and lookups of one passage,
// document or source. It never calls a language model; the only model it
// uses is the embedder, for the query.
package retrieve

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/marklubin/researchguy/internal/store"
	"github.com/marklubin/researchguy/internal/store/index"
	"github.com/pgvector/pgvector-go"
)

// QueryEmbedder embeds a search query (internal/embed).
type QueryEmbedder interface {
	Model() string
	EmbedQuery(ctx context.Context, text string) ([]float32, error)
}

// Retriever queries an index. Store is where document text is read from;
// Embed may be nil, which makes every query full-text only.
type Retriever struct {
	Index *index.Index
	Store *store.Store
	Embed QueryEmbedder
	// Now is the clock age labels and recency weighting use; nil is
	// time.Now.
	Now func() time.Time
	// VolatileMaxAge is how old a volatile claim's date can be before it's
	// flagged possibly outdated (default 30 days). VolatileHalfLife is the
	// recency weighting volatile claims get when a query sets none
	// (default a year).
	VolatileMaxAge   time.Duration
	VolatileHalfLife time.Duration
}

func (r *Retriever) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

// Kinds of evidence card.
const (
	KindPassage = "passage" // from a fetched document: primary evidence
	KindReport  = "report"  // from a research report: synthesis, never primary evidence
	// KindClaim is one checkable assertion extracted from a fetched
	// document, with the words in the document that state it.
	KindClaim = "claim"
)

// Date fields a query can filter on.
const (
	DatePublished = "published" // when the source says it was published
	DateCollected = "collected" // when researchguy first fetched it
)

// Query is a find request. Only Text is required.
type Query struct {
	Text  string   `json:"query"`
	Kinds []string `json:"kinds,omitempty"` // KindPassage, KindReport, KindClaim; empty is all three
	// Since and Until bound DateField (default published). A published
	// bound leaves out undated documents.
	Since     *time.Time `json:"since,omitempty"`
	Until     *time.Time `json:"until,omitempty"`
	DateField string     `json:"date_field,omitempty"`
	RunID     string     `json:"run_id,omitempty"` // documents this run fetched, or its report
	Domain    string     `json:"domain,omitempty"` // the domain or a subdomain of it
	// AsOf keeps only what had been collected by then: what an earlier
	// report could have seen.
	AsOf *time.Time `json:"as_of,omitempty"`
	// PreferRecent halves a card's score every PreferRecent of age (from
	// its published date, else its collection date). Zero is off: an old
	// primary source isn't worse evidence for being old. Volatile claims
	// get Retriever.VolatileHalfLife when it's zero.
	PreferRecent time.Duration `json:"prefer_recent,omitempty"`
	// MinSimilarity drops vector matches less similar than this (cosine,
	// 0-1). Zero keeps the nearest whatever their distance, so a query
	// nothing matches still returns its nearest passages, with their
	// similarity to judge them by.
	MinSimilarity float64 `json:"min_similarity,omitempty"`
	Limit         int     `json:"limit,omitempty"` // default 10, at most 100
}

// Date is a document's publication date as the index holds it.
type Date struct {
	Date      string `json:"date"` // YYYY, YYYY-MM or YYYY-MM-DD, per Precision
	Precision string `json:"precision"`
	From      string `json:"from"`
	Weak      bool   `json:"weak,omitempty"`
}

// Card is one ranked piece of evidence.
type Card struct {
	// Ref is what a report cites: P:<passage id>, or C:<claim id> for a
	// claim.
	Ref  string `json:"ref"`
	Kind string `json:"kind"`
	// A claim's id, what it says (Text), whether its quote was found in the
	// document and the quote as the document has it. A claim whose quote
	// wasn't found has no Quote and is never shown as quoted.
	ClaimID       int64  `json:"claim_id,omitempty"`
	QuoteVerified bool   `json:"quote_verified,omitempty"`
	Quote         string `json:"quote,omitempty"`
	// AsOf is when the document says the claim is true as of; Volatile is
	// a claim whose truth changes with time.
	AsOf     string `json:"as_of,omitempty"`
	Volatile bool   `json:"volatile,omitempty"`
	// Flags and Cluster say how other claims bear on a claim (see
	// Cluster).
	Flags   []string `json:"flags,omitempty"`
	Cluster *Cluster `json:"cluster,omitempty"`
	// PassageID is the passage, or for a claim the passage its quote
	// starts in (0 when none).
	PassageID   int64     `json:"passage_id"`
	DocumentID  int64     `json:"document_id"`
	SourceID    int64     `json:"source_id"`
	Text        string    `json:"text"`
	CharStart   int       `json:"char_start"`
	CharEnd     int       `json:"char_end"`
	URL         string    `json:"url"`
	Domain      string    `json:"domain"`
	Title       string    `json:"title,omitempty"`
	SourceKind  string    `json:"source_kind"`
	DOI         string    `json:"doi,omitempty"`
	ContentKind string    `json:"content_kind"` // full, or abstract (abstract-only source)
	Published   *Date     `json:"published,omitempty"`
	Collected   time.Time `json:"collected"`
	LastFetched time.Time `json:"last_fetched"`
	// Runs are the latest runs that fetched the document, or whose report
	// it is.
	Runs []string `json:"runs,omitempty"`
	// Age says how old the evidence is: its publication date when known,
	// otherwise that it's undated and when it was collected.
	Age        string  `json:"age"`
	Score      float64 `json:"score"`
	TextRank   int     `json:"text_rank,omitempty"`   // rank in the full-text arm, 1-based
	VectorRank int     `json:"vector_rank,omitempty"` // rank in the vector arm, 1-based
	// Similarity is the vector match's cosine similarity to the query.
	Similarity float64 `json:"similarity,omitempty"`
}

// Search modes.
const (
	ModeHybrid   = "hybrid"    // full-text and vector rankings fused
	ModeFullText = "full_text" // no query vector: the embedder is off or down
)

// Result is a find answer.
type Result struct {
	Query      string `json:"query"`
	Mode       string `json:"mode"`
	EmbedModel string `json:"embed_model,omitempty"`
	// Note says what limited the search, such as the embedder being down.
	Note   string `json:"note,omitempty"`
	Cards  []Card `json:"cards"`
	TookMS int64  `json:"took_ms"`
}

// queryEmbedTimeout caps embedding the query.
const queryEmbedTimeout = 30 * time.Second

// rrfK is reciprocal rank fusion's constant: a card's score is the sum of
// 1/(rrfK + rank) over the rankings it appears in.
const rrfK = 60

// perDocument caps the cards one document contributes, so one long
// document doesn't fill the answer.
const perDocument = 2

// ErrEmptyQuery is returned for a query with no text.
var ErrEmptyQuery = errors.New("empty query")

// Find ranks passages for q: a full-text ranking and, when the query can
// be embedded, a vector ranking over the same filters, fused with
// reciprocal rank fusion. Nothing is dropped for age unless q asks for it.
func (r *Retriever) Find(ctx context.Context, q Query) (Result, error) {
	started := time.Now()
	q.Text = strings.TrimSpace(q.Text)
	if q.Text == "" {
		return Result{}, ErrEmptyQuery
	}
	if err := q.check(); err != nil {
		return Result{}, err
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 10
	}
	limit = min(limit, 100)
	res := Result{Query: q.Text, Mode: ModeFullText, Cards: []Card{}}

	var vec []float32
	if r.Embed != nil {
		res.EmbedModel = r.Embed.Model()
		// A cold model takes a few seconds to load; a dead one shouldn't
		// hold the search up longer than that.
		ectx, cancel := context.WithTimeout(ctx, queryEmbedTimeout)
		v, err := r.Embed.EmbedQuery(ectx, q.Text)
		cancel()
		switch {
		case err != nil:
			res.Note = fmt.Sprintf("full-text only: the embedding model is unavailable (%v)", err)
		case len(v) != index.Dims:
			res.Note = fmt.Sprintf("full-text only: %s makes %d-dimension vectors, the index holds %d", res.EmbedModel, len(v), index.Dims)
		default:
			vec, res.Mode = v, ModeHybrid
		}
	} else {
		res.Note = "full-text only: no embedding model configured"
	}

	k := max(limit*5, 50)
	wantPassages, wantClaims, origin := q.arms()
	ranks := map[int64]*ranked{}      // passages
	claimRanks := map[int64]*ranked{} // claims
	err := pgx.BeginTxFunc(ctx, r.Index.Pool(), pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if vec != nil {
			if _, err := tx.Exec(ctx, `SELECT set_config('hnsw.iterative_scan', 'relaxed_order', true), set_config('hnsw.ef_search', $1, true)`,
				strconv.Itoa(max(k, 100))); err != nil {
				return err
			}
		}
		arms := []struct {
			want   bool
			table  string
			joins  string
			where  string
			origin string
			into   map[int64]*ranked
		}{
			{wantPassages, "p", passageJoins, "", origin, ranks},
			{wantClaims, "c", claimJoins, "c.quote_verified AND ", "", claimRanks},
		}
		for _, arm := range arms {
			if !arm.want {
				continue
			}
			ids, err := textArm(ctx, tx, q, arm.table, arm.joins, arm.where, arm.origin, k)
			if err != nil {
				return fmt.Errorf("full-text search: %w", err)
			}
			for i, id := range ids {
				arm.into[id] = &ranked{text: i + 1}
			}
			if vec == nil {
				continue
			}
			hits, err := vectorArm(ctx, tx, q, arm.table, arm.joins, arm.where, arm.origin, vec, res.EmbedModel, k)
			if err != nil {
				return fmt.Errorf("vector search: %w", err)
			}
			for i, h := range hits {
				if h.similarity < q.MinSimilarity {
					break
				}
				if arm.into[h.id] == nil {
					arm.into[h.id] = &ranked{}
				}
				arm.into[h.id].vector, arm.into[h.id].similarity = i+1, h.similarity
			}
		}
		return nil
	})
	if err != nil {
		return res, err
	}
	if len(ranks)+len(claimRanks) == 0 {
		res.TookMS = time.Since(started).Milliseconds()
		return res, nil
	}
	var cards []Card
	if len(ranks) > 0 {
		cards, err = r.cards(ctx, keys(ranks))
		if err != nil {
			return res, err
		}
	}
	if len(claimRanks) > 0 {
		cs, err := r.claimCards(ctx, keys(claimRanks))
		if err != nil {
			return res, err
		}
		cards = append(cards, cs...)
	}
	now := r.now()
	for i := range cards {
		c := &cards[i]
		rk := ranks[c.PassageID]
		if c.Kind == KindClaim {
			rk = claimRanks[c.ClaimID]
		}
		c.TextRank, c.VectorRank = rk.text, rk.vector
		c.Similarity = math.Round(rk.similarity*1e4) / 1e4
		if rk.text > 0 {
			c.Score += 1 / float64(rrfK+rk.text)
		}
		if rk.vector > 0 {
			c.Score += 1 / float64(rrfK+rk.vector)
		}
		if c.Kind != KindClaim {
			c.Score *= informative(c.Text)
		}
		halfLife := q.PreferRecent
		if halfLife == 0 && c.Volatile {
			halfLife = r.volatileHalfLife()
		}
		if halfLife > 0 {
			c.Score *= math.Pow(0.5, float64(now.Sub(c.dated()))/float64(halfLife))
		}
		c.Age = ageLabel(c, now)
	}
	slices.SortStableFunc(cards, func(a, b Card) int {
		if a.Score != b.Score {
			if a.Score > b.Score {
				return -1
			}
			return 1
		}
		if a.Kind != b.Kind {
			return strings.Compare(a.Kind, b.Kind)
		}
		return compareInt(a.PassageID+a.ClaimID, b.PassageID+b.ClaimID)
	})
	// Twice the limit is picked so that collapsing superseded claims under
	// the claims that supersede them still leaves enough.
	type docKind struct {
		doc  int64
		kind string
	}
	perDoc := map[docKind]int{}
	var picked []Card
	for _, c := range cards {
		key := docKind{c.DocumentID, c.Kind}
		if perDoc[key] == perDocument {
			continue
		}
		perDoc[key]++
		c.Score = math.Round(c.Score*1e6) / 1e6
		picked = append(picked, c)
		if len(picked) == 2*limit {
			break
		}
	}
	if err := r.attachClusters(ctx, picked, q.AsOf); err != nil {
		return res, err
	}
	res.Cards = append(res.Cards, collapseSuperseded(picked)...)
	if len(res.Cards) > limit {
		res.Cards = res.Cards[:limit]
	}
	res.TookMS = time.Since(started).Milliseconds()
	return res, nil
}

type ranked struct {
	text, vector int
	similarity   float64
}

func keys[V any](m map[int64]V) []int64 {
	out := make([]int64, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	return out
}

// arms says what q searches: passages (with the document origin to keep,
// blank for both) and claims.
func (q Query) arms() (passages, claims bool, origin string) {
	if len(q.Kinds) == 0 {
		return true, true, ""
	}
	primary, report := slices.Contains(q.Kinds, KindPassage), slices.Contains(q.Kinds, KindReport)
	switch {
	case primary && !report:
		origin = "primary"
	case report && !primary:
		origin = "synthesis"
	}
	return primary || report, slices.Contains(q.Kinds, KindClaim), origin
}

func (r *Retriever) volatileHalfLife() time.Duration {
	if r.VolatileHalfLife > 0 {
		return r.VolatileHalfLife
	}
	return 365 * 24 * time.Hour
}

func compareInt(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func (q Query) check() error {
	for _, k := range q.Kinds {
		if k != KindPassage && k != KindReport && k != KindClaim {
			return fmt.Errorf("unknown kind %q (want %s, %s or %s)", k, KindPassage, KindReport, KindClaim)
		}
	}
	if q.DateField != "" && q.DateField != DatePublished && q.DateField != DateCollected {
		return fmt.Errorf("unknown date field %q (want %s or %s)", q.DateField, DatePublished, DateCollected)
	}
	if q.Since != nil && q.Until != nil && q.Until.Before(*q.Since) {
		return fmt.Errorf("until %s is before since %s", q.Until.Format(time.DateOnly), q.Since.Format(time.DateOnly))
	}
	if q.PreferRecent < 0 {
		return fmt.Errorf("prefer_recent must not be negative")
	}
	if q.MinSimilarity < 0 || q.MinSimilarity > 1 {
		return fmt.Errorf("min_similarity must be between 0 and 1")
	}
	return nil
}

// args collects positional SQL parameters.
type args []any

func (a *args) add(v any) string {
	*a = append(*a, v)
	return "$" + strconv.Itoa(len(*a))
}

// filters is q's WHERE conditions over documents d and sources s, each
// starting with AND; origin, when set, keeps documents of that origin.
func filters(q Query, a *args, origin string) string {
	var b strings.Builder
	if origin != "" {
		b.WriteString(" AND d.origin = " + a.add(origin))
	}
	col := "d.published_at"
	if q.DateField == DateCollected {
		col = "d.first_fetched_at"
	}
	if q.Since != nil {
		b.WriteString(" AND " + col + " >= " + a.add(*q.Since))
	}
	if q.Until != nil {
		b.WriteString(" AND " + col + " <= " + a.add(*q.Until))
	}
	if q.AsOf != nil {
		b.WriteString(" AND d.first_fetched_at <= " + a.add(*q.AsOf))
	}
	if q.RunID != "" {
		run := a.add(q.RunID)
		b.WriteString(" AND (EXISTS (SELECT 1 FROM fetches f WHERE f.document_id = d.id AND f.run_id = " + run +
			") OR EXISTS (SELECT 1 FROM runs r WHERE r.id = " + run + " AND r.report_document_id = d.id))")
	}
	if dom := strings.ToLower(strings.TrimSpace(q.Domain)); dom != "" {
		dom = strings.TrimPrefix(dom, "www.")
		b.WriteString(" AND (s.domain = " + a.add(dom) + " OR s.domain LIKE " + a.add("%."+dom) + ")")
	}
	return b.String()
}

const passageJoins = ` FROM passages p JOIN documents d ON d.id = p.document_id JOIN sources s ON s.id = d.source_id`

const claimJoins = ` FROM claims c JOIN documents d ON d.id = c.document_id JOIN sources s ON s.id = d.source_id`

// textArm ranks the rows of table t (passages p or claims c, with their
// joins and an extra condition) by full-text match. Query terms are ORed,
// so a row matching some of a natural-language question still ranks, with
// more matches ranking higher.
func textArm(ctx context.Context, tx pgx.Tx, q Query, t, joins, where, origin string, k int) ([]int64, error) {
	a := args{}
	tsq := "replace(plainto_tsquery('english', " + a.add(q.Text) + ")::text, '&', '|')::tsquery"
	sql := `SELECT ` + t + `.id` + joins + `, (SELECT ` + tsq + ` AS q) query
		WHERE ` + where + t + `.tsv @@ query.q` + filters(q, &a, origin) + `
		ORDER BY ts_rank_cd(` + t + `.tsv, query.q) DESC, ` + t + `.id LIMIT ` + a.add(k)
	return queryIDs(ctx, tx, sql, a)
}

type vectorHit struct {
	id         int64
	similarity float64
}

// vectorArm ranks the rows of table t embedded with model by cosine
// distance to vec. The transaction's iterative scans keep a filtered query
// from coming back short.
func vectorArm(ctx context.Context, tx pgx.Tx, q Query, t, joins, where, origin string, vec []float32, model string, k int) ([]vectorHit, error) {
	a := args{}
	v := a.add(pgvector.NewVector(vec))
	sql := `SELECT ` + t + `.id, 1 - (` + t + `.embedding <=> ` + v + `)` + joins + `
		WHERE ` + where + t + `.embedding IS NOT NULL AND ` + t + `.embed_model = ` + a.add(model) + filters(q, &a, origin) + `
		ORDER BY ` + t + `.embedding <=> ` + v + ` LIMIT ` + a.add(k)
	rows, err := tx.Query(ctx, sql, a...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (vectorHit, error) {
		var h vectorHit
		err := row.Scan(&h.id, &h.similarity)
		return h, err
	})
}

func queryIDs(ctx context.Context, tx pgx.Tx, sql string, a args) ([]int64, error) {
	rows, err := tx.Query(ctx, sql, a...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int64])
}

// docCardColumns are the document and source columns docScan reads, over
// documents d and sources s.
const docCardColumns = `d.source_id, d.content_kind, d.origin, d.published_at, d.published_precision, d.published_from, d.published_weak,
	d.first_fetched_at, d.last_fetched_at, d.title, s.url, s.domain, s.title, s.kind, s.doi,
	ARRAY(SELECT DISTINCT f.run_id FROM fetches f WHERE f.document_id = d.id ORDER BY f.run_id DESC LIMIT 5) ||
	ARRAY(SELECT r.id FROM runs r WHERE r.report_document_id = d.id ORDER BY r.id DESC LIMIT 5)`

// cardColumns are the columns scanCard reads, over passages p, documents d
// and sources s.
const cardColumns = `p.id, p.document_id, p.char_start, p.char_end, p.text, ` + docCardColumns

func (r *Retriever) cards(ctx context.Context, ids []int64) ([]Card, error) {
	rows, err := r.Index.Pool().Query(ctx, `SELECT `+cardColumns+passageJoins+` WHERE p.id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanCard)
}

func scanCard(row pgx.CollectableRow) (Card, error) {
	var c Card
	var d docScan
	err := row.Scan(append([]any{&c.PassageID, &c.DocumentID, &c.CharStart, &c.CharEnd, &c.Text}, d.targets(&c)...)...)
	if err != nil {
		return c, err
	}
	c.Ref = PassageRef(c.PassageID)
	d.finish(&c)
	return c, nil
}

// docScan reads docCardColumns into a card.
type docScan struct {
	origin, docTitle, srcTitle string
	pubAt                      *time.Time
	precision, from, doi       *string
	weak                       bool
}

func (d *docScan) targets(c *Card) []any {
	return []any{&c.SourceID, &c.ContentKind, &d.origin, &d.pubAt, &d.precision, &d.from, &d.weak,
		&c.Collected, &c.LastFetched, &d.docTitle, &c.URL, &c.Domain, &d.srcTitle, &c.SourceKind, &d.doi, &c.Runs}
}

// finish sets the card's kind (a passage's, unless it's already a claim),
// title, DOI and publication date.
func (d *docScan) finish(c *Card) {
	if c.Kind == "" {
		c.Kind = KindPassage
		if d.origin == "synthesis" {
			c.Kind = KindReport
		}
	}
	c.Title = d.docTitle
	if c.Title == "" {
		c.Title = d.srcTitle
	}
	if d.doi != nil {
		c.DOI = *d.doi
	}
	c.Published = dateOf(d.pubAt, d.precision, d.from, d.weak)
}

// dateOf turns the documents date columns back into a Date at its
// precision.
func dateOf(at *time.Time, precision, from *string, weak bool) *Date {
	if at == nil || precision == nil {
		return nil
	}
	layout := dateLayouts[*precision]
	if layout == "" {
		layout = "2006-01-02"
	}
	d := &Date{Date: at.Format(layout), Precision: *precision, Weak: weak}
	if from != nil {
		d.From = *from
	}
	return d
}

// dated is the time a card's age is measured from: for a claim, the date
// it's true as of when the document says; otherwise its publication date,
// else when it was collected.
func (c Card) dated() time.Time {
	if t, ok := parseDate(c.AsOf); ok {
		return t
	}
	if c.Published != nil {
		if t, err := time.Parse(dateLayouts[c.Published.Precision], c.Published.Date); err == nil {
			return t
		}
	}
	return c.Collected
}

var dateLayouts = map[string]string{"year": "2006", "month": "2006-01", "day": "2006-01-02"}

// ageLabel describes how old a card's evidence is.
func ageLabel(c *Card, now time.Time) string {
	collected := c.Collected.Format(time.DateOnly)
	label := ""
	if t, ok := parseDate(c.AsOf); ok {
		label = "true as of " + c.AsOf + " (" + since(t, now) + "); "
	}
	if c.Published == nil {
		return label + "undated; collected " + collected
	}
	pub, _ := time.Parse(dateLayouts[c.Published.Precision], c.Published.Date)
	label += "published " + c.Published.Date + " (" + since(pub, now) + ")"
	if c.Published.Weak {
		label += ", date weak (" + c.Published.From + ")"
	}
	return label + "; collected " + collected
}

// since is a rough age: days, months or years.
func since(t, now time.Time) string {
	d := now.Sub(t)
	switch days := int(d.Hours() / 24); {
	case days < 0:
		return "future date"
	case days < 60:
		return strconv.Itoa(days) + "d ago"
	case days < 730:
		return strconv.Itoa(days/30) + "mo ago"
	default:
		return strconv.Itoa(days/365) + "y ago"
	}
}

// informative scales a score down for passages that are mostly repetition,
// such as a figure's axis labels or a table of tokens: the share of
// distinct words, relative to ordinary prose, floored at 0.2.
func informative(text string) float64 {
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	if len(words) < 40 {
		return 1
	}
	distinct := map[string]bool{}
	for _, w := range words {
		distinct[w] = true
	}
	ratio := float64(len(distinct)) / float64(len(words))
	return math.Max(0.2, math.Min(1, ratio/0.25))
}

// Refs: what reports cite. P is a passage, S a source, C a claim, E a
// capture of the report's own run.
const (
	RefPassage = "P"
	RefSource  = "S"
	RefClaim   = "C"
	RefCapture = "E"
)

// PassageRef is the citation for a passage.
func PassageRef(id int64) string { return RefPassage + ":" + strconv.FormatInt(id, 10) }

// ClaimRef is the citation for a claim.
func ClaimRef(id int64) string { return RefClaim + ":" + strconv.FormatInt(id, 10) }

// SourceRef is the citation for a source.
func SourceRef(id int64) string { return RefSource + ":" + strconv.FormatInt(id, 10) }

// ParseRef splits "P:123" (or "S:123") into its kind and ID.
func ParseRef(ref string) (kind string, id int64, err error) {
	k, n, ok := strings.Cut(strings.TrimSpace(ref), ":")
	if !ok || (k != RefPassage && k != RefSource) {
		return "", 0, fmt.Errorf("invalid ref %q (want P:<id> or S:<id>)", ref)
	}
	id, err = strconv.ParseInt(n, 10, 64)
	if err != nil || id <= 0 {
		return "", 0, fmt.Errorf("invalid ref %q (want P:<id> or S:<id>)", ref)
	}
	return k, id, nil
}
