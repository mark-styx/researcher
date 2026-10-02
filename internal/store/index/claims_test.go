package index

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/marklubin/researchguy/internal/store"
)

const testExtractor = "test-model/claims-v1"

// claimRuns indexes fetchedRun (go.dev with docText, plus an abstract) and
// a second run that fetched a mirror of docText and an unrelated page.
func claimRuns(t *testing.T, ix *Index, st *store.Store) (goDev, mirror, other int64) {
	t.Helper()
	fetchedRun(t, st)
	run := sampleRun(t, st)
	full := store.HashText(docText)
	otherText := strings.Repeat("Wholly unrelated text about tide tables and harbour pilots. ", 30)
	otherSHA, _ := st.PutText(otherText)
	at := time.Date(2026, 10, 2, 13, 0, 0, 0, time.UTC)
	writeFetches(t, run,
		store.FetchRecord{URL: "https://mirror.example/copy", Reason: store.ReasonOpened, Via: "direct", AttemptedAt: at, Attempts: 1,
			HTTPStatus: 200, TextSHA256: full, TextChars: len(docText), ContentKind: store.KindFull, Title: "Copy"},
		store.FetchRecord{URL: "https://example.org/post", Reason: store.ReasonOpened, Via: "direct", AttemptedAt: at, Attempts: 1,
			HTTPStatus: 200, TextSHA256: otherSHA, TextChars: len(otherText), ContentKind: store.KindFull, Title: "Tides"},
	)
	if _, err := ix.Sync(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	return DocumentID("go.dev/blog/go1.18", full), DocumentID("mirror.example/copy", full), DocumentID("example.org/post", otherSHA)
}

func docTextExtraction() store.Extraction {
	return store.Extraction{TextSHA: store.HashText(docText), Extractor: testExtractor, Model: "test-model",
		ExtractedAt: time.Date(2026, 10, 2, 14, 0, 0, 0, time.UTC), Chunks: 2, Attempts: 1,
		Claims: []store.ExtractedClaim{
			{Text: "Exposure shifted opinion by 0.1 SD.", Quote: "exposure shifted opinion by a tenth of a standard deviation", Chunk: 0},
			{Text: "The effect faded within two weeks.", Quote: "The effect faded within two weeks of the last exposure.", Chunk: 0},
			{Text: "  ", Quote: "nothing", Chunk: 0},
			{Text: "Replications found the pattern in three countries.", Quote: "Replications in three countries found the same pattern",
				AsOf: "2024-03", Volatile: true, Chunk: 1},
			{Text: "The effect lasted for years.", Quote: "The effect lasted for years after exposure", AsOf: "March 2024", Chunk: 1},
		},
		Failed: []store.ChunkError{{Chunk: 2, Error: "model returned no JSON"}}}
}

type claimRow struct {
	id               int64
	passage          *int64
	verified         bool
	start, end       *int
	key              *string
	asOf             *time.Time
	precision        *string
	volatile, hasVec bool
	chunk, ord       int
}

func claimsOf(t *testing.T, ix *Index, doc int64) []claimRow {
	t.Helper()
	rows, err := ix.pool.Query(context.Background(), `SELECT id, passage_id, quote_verified, char_start, char_end, quote_key,
		as_of, as_of_precision, volatile, embedding IS NOT NULL, chunk, ord FROM claims WHERE document_id = $1 ORDER BY chunk, ord`, doc)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []claimRow
	for rows.Next() {
		var c claimRow
		if err := rows.Scan(&c.id, &c.passage, &c.verified, &c.start, &c.end, &c.key, &c.asOf, &c.precision, &c.volatile, &c.hasVec, &c.chunk, &c.ord); err != nil {
			t.Fatal(err)
		}
		out = append(out, c)
	}
	return out
}

func TestSyncClaims_AnchorsQuotesLinksAndGroupsOrigins(t *testing.T) {
	ix, _ := openIndex(t)
	ctx := context.Background()
	st := newStore(t)
	goDev, mirror, other := claimRuns(t, ix, st)

	e := docTextExtraction()
	if err := st.PutExtraction(e); err != nil {
		t.Fatal(err)
	}
	// A cached vector for one claim's text is attached at ingest.
	ix.SetEmbedModel("fake")
	if err := st.PutVector("fake", store.HashText("Exposure shifted opinion by 0.1 SD."), vectorFor("x", Dims)); err != nil {
		t.Fatal(err)
	}
	// An extraction of text no document has waits.
	if err := st.PutExtraction(store.Extraction{TextSHA: store.HashText("not fetched yet"), Extractor: testExtractor}); err != nil {
		t.Fatal(err)
	}

	stats, err := ix.SyncClaims(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Extractions != 2 || stats.Claims != 8 || stats.Verified != 6 || stats.Waiting != 1 || stats.RuleLinks != 3 || len(stats.Failed) != 0 {
		t.Fatalf("stats = %+v", stats)
	}

	cs := claimsOf(t, ix, goDev)
	if len(cs) != 4 {
		t.Fatalf("go.dev claims = %+v", cs)
	}
	runes := []rune(docText)
	for i, want := range []string{"Exposure shifted opinion by a tenth of a standard deviation", "The effect faded within two weeks of the last exposure",
		"Replications in three countries found the same pattern"} {
		c := cs[i]
		if !c.verified || c.start == nil || string(runes[*c.start:*c.end]) != want || c.passage == nil || c.key == nil {
			t.Errorf("claim %d = %+v", i, c)
		}
	}
	if cs[0].id != ClaimID(goDev, testExtractor, 0, 0) || cs[2].id != ClaimID(goDev, testExtractor, 1, 0) {
		t.Errorf("claim ids aren't derived from document, extractor, chunk and place: %+v", cs)
	}
	// Each verified claim sits in the passage holding its quote.
	var passageText string
	ix.pool.QueryRow(ctx, `SELECT text FROM passages WHERE id = $1`, *cs[2].passage).Scan(&passageText)
	if !strings.Contains(passageText, "Replications in three countries") {
		t.Errorf("claim 2's passage = %q", passageText)
	}
	if c := cs[2]; c.asOf == nil || c.asOf.Format("2006-01-02") != "2024-03-01" || *c.precision != "month" || !c.volatile {
		t.Errorf("as_of = %+v", c)
	}
	if c := cs[3]; c.verified || c.start != nil || c.passage != nil || c.key != nil || c.asOf != nil {
		t.Errorf("an unverified quote is anchored: %+v", c)
	}
	if !cs[0].hasVec || cs[1].hasVec {
		t.Errorf("cached vectors: %v %v", cs[0].hasVec, cs[1].hasVec)
	}
	var complete bool
	ix.pool.QueryRow(ctx, `SELECT complete FROM extractions WHERE document_id = $1`, goDev).Scan(&complete)
	if complete {
		t.Error("an extraction with a failed chunk is marked complete")
	}

	// The mirror quotes the same words: rule links, and one origin.
	var same int
	ix.pool.QueryRow(ctx, `SELECT count(*) FROM claim_relations l JOIN claims a ON a.id = l.from_claim JOIN claims b ON b.id = l.to_claim
		WHERE l.relation = 'same' AND l.method = 'rule' AND a.document_id <> b.document_id`).Scan(&same)
	if same != 3 {
		t.Errorf("rule links = %d", same)
	}
	origin := func(doc int64) (int64, string) {
		var o int64
		var reason string
		ix.pool.QueryRow(ctx, `SELECT origin_id, reason FROM origins WHERE document_id = $1`, doc).Scan(&o, &reason)
		return o, reason
	}
	root, joined := min(goDev, mirror), max(goDev, mirror)
	if o, r := origin(joined); o != root || r != "same-link" {
		t.Errorf("mirror's origin = %d %s, want %d same-link", o, r, root)
	}
	if o, r := origin(root); o != root || r != "self" {
		t.Errorf("root's origin = %d %s", o, r)
	}
	if o, r := origin(other); o != other || r != "self" {
		t.Errorf("unrelated page's origin = %d %s", o, r)
	}
	if stats.Origins == nil || stats.Origins.Documents != 4 || stats.Origins.Origins != 3 {
		t.Errorf("origin stats = %+v", stats.Origins)
	}

	// Nothing new: nothing to do.
	again, err := ix.SyncClaims(ctx, st)
	if err != nil || again.Extractions != 0 || again.Origins != nil || again.Waiting != 1 {
		t.Errorf("second sync = %+v, %v", again, err)
	}

	// Logged links: a human's overrides a model's for the pair, either
	// way round.
	a, b := cs[0].id, cs[1].id
	now := time.Now().UTC()
	if err := st.AppendLinks([]store.Link{
		{From: a, To: b, Relation: store.RelSupports, Method: store.LinkModel, Model: "sonnet", Confidence: 0.7, CreatedAt: now},
		{From: b, To: a, Relation: store.RelContradicts, Method: store.LinkHuman, Note: "checked", CreatedAt: now},
	}); err != nil {
		t.Fatal(err)
	}
	linked, err := ix.SyncClaims(ctx, st)
	if err != nil || linked.Links != 2 {
		t.Fatalf("link sync = %+v, %v", linked, err)
	}
	var relation, method string
	ix.pool.QueryRow(ctx, `SELECT relation, method FROM claim_relations WHERE LEAST(from_claim, to_claim) = $1 AND GREATEST(from_claim, to_claim) = $2`,
		min(a, b), max(a, b)).Scan(&relation, &method)
	if relation != store.RelContradicts || method != store.LinkHuman {
		t.Errorf("relation = %s by %s", relation, method)
	}
	if n, _ := ix.SyncClaims(ctx, st); n.Links != 0 {
		t.Errorf("the log was read again: %+v", n)
	}

	// Filling in a failed chunk keeps the other claims' ids.
	e.Claims = append(e.Claims, store.ExtractedClaim{Text: "A third claim.", Quote: "The effect faded within two weeks", Chunk: 2})
	e.Failed, e.Attempts = nil, 2
	if err := st.PutExtraction(e); err != nil {
		t.Fatal(err)
	}
	if s, err := ix.SyncClaims(ctx, st); err != nil || s.Extractions != 2 || s.Claims != 10 {
		t.Fatalf("re-extraction sync = %+v, %v", s, err)
	}
	cs2 := claimsOf(t, ix, goDev)
	if len(cs2) != 5 || cs2[0].id != cs[0].id || cs2[3].id != cs[3].id {
		t.Errorf("ids moved: %+v", cs2)
	}

	// A rebuild gives the same claims, links and origins.
	before := snapshotClaims(t, ix)
	if _, err := ix.Rebuild(ctx, st); err != nil {
		t.Fatal(err)
	}
	if after := snapshotClaims(t, ix); after != before {
		t.Errorf("rebuild changed claims:\n%s\nwas\n%s", after, before)
	}
}

// snapshotClaims summarizes the claim tables for comparing across a
// rebuild.
func snapshotClaims(t *testing.T, ix *Index) string {
	t.Helper()
	var s string
	err := ix.pool.QueryRow(context.Background(), `SELECT
		(SELECT string_agg(id::text || ':' || coalesce(char_start, -1) || ':' || coalesce(passage_id, 0), ',' ORDER BY id) FROM claims) || ' | ' ||
		(SELECT string_agg(from_claim || '>' || to_claim || ':' || relation || ':' || method, ',' ORDER BY from_claim, to_claim, method) FROM claim_links) || ' | ' ||
		(SELECT string_agg(document_id || '>' || origin_id || ':' || reason, ',' ORDER BY document_id) FROM origins)`).Scan(&s)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSyncClaims_BadFileIsReportedNotFatal(t *testing.T) {
	ix, _ := openIndex(t)
	ctx := context.Background()
	st := newStore(t)
	goDev, _, _ := claimRuns(t, ix, st)
	if err := st.PutExtraction(docTextExtraction()); err != nil {
		t.Fatal(err)
	}
	files, _ := st.Extractions()
	if err := os.WriteFile(files[0].Path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	stats, err := ix.SyncClaims(ctx, st)
	if err != nil || len(stats.Failed) != 1 || stats.Extractions != 0 {
		t.Fatalf("stats = %+v, %v", stats, err)
	}
	if cs := claimsOf(t, ix, goDev); len(cs) != 0 {
		t.Errorf("claims from a bad file: %+v", cs)
	}
}

func TestAsOfDate(t *testing.T) {
	tests := []struct{ in, date, precision string }{
		{"2024", "2024-01-01", "year"},
		{"2024-03", "2024-03-01", "month"},
		{" 2024-03-15 ", "2024-03-15", "day"},
		{"2024-02-30", "", ""},
		{"March 2024", "", ""},
		{"", "", ""},
	}
	for _, tc := range tests {
		d, p := asOfDate(tc.in)
		got, gotP := "", ""
		if d != nil {
			got, gotP = d.(time.Time).Format("2006-01-02"), p.(string)
		}
		if got != tc.date || gotP != tc.precision {
			t.Errorf("asOfDate(%q) = %s %s, want %s %s", tc.in, got, gotP, tc.date, tc.precision)
		}
	}
}

func TestSimHashAndNearDuplicates(t *testing.T) {
	// Varied text, as real documents are: a repeated sentence has so few
	// distinct shingles that one edit can tip many bits.
	base, unrelated := variedText(1, 600), variedText(2, 600)
	edited := strings.Replace(base, " ", " inserted ", 1)
	h1, h2, h3 := SimHash(base), SimHash(edited), SimHash(unrelated)
	if h1 != SimHash(base) || SimHash("") != 0 || SimHash("two words") == 0 {
		t.Error("SimHash isn't deterministic or mishandles short text")
	}
	pairs := nearDuplicates(func(yield func(int64, uint64)) {
		yield(30, h1)
		yield(10, h2)
		yield(20, h3)
	})
	if len(pairs) != 1 || pairs[0] != [2]int64{10, 30} {
		t.Errorf("pairs = %v (hamming base/edited %d, base/unrelated %d)", pairs, popcount(h1^h2), popcount(h1^h3))
	}
}

// variedText is n pseudo-random words from a fixed vocabulary.
func variedText(seed uint32, n int) string {
	vocab := strings.Fields("court ruling appeal statute vaccine trial cohort placebo inflation wage rent zoning carbon grid " +
		"battery solar privacy breach model dataset benchmark harbour tide pilot river bank policy rate index survey")
	words := make([]string, n)
	for i := range words {
		seed = seed*1664525 + 1013904223
		words[i] = vocab[seed>>16%uint32(len(vocab))]
	}
	return strings.Join(words, " ")
}

func popcount(x uint64) int {
	n := 0
	for ; x != 0; x &= x - 1 {
		n++
	}
	return n
}

func TestEmbedClaims_EmbedsAndCaches(t *testing.T) {
	ix, _ := openIndex(t)
	ctx := context.Background()
	st := newStore(t)
	claimRuns(t, ix, st)
	if err := st.PutExtraction(docTextExtraction()); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.SyncClaims(ctx, st); err != nil {
		t.Fatal(err)
	}
	emb := &fakeEmbedder{model: "fake", dims: Dims}
	stats, err := ix.EmbedClaims(ctx, st, emb)
	if err != nil || stats.Embedded+stats.FromCache != 8 || stats.Remaining != 0 {
		t.Fatalf("stats = %+v, %v", stats, err)
	}
	// A rebuild reattaches the cached vectors without the model.
	ix.SetEmbedModel("fake")
	if _, err := ix.Rebuild(ctx, st); err != nil {
		t.Fatal(err)
	}
	var missing int
	ix.pool.QueryRow(ctx, `SELECT count(*) FROM claims WHERE embedding IS NULL`).Scan(&missing)
	if missing != 0 {
		t.Errorf("%d claims lost their vector across a rebuild", missing)
	}
	calls := emb.calls
	if again, err := ix.EmbedClaims(ctx, st, emb); err != nil || again.Embedded != 0 || emb.calls != calls {
		t.Errorf("re-embedding after rebuild: %+v, %v", again, err)
	}
}

func TestClaimCandidates_CitedFirstAndCompleteSkipped(t *testing.T) {
	ix, _ := openIndex(t)
	ctx := context.Background()
	st := newStore(t)
	_, _, other := claimRuns(t, ix, st)
	otherSHA := store.HashText(strings.Repeat("Wholly unrelated text about tide tables and harbour pilots. ", 30))
	dir := store.ExtractorDir(testExtractor)

	got, err := ix.ClaimCandidates(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	// docText is two documents but one text; the 17-character abstract is
	// too short. Uncited, the newest fetch comes first.
	if len(got) != 2 || got[0].TextSHA != otherSHA || got[1].TextSHA != store.HashText(docText) || got[0].Cited || got[0].Title != "Tides" {
		t.Fatalf("candidates = %+v", got)
	}

	// A report citing docText's source puts it first.
	run := sampleRun(t, st)
	if err := store.WriteCitations(run.Dir(), []store.Citation{{Ord: 1, Marker: "S:x", TargetKind: "source",
		TargetID: fmt.Sprint(SourceID("go.dev/blog/go1.18")), Group: 1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Sync(ctx, st); err != nil {
		t.Fatal(err)
	}
	got, _ = ix.ClaimCandidates(ctx, dir)
	if len(got) != 2 || got[0].TextSHA != store.HashText(docText) || !got[0].Cited || got[1].Cited {
		t.Fatalf("after citing = %+v", got)
	}

	// A complete extraction takes a text off the list; an incomplete one
	// doesn't.
	e := docTextExtraction()
	e.Failed = nil
	if err := st.PutExtraction(e); err != nil {
		t.Fatal(err)
	}
	if err := st.PutExtraction(store.Extraction{TextSHA: otherSHA, Extractor: testExtractor, Chunks: 1, Attempts: 1,
		Failed: []store.ChunkError{{Chunk: 0, Error: "bad JSON"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.SyncClaims(ctx, st); err != nil {
		t.Fatal(err)
	}
	got, _ = ix.ClaimCandidates(ctx, dir)
	if len(got) != 1 || got[0].TextSHA != otherSHA {
		t.Fatalf("after extracting = %+v (other doc %d)", got, other)
	}
}

func TestClaimCountsAndPendingExtractions(t *testing.T) {
	ix, _ := openIndex(t)
	ctx := context.Background()
	st := newStore(t)
	claimRuns(t, ix, st)
	ix.SetEmbedModel("fake")
	for _, e := range []store.Extraction{docTextExtraction(), {TextSHA: store.HashText("not fetched yet"), Extractor: testExtractor}} {
		if err := st.PutExtraction(e); err != nil {
			t.Fatal(err)
		}
	}
	if pending, waiting, err := ix.PendingExtractions(ctx, st); err != nil || pending != 1 || waiting != 1 {
		t.Fatalf("before sync: pending %d, waiting %d, %v", pending, waiting, err)
	}
	if _, err := ix.SyncClaims(ctx, st); err != nil {
		t.Fatal(err)
	}
	if pending, waiting, err := ix.PendingExtractions(ctx, st); err != nil || pending != 0 || waiting != 1 {
		t.Errorf("after sync: pending %d, waiting %d, %v", pending, waiting, err)
	}
	// The text's two documents each get its 4 claims, 3 with their quote found.
	if c, err := ix.ClaimCounts(ctx); err != nil || c != (ClaimCounts{Claims: 8, Unverified: 2, Unembedded: 8, Links: 3}) {
		t.Errorf("counts = %+v, %v", c, err)
	}
	if _, err := ix.EmbedClaims(ctx, st, &fakeEmbedder{model: "fake", dims: Dims}); err != nil {
		t.Fatal(err)
	}
	if c, _ := ix.ClaimCounts(ctx); c.Unembedded != 0 || c.Unlinked != 6 {
		t.Errorf("after embedding = %+v", c)
	}
}
