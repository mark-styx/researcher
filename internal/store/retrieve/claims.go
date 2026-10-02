package retrieve

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Cluster flags, computed from a claim's links and dates. No model reads
// anything to set them; a flag that rests on a model's label says so in
// the related claim's Method.
const (
	// FlagReinforced: claims from two or more independent origins say it
	// (Cluster.Origins counts them, SupportDates spans them).
	FlagReinforced = "reinforced"
	// FlagSingleOrigin: everything saying it traces to one origin, however
	// many documents repeat it.
	FlagSingleOrigin = "single_origin"
	// FlagContested: a claim contradicts it.
	FlagContested = "contested"
	// FlagNewerContradiction: the newest contradicting claim is dated
	// after the newest one saying it.
	FlagNewerContradiction = "newer_contradiction"
	// FlagSuperseded: a newer claim supersedes it.
	FlagSuperseded = "superseded"
	// FlagPossiblyOutdated: volatile, and dated longer ago than the
	// retriever's VolatileMaxAge.
	FlagPossiblyOutdated = "possibly_outdated"
)

// How a related claim relates to the claim whose cluster it's in.
const (
	RelSame         = "same"
	RelSupports     = "supports"
	RelContradicts  = "contradicts"
	RelRefines      = "refines"    // the claim refines this related one
	RelRefinedBy    = "refined_by" // this related claim refines the claim
	RelSupersedes   = "supersedes" // the claim supersedes this related one
	RelSupersededBy = "superseded_by"
)

// Related is a claim linked to another, with how the link was made: by
// rule (the same quoted words), by a model (an inference, with its
// confidence) or by a person.
type Related struct {
	Ref        string  `json:"ref"`
	ClaimID    int64   `json:"claim_id"`
	Relation   string  `json:"relation"`
	Method     string  `json:"method"`
	Model      string  `json:"model,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
	Note       string  `json:"note,omitempty"`

	Text          string    `json:"text"`
	QuoteVerified bool      `json:"quote_verified"`
	AsOf          string    `json:"as_of,omitempty"`
	Volatile      bool      `json:"volatile,omitempty"`
	DocumentID    int64     `json:"document_id"`
	Origin        int64     `json:"origin"`
	URL           string    `json:"url"`
	Domain        string    `json:"domain"`
	Title         string    `json:"title,omitempty"`
	Published     *Date     `json:"published,omitempty"`
	Collected     time.Time `json:"collected"`
	LastFetched   time.Time `json:"last_fetched"`
	// Dated is when it's true as of: its as_of, else its document's
	// publication date, else when that was collected.
	Dated string `json:"dated"`
}

// Cluster is what's linked to a claim, one link away, with what the links
// add up to.
type Cluster struct {
	// Origin is the claim's origin: its document, or the lowest document id
	// of the group of documents that are one origin repeated.
	Origin  int64     `json:"origin"`
	Related []Related `json:"related,omitempty"`
	// Origins counts the independent origins saying it: the claim's own and
	// those of the same and supporting claims. SupportDates is the span of
	// their dates; LastConfirmed is the latest fetch of any of their
	// documents.
	Origins       int       `json:"origins"`
	SupportDates  string    `json:"support_dates,omitempty"`
	LastConfirmed time.Time `json:"last_confirmed"`
}

// claimCardColumns are the columns scanClaimCard reads, over claims c,
// origins o, documents d and sources s.
const claimCardColumns = `c.id, c.document_id, coalesce(c.passage_id, 0), coalesce(c.char_start, 0), coalesce(c.char_end, 0),
	c.text, c.quote, c.quote_verified, c.as_of, c.as_of_precision, c.volatile, d.sha256, coalesce(o.origin_id, c.document_id), ` + docCardColumns

const claimCardJoins = claimJoins + ` LEFT JOIN origins o ON o.document_id = c.document_id`

// claimRow is a claim card with what's needed to finish it.
type claimRow struct {
	card       Card
	modelQuote string
	sha        string
	origin     int64
}

func scanClaimRow(row pgx.CollectableRow) (claimRow, error) {
	var cr claimRow
	c := &cr.card
	c.Kind = KindClaim
	var asOf *time.Time
	var asOfP *string
	var d docScan
	err := row.Scan(append([]any{&c.ClaimID, &c.DocumentID, &c.PassageID, &c.CharStart, &c.CharEnd,
		&c.Text, &cr.modelQuote, &c.QuoteVerified, &asOf, &asOfP, &c.Volatile, &cr.sha, &cr.origin}, d.targets(c)...)...)
	if err != nil {
		return cr, err
	}
	c.Ref = ClaimRef(c.ClaimID)
	c.AsOf = dateText(asOf, asOfP)
	d.finish(c)
	return cr, nil
}

// claimRows reads the claims ids with their documents.
func (r *Retriever) claimRows(ctx context.Context, ids []int64) ([]claimRow, error) {
	rows, err := r.Index.Pool().Query(ctx, `SELECT `+claimCardColumns+claimCardJoins+` WHERE c.id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	crs, err := pgx.CollectRows(rows, scanClaimRow)
	if err != nil {
		return nil, err
	}
	r.fillQuotes(crs)
	return crs, nil
}

// claimCards are the cards of claims ids, quotes filled in.
func (r *Retriever) claimCards(ctx context.Context, ids []int64) ([]Card, error) {
	crs, err := r.claimRows(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]Card, len(crs))
	for i, cr := range crs {
		out[i] = cr.card
		out[i].Cluster = &Cluster{Origin: cr.origin}
	}
	return out, nil
}

// fillQuotes sets each verified claim's quote to the words its document
// has at the claim's offsets, which verification found to match the
// model's quote. When the text can't be read, the model's quote stands in:
// it matched up to case, punctuation and spacing.
func (r *Retriever) fillQuotes(crs []claimRow) {
	texts := map[string][]rune{}
	for i := range crs {
		c := &crs[i].card
		if !c.QuoteVerified {
			continue
		}
		c.Quote = crs[i].modelQuote
		if r.Store == nil {
			continue
		}
		text, ok := texts[crs[i].sha]
		if !ok {
			if t, err := r.Store.ReadText(crs[i].sha); err == nil {
				text = []rune(t)
			}
			texts[crs[i].sha] = text
		}
		if c.CharStart >= 0 && c.CharEnd > c.CharStart && c.CharEnd <= len(text) {
			c.Quote = string(text[c.CharStart:c.CharEnd])
		}
	}
}

// linkRow is a claim_links row.
type linkRow struct {
	from, to         int64
	relation, method string
	model, note      *string
	confidence       *float64
}

// links returns the link that holds for each pair of claims with one of
// ids in it (as claim_relations picks it: a person's over a model's over a
// rule's, the latest of each), unrelated ones left out. among keeps only
// pairs with both claims in ids.
func (r *Retriever) links(ctx context.Context, ids []int64, among bool) ([]linkRow, error) {
	cond := `from_claim = ANY($1) OR to_claim = ANY($1)`
	if among {
		cond = `from_claim = ANY($1) AND to_claim = ANY($1)`
	}
	rows, err := r.Index.Pool().Query(ctx, `SELECT from_claim, to_claim, relation, method, model, note, confidence FROM (
		SELECT DISTINCT ON (LEAST(from_claim, to_claim), GREATEST(from_claim, to_claim)) *
		FROM claim_links WHERE `+cond+`
		ORDER BY LEAST(from_claim, to_claim), GREATEST(from_claim, to_claim),
			CASE method WHEN 'human' THEN 0 WHEN 'model' THEN 1 ELSE 2 END, created_at DESC) l
		WHERE relation <> 'unrelated'`, ids)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (linkRow, error) {
		var l linkRow
		err := row.Scan(&l.from, &l.to, &l.relation, &l.method, &l.model, &l.note, &l.confidence)
		return l, err
	})
}

// relationFor is how other relates to id under l.
func relationFor(l linkRow, id int64) string {
	from := l.from == id
	switch l.relation {
	case "refines":
		if from {
			return RelRefines
		}
		return RelRefinedBy
	case "supersedes":
		if from {
			return RelSupersedes
		}
		return RelSupersededBy
	}
	return l.relation
}

// attachClusters fills in the clusters and flags of the claim cards in
// cards. asOf, when set, leaves out related claims from documents
// collected after it.
func (r *Retriever) attachClusters(ctx context.Context, cards []Card, asOf *time.Time) error {
	var ids []int64
	for _, c := range cards {
		if c.Kind == KindClaim {
			ids = append(ids, c.ClaimID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	links, err := r.links(ctx, ids, false)
	if err != nil {
		return fmt.Errorf("reading claim links: %w", err)
	}
	other := map[int64]bool{}
	for _, l := range links {
		other[l.from], other[l.to] = true, true
	}
	related := map[int64]claimRow{}
	if len(other) > 0 {
		crs, err := r.claimRows(ctx, keys(other))
		if err != nil {
			return err
		}
		for _, cr := range crs {
			if asOf != nil && cr.card.Collected.After(*asOf) {
				continue
			}
			related[cr.card.ClaimID] = cr
		}
	}
	byClaim := map[int64][]linkRow{}
	for _, l := range links {
		byClaim[l.from] = append(byClaim[l.from], l)
		byClaim[l.to] = append(byClaim[l.to], l)
	}
	now := r.now()
	for i := range cards {
		c := &cards[i]
		if c.Kind != KindClaim {
			continue
		}
		if c.Cluster == nil {
			c.Cluster = &Cluster{Origin: c.DocumentID}
		}
		for _, l := range byClaim[c.ClaimID] {
			id := l.to
			if id == c.ClaimID {
				id = l.from
			}
			cr, ok := related[id]
			if !ok {
				continue
			}
			c.Cluster.Related = append(c.Cluster.Related, relatedOf(cr, l, relationFor(l, c.ClaimID), now))
		}
		finishCluster(c, now, r.volatileMaxAge())
	}
	return nil
}

func relatedOf(cr claimRow, l linkRow, relation string, now time.Time) Related {
	c := cr.card
	rel := Related{Ref: c.Ref, ClaimID: c.ClaimID, Relation: relation, Method: l.method,
		Text: c.Text, QuoteVerified: c.QuoteVerified, AsOf: c.AsOf, Volatile: c.Volatile, DocumentID: c.DocumentID,
		Origin: cr.origin, URL: c.URL, Domain: c.Domain, Title: c.Title, Published: c.Published,
		Collected: c.Collected, LastFetched: c.LastFetched, Dated: c.dated().Format(time.DateOnly)}
	if l.model != nil {
		rel.Model = *l.model
	}
	if l.note != nil {
		rel.Note = *l.note
	}
	if l.confidence != nil {
		rel.Confidence = *l.confidence
	}
	return rel
}

func (r *Retriever) volatileMaxAge() time.Duration {
	if r.VolatileMaxAge > 0 {
		return r.VolatileMaxAge
	}
	return 30 * 24 * time.Hour
}

// relationOrder sorts a cluster: what bears on the claim most first.
var relationOrder = map[string]int{RelSupersededBy: 0, RelContradicts: 1, RelSame: 2, RelSupports: 3, RelRefinedBy: 4, RelRefines: 5, RelSupersedes: 6}

// finishCluster sorts c's cluster, by relation and then newest first, and
// sets the counts and flags it adds up to.
func finishCluster(c *Card, now time.Time, maxAge time.Duration) {
	cl := c.Cluster
	slices.SortStableFunc(cl.Related, func(a, b Related) int {
		if d := relationOrder[a.Relation] - relationOrder[b.Relation]; d != 0 {
			return d
		}
		if d := strings.Compare(b.Dated, a.Dated); d != 0 {
			return d
		}
		return compareInt(a.ClaimID, b.ClaimID)
	})
	origins := map[int64]bool{cl.Origin: true}
	first, last := c.dated(), c.dated()
	cl.LastConfirmed = c.LastFetched
	var newestContra time.Time
	contested, superseded := false, false
	for _, rel := range cl.Related {
		dated, _ := time.Parse(time.DateOnly, rel.Dated)
		switch rel.Relation {
		case RelSame, RelSupports:
			origins[rel.Origin] = true
			first, last = minTime(first, dated), maxTime(last, dated)
			if rel.LastFetched.After(cl.LastConfirmed) {
				cl.LastConfirmed = rel.LastFetched
			}
		case RelContradicts:
			contested = true
			newestContra = maxTime(newestContra, dated)
		case RelSupersededBy:
			superseded = true
		}
	}
	cl.Origins = len(origins)
	if cl.Origins > 1 {
		cl.SupportDates = first.Format("2006")
		if y := last.Format("2006"); y != cl.SupportDates {
			cl.SupportDates += "-" + y
		}
	}
	c.Flags = nil
	if cl.Origins >= 2 {
		c.Flags = append(c.Flags, FlagReinforced)
	} else {
		c.Flags = append(c.Flags, FlagSingleOrigin)
	}
	if contested {
		c.Flags = append(c.Flags, FlagContested)
		if newestContra.After(last) {
			c.Flags = append(c.Flags, FlagNewerContradiction)
		}
	}
	if superseded {
		c.Flags = append(c.Flags, FlagSuperseded)
	}
	if c.Volatile && now.Sub(c.dated()) > maxAge {
		c.Flags = append(c.Flags, FlagPossiblyOutdated)
	}
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// collapseSuperseded drops the claims a card in cards supersedes: the
// newer card lists them in its cluster.
func collapseSuperseded(cards []Card) []Card {
	present := map[int64]bool{}
	for _, c := range cards {
		if c.Kind == KindClaim {
			present[c.ClaimID] = true
		}
	}
	out := cards[:0:0]
	for _, c := range cards {
		if c.Kind == KindClaim && c.Cluster != nil && slices.ContainsFunc(c.Cluster.Related, func(r Related) bool {
			return r.Relation == RelSupersededBy && present[r.ClaimID]
		}) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// dateText formats a date to its precision: 2024, 2024-03 or 2024-03-09.
func dateText(t *time.Time, precision *string) string {
	if t == nil {
		return ""
	}
	p := "day"
	if precision != nil {
		p = *precision
	}
	return t.Format(dateLayouts[p])
}

// parseDate reads a YYYY, YYYY-MM or YYYY-MM-DD date.
func parseDate(s string) (time.Time, bool) {
	for _, layout := range []string{"2006-01-02", "2006-01", "2006"} {
		if len(s) == len(layout) {
			t, err := time.Parse(layout, s)
			return t, err == nil
		}
	}
	return time.Time{}, false
}

// ClaimResult is one claim in full: its card with its cluster, the
// passage its quote is in, the model's own quote when it wasn't found, and
// the other versions of its source.
type ClaimResult struct {
	Card
	// ModelQuote is the quote the model gave, set only when it wasn't
	// found in the document: it's unverified and mustn't be cited as a
	// quote.
	ModelQuote  string         `json:"model_quote,omitempty"`
	Extractor   string         `json:"extractor"`
	ExtractedAt time.Time      `json:"extracted_at"`
	Passage     *PassageResult `json:"passage,omitempty"`
	Versions    []Version      `json:"other_versions,omitempty"`
}

// Claim looks up claim id.
func (r *Retriever) Claim(ctx context.Context, id int64) (ClaimResult, error) {
	var res ClaimResult
	crs, err := r.claimRows(ctx, []int64{id})
	if err != nil {
		return res, err
	}
	if len(crs) == 0 {
		return res, fmt.Errorf("claim %s: %w", ClaimRef(id), ErrNotFound)
	}
	cr := crs[0]
	res.Card = cr.card
	res.Cluster = &Cluster{Origin: cr.origin}
	if !res.QuoteVerified {
		res.ModelQuote = cr.modelQuote
	}
	if err := r.Index.Pool().QueryRow(ctx, `SELECT extractor, extracted_at FROM claims WHERE id = $1`, id).Scan(&res.Extractor, &res.ExtractedAt); err != nil {
		return res, err
	}
	cards := []Card{res.Card}
	if err := r.attachClusters(ctx, cards, nil); err != nil {
		return res, err
	}
	res.Card = cards[0]
	res.Age = ageLabel(&res.Card, r.now())
	if res.PassageID != 0 {
		p, err := r.Passage(ctx, res.PassageID, 0)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return res, err
		}
		if err == nil {
			res.Passage = &p
		}
	}
	res.Versions, err = r.versions(ctx, res.SourceID, res.DocumentID)
	return res, err
}

// TimelineEntry is one claim in a timeline.
type TimelineEntry struct {
	Dated string `json:"dated"`
	Card
	// Changes says how the claim bears on earlier entries: supersedes,
	// contradicts or refines them.
	Changes []string `json:"changes,omitempty"`
	// Matched is set for claims the query found, as against ones reached
	// through a link.
	Matched bool `json:"matched"`
}

// TimelineResult is a timeline: claims oldest first.
type TimelineResult struct {
	Query   string          `json:"query"`
	Mode    string          `json:"mode,omitempty"`
	Note    string          `json:"note,omitempty"`
	Entries []TimelineEntry `json:"entries"`
	TookMS  int64           `json:"took_ms"`
}

// Timeline lays out claims in date order: the claims q finds (or, when
// q.Text is a claim ref such as C:123, that claim) and the claims linked
// to them, each marked with how it changes what came before: a claim that
// supersedes or contradicts an earlier one is where the evidence changed.
func (r *Retriever) Timeline(ctx context.Context, q Query) (TimelineResult, error) {
	started := time.Now()
	res := TimelineResult{Query: strings.TrimSpace(q.Text), Entries: []TimelineEntry{}}
	var seeds []int64
	if id, ok := claimRefID(res.Query); ok {
		seeds = []int64{id}
	} else {
		if q.Limit <= 0 {
			q.Limit = 20
		}
		q.Kinds = []string{KindClaim}
		found, err := r.Find(ctx, q)
		if err != nil {
			return res, err
		}
		res.Mode, res.Note = found.Mode, found.Note
		for _, c := range found.Cards {
			seeds = append(seeds, c.ClaimID)
		}
	}
	if len(seeds) == 0 {
		res.TookMS = time.Since(started).Milliseconds()
		return res, nil
	}
	links, err := r.links(ctx, seeds, false)
	if err != nil {
		return res, err
	}
	ids := map[int64]bool{}
	for _, id := range seeds {
		ids[id] = true
	}
	for _, l := range links {
		ids[l.from], ids[l.to] = true, true
	}
	cards, err := r.claimCards(ctx, keys(ids))
	if err != nil {
		return res, err
	}
	if len(cards) == 0 && len(seeds) == 1 {
		return res, fmt.Errorf("claim %s: %w", ClaimRef(seeds[0]), ErrNotFound)
	}
	if q.AsOf != nil {
		cards = slices.DeleteFunc(cards, func(c Card) bool { return c.Collected.After(*q.AsOf) })
	}
	if err := r.attachClusters(ctx, cards, q.AsOf); err != nil {
		return res, err
	}
	all, err := r.links(ctx, keys(ids), true)
	if err != nil {
		return res, err
	}
	now := r.now()
	byID := map[int64]*TimelineEntry{}
	for _, c := range cards {
		c.Age = ageLabel(&c, now)
		res.Entries = append(res.Entries, TimelineEntry{Dated: c.dated().Format(time.DateOnly), Card: c, Matched: slices.Contains(seeds, c.ClaimID)})
	}
	for i := range res.Entries {
		byID[res.Entries[i].ClaimID] = &res.Entries[i]
	}
	for _, l := range all {
		a, b := byID[l.from], byID[l.to]
		if a == nil || b == nil {
			continue
		}
		switch l.relation {
		case "supersedes", "refines":
			a.Changes = append(a.Changes, l.relation+" "+b.Ref)
		case "contradicts":
			// The later of the two is where the evidence changed.
			if b.Dated > a.Dated {
				a, b = b, a
			}
			a.Changes = append(a.Changes, "contradicts "+b.Ref+" ("+b.Dated+")")
		}
	}
	slices.SortStableFunc(res.Entries, func(a, b TimelineEntry) int {
		if d := strings.Compare(a.Dated, b.Dated); d != 0 {
			return d
		}
		return compareInt(a.ClaimID, b.ClaimID)
	})
	res.TookMS = time.Since(started).Milliseconds()
	return res, nil
}

// claimRefID reads a claim ref (C:123).
func claimRefID(s string) (int64, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(s), RefClaim+":")
	if !ok {
		return 0, false
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	return id, err == nil && id > 0
}
