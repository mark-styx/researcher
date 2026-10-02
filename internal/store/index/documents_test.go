package index

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marklubin/researchguy/internal/store"
	"github.com/pgvector/pgvector-go"
)

// docText is three ~1,000 character paragraphs, a passage each.
var docText = strings.Join([]string{
	strings.Repeat("Exposure shifted opinion by a tenth of a standard deviation. ", 16),
	strings.Repeat("The effect faded within two weeks of the last exposure. ", 18),
	strings.Repeat("Replications in three countries found the same pattern. ", 18),
}, "\n\n")

// fullPassages is how many passages docText makes; total adds the abstract.
var fullPassages = len(store.Passages(docText))
var total = fullPassages + 1

// fetchedRun is sampleRun plus a fetch log: go.dev's page fetched with a
// day date, the doi as an OpenAlex abstract, a cited URL no capture has,
// and a failure.
func fetchedRun(t *testing.T, st *store.Store) *store.Run {
	t.Helper()
	run := sampleRun(t, st)
	full, err := st.PutText(docText)
	if err != nil {
		t.Fatal(err)
	}
	abstract, _ := st.PutText("A short abstract.")
	at := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	writeFetches(t, run,
		store.FetchRecord{URL: "https://go.dev/blog/go1.18", Reason: store.ReasonOpened, Via: "direct", AttemptedAt: at, Attempts: 1,
			HTTPStatus: 200, ContentType: "text/html", RawSHA256: full, RawBytes: 9000, TextSHA256: full, TextChars: len(docText),
			ContentKind: store.KindFull, Title: "Go 1.18 is released", Published: &store.Published{Date: "2022-03-15", Precision: "day", From: "article:published_time"}},
		store.FetchRecord{URL: "https://doi.org/10.1145/ABC.123", Reason: store.ReasonResult, Rank: 2, Via: "direct", AttemptedAt: at, Attempts: 3,
			HTTPStatus: 403, Error: "HTTP 403"},
		store.FetchRecord{URL: "https://doi.org/10.1145/ABC.123", Reason: store.ReasonResult, Rank: 2, Via: "openalex", FetchURL: "https://api.openalex.org/works/https://doi.org/10.1145/abc.123",
			AttemptedAt: at, Attempts: 1, HTTPStatus: 200, TextSHA256: abstract, TextChars: 17, ContentKind: store.KindAbstract,
			Title: "Paper", DOI: "10.1145/abc.123", Published: &store.Published{Date: "2019", Precision: "year", From: "openalex"}},
		store.FetchRecord{URL: "https://cited.example/only-in-report", Reason: store.ReasonCited, Via: "direct", AttemptedAt: at, Attempts: 1,
			Error: "dial tcp: no such host"},
	)
	return run
}

func writeFetches(t *testing.T, run *store.Run, recs ...store.FetchRecord) {
	t.Helper()
	log, err := store.OpenFetchLog(run.Dir())
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	for _, r := range recs {
		if _, err := log.Append(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := log.WriteSummary(store.FetchSummary{Records: len(recs)}); err != nil {
		t.Fatal(err)
	}
}

// fakeEmbedder makes a deterministic unit vector per text.
type fakeEmbedder struct {
	model string
	dims  int
	calls int
	texts int
	err   error
}

func (f *fakeEmbedder) Model() string { return f.model }

func (f *fakeEmbedder) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	f.texts += len(texts)
	out := make([][]float32, len(texts))
	for i, s := range texts {
		out[i] = vectorFor(f.model+s, f.dims)
	}
	return out, nil
}

func vectorFor(s string, dims int) []float32 {
	v := make([]float32, dims)
	sum := sha256.Sum256([]byte(s))
	var norm float64
	for i := range v {
		x := float32(binary.BigEndian.Uint16(sum[(2*i)%32:])) / 65535
		v[i] = x + 0.01
		norm += float64(v[i] * v[i])
	}
	for i := range v {
		v[i] /= float32(math.Sqrt(norm))
	}
	return v
}

func TestIngestRun_FetchesDocumentsPassages(t *testing.T) {
	ix, _ := openIndex(t)
	ctx := context.Background()
	st := newStore(t)
	run := fetchedRun(t, st)

	stats, err := ix.IngestRun(ctx, run.Dir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if fullPassages != 3 {
		t.Fatalf("docText makes %d passages; the test expects 3", fullPassages)
	}
	if stats.Fetches != 4 || stats.Documents != 2 || stats.Passages != total || stats.Sources != 4 || stats.MissingTexts != 0 {
		t.Errorf("stats = %+v, want 4 fetches, 2 documents, %d passages, 4 sources", stats, total)
	}

	var kind, precision, from, title string
	var published time.Time
	var weak bool
	if err := ix.pool.QueryRow(ctx, `SELECT d.content_kind, d.published_at, d.published_precision, d.published_from, d.published_weak, d.title
		FROM documents d JOIN sources s ON s.id = d.source_id WHERE s.url_key = 'go.dev/blog/go1.18'`).
		Scan(&kind, &published, &precision, &from, &weak, &title); err != nil {
		t.Fatal(err)
	}
	if kind != "full" || published.Format("2006-01-02") != "2022-03-15" || precision != "day" || from != "article:published_time" || weak || title != "Go 1.18 is released" {
		t.Errorf("go.dev document = %s %v %s %s %v %q", kind, published, precision, from, weak, title)
	}
	if err := ix.pool.QueryRow(ctx, `SELECT d.content_kind, d.published_at, d.published_precision FROM documents d
		JOIN sources s ON s.id = d.source_id WHERE s.domain = 'doi.org'`).Scan(&kind, &published, &precision); err != nil {
		t.Fatal(err)
	}
	if kind != "abstract" || published.Format("2006-01-02") != "2019-01-01" || precision != "year" {
		t.Errorf("doi document = %s %v %s; a year is stored as its first day", kind, published, precision)
	}

	// Source kinds: the doi is a paper, the rest are web.
	var srcKind string
	ix.pool.QueryRow(ctx, `SELECT kind FROM sources WHERE domain = 'doi.org'`).Scan(&srcKind)
	if srcKind != "paper" {
		t.Errorf("doi source kind = %q, want paper", srcKind)
	}
	// The cited URL is a source though no capture has it.
	var citedTitle string
	if err := ix.pool.QueryRow(ctx, `SELECT kind, title FROM sources WHERE url_key = 'cited.example/only-in-report'`).Scan(&srcKind, &citedTitle); err != nil || srcKind != "web" {
		t.Errorf("cited source = %q, %v", srcKind, err)
	}

	// Every attempt is a row, failures included; only successes link a document.
	var failed, linked, attempts int
	ix.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE error <> ''), count(document_id), max(attempts) FROM fetches WHERE run_id = $1`, run.ID()).
		Scan(&failed, &linked, &attempts)
	if failed != 2 || linked != 2 || attempts != 3 {
		t.Errorf("fetches: %d failed, %d linked, max attempts %d", failed, linked, attempts)
	}
	var fetchURL string
	ix.pool.QueryRow(ctx, `SELECT fetch_url FROM fetches WHERE run_id = $1 AND seq = 1`, run.ID()).Scan(&fetchURL)
	if fetchURL != "https://go.dev/blog/go1.18" {
		t.Errorf("fetch_url defaults to the URL: %q", fetchURL)
	}

	// Passages: offsets cut the document's text, ids are derived, full-text works.
	rows, err := ix.pool.Query(ctx, `SELECT p.id, p.document_id, p.char_start, p.char_end, p.text, p.text_sha256 FROM passages p
		JOIN documents d ON d.id = p.document_id WHERE d.content_kind = 'full' ORDER BY p.ord`)
	if err != nil {
		t.Fatal(err)
	}
	r := []rune(docText)
	n := 0
	for rows.Next() {
		var id, doc int64
		var start, end int
		var text, sha string
		if err := rows.Scan(&id, &doc, &start, &end, &text, &sha); err != nil {
			t.Fatal(err)
		}
		if string(r[start:end]) != text || store.HashText(text) != sha || id != PassageID(doc, start, end) {
			t.Errorf("passage %d: span %d-%d doesn't match its text or id", n, start, end)
		}
		n++
	}
	rows.Close()
	if n != fullPassages {
		t.Errorf("full document has %d passages, want %d", n, fullPassages)
	}
	var hits int
	ix.pool.QueryRow(ctx, `SELECT count(*) FROM passages WHERE tsv @@ plainto_tsquery('english', 'replications countries')`).Scan(&hits)
	if hits != 1 {
		t.Errorf("full-text hits = %d, want 1", hits)
	}

	c, _ := ix.Counts(ctx)
	if c.Fetches != 4 || c.Documents != 2 || c.Passages != int64(total) || c.Unembedded != int64(total) {
		t.Errorf("counts = %+v", c)
	}
}

func TestIngestRun_FetchesIdempotentAndShared(t *testing.T) {
	ix, _ := openIndex(t)
	ctx := context.Background()
	st := newStore(t)
	a := fetchedRun(t, st)
	if _, err := ix.IngestRun(ctx, a.Dir(), false); err != nil {
		t.Fatal(err)
	}
	if s, err := ix.IngestRun(ctx, a.Dir(), true); err != nil || s.Documents != 0 || s.Passages != 0 || s.Fetches != 4 {
		t.Errorf("forced re-ingest = %+v, %v; want nothing new", s, err)
	}
	// Another run fetching the same text adds fetch rows, not documents.
	b := fetchedRun(t, st)
	s, err := ix.IngestRun(ctx, b.Dir(), false)
	if err != nil || s.Documents != 0 || s.Passages != 0 {
		t.Errorf("second run = %+v, %v; want its documents already known", s, err)
	}
	c, _ := ix.Counts(ctx)
	if c.Fetches != 8 || c.Documents != 2 || c.Passages != int64(total) {
		t.Errorf("counts = %+v", c)
	}
	var first, last time.Time
	ix.pool.QueryRow(ctx, `SELECT first_fetched_at, last_fetched_at FROM documents WHERE content_kind = 'full'`).Scan(&first, &last)
	if last.Before(first) {
		t.Errorf("fetched %v..%v", first, last)
	}
}

func TestIngestRun_FetchLogGrowthIsPending(t *testing.T) {
	ix, _ := openIndex(t)
	ctx := context.Background()
	st := newStore(t)
	run := sampleRun(t, st)
	if _, err := ix.IngestRun(ctx, run.Dir(), false); err != nil {
		t.Fatal(err)
	}
	if p, _, _ := ix.Pending(ctx, st); len(p) != 0 {
		t.Fatalf("pending = %v before fetching", p)
	}
	text, _ := st.PutText(docText)
	writeFetches(t, run, store.FetchRecord{URL: "https://example.org/post", Reason: store.ReasonOpened, Via: "direct",
		AttemptedAt: time.Now(), Attempts: 1, HTTPStatus: 200, TextSHA256: text, TextChars: len(docText), ContentKind: store.KindFull})
	if p, _, _ := ix.Pending(ctx, st); len(p) != 1 || p[0] != run.ID() {
		t.Errorf("pending = %v after the fetch log grew", p)
	}
	s, err := ix.IngestRun(ctx, run.Dir(), false)
	if err != nil || s.Skipped || s.Documents != 1 {
		t.Errorf("ingest after fetching = %+v, %v", s, err)
	}
}

func TestIngestRun_MissingTextSplitLater(t *testing.T) {
	ix, _ := openIndex(t)
	ctx := context.Background()
	st := newStore(t)
	run := sampleRun(t, st)
	sha := store.HashText(docText) // never written
	writeFetches(t, run, store.FetchRecord{URL: "https://example.org/post", Reason: store.ReasonOpened, Via: "direct",
		AttemptedAt: time.Now(), Attempts: 1, HTTPStatus: 200, TextSHA256: sha, TextChars: len(docText), ContentKind: store.KindFull})
	s, err := ix.IngestRun(ctx, run.Dir(), false)
	if err != nil || s.MissingTexts != 1 || s.Documents != 1 || s.Passages != 0 {
		t.Fatalf("ingest = %+v, %v; want the document without passages", s, err)
	}
	if _, err := st.PutText(docText); err != nil {
		t.Fatal(err)
	}
	if s, err := ix.IngestRun(ctx, run.Dir(), true); err != nil || s.Passages != fullPassages || s.Documents != 0 {
		t.Errorf("re-ingest with the text = %+v, %v; want its passages", s, err)
	}
}

func TestIngestRun_WeakDateReplaced(t *testing.T) {
	ix, _ := openIndex(t)
	ctx := context.Background()
	st := newStore(t)
	text, _ := st.PutText(docText)
	rec := store.FetchRecord{URL: "https://example.org/post", Reason: store.ReasonOpened, Via: "direct", AttemptedAt: time.Now(), Attempts: 1,
		HTTPStatus: 200, TextSHA256: text, TextChars: len(docText), ContentKind: store.KindFull}
	weak := rec
	weak.Published = &store.Published{Date: "2025-06-01", Precision: "day", From: "last-modified", Weak: true}
	a := sampleRun(t, st)
	writeFetches(t, a, weak)
	if _, err := ix.IngestRun(ctx, a.Dir(), false); err != nil {
		t.Fatal(err)
	}
	strong := rec
	strong.Published = &store.Published{Date: "2021-11", Precision: "month", From: "citation_publication_date"}
	b := sampleRun(t, st)
	writeFetches(t, b, strong)
	if _, err := ix.IngestRun(ctx, b.Dir(), false); err != nil {
		t.Fatal(err)
	}
	// A later weak date doesn't replace a strong one.
	c := sampleRun(t, st)
	writeFetches(t, c, weak)
	if _, err := ix.IngestRun(ctx, c.Dir(), false); err != nil {
		t.Fatal(err)
	}
	var at time.Time
	var precision, from string
	var isWeak bool
	ix.pool.QueryRow(ctx, `SELECT published_at, published_precision, published_from, published_weak FROM documents`).Scan(&at, &precision, &from, &isWeak)
	if at.Format("2006-01-02") != "2021-11-01" || precision != "month" || from != "citation_publication_date" || isWeak {
		t.Errorf("published = %v %s %s weak=%v; want the strong month date", at, precision, from, isWeak)
	}
}

func TestEmbed_EmbedsCachesAndReembedsOnModelChange(t *testing.T) {
	ix, _ := openIndex(t)
	ctx := context.Background()
	st := newStore(t)
	run := fetchedRun(t, st)
	if _, err := ix.IngestRun(ctx, run.Dir(), false); err != nil {
		t.Fatal(err)
	}
	emb := &fakeEmbedder{model: "nomic-embed-text", dims: Dims}
	stats, err := ix.Embed(ctx, st, emb)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Embedded != total || stats.FromCache != 0 || stats.Remaining != 0 || stats.Model != "nomic-embed-text" {
		t.Errorf("Embed = %+v", stats)
	}
	// The vector round-trips through pgvector, and the store cached it.
	var id int64
	var text string
	var got pgvector.Vector
	if err := ix.pool.QueryRow(ctx, `SELECT id, text, embedding FROM passages ORDER BY id LIMIT 1`).Scan(&id, &text, &got); err != nil {
		t.Fatal(err)
	}
	want := vectorFor(emb.model+text, Dims)
	if len(got.Slice()) != Dims || got.Slice()[0] != want[0] || got.Slice()[Dims-1] != want[Dims-1] {
		t.Errorf("stored vector doesn't match the model's")
	}
	if v, ok, err := st.ReadVector(emb.model, store.HashText(text)); err != nil || !ok || v[5] != want[5] {
		t.Errorf("vector not cached: %v %v", ok, err)
	}
	// Cosine search finds a passage's own vector first.
	var nearest int64
	ix.pool.QueryRow(ctx, `SELECT id FROM passages ORDER BY embedding <=> $1 LIMIT 1`, pgvector.NewVector(want)).Scan(&nearest)
	if nearest != id {
		t.Errorf("nearest = %d, want %d", nearest, id)
	}
	if again, err := ix.Embed(ctx, st, emb); err != nil || again.Embedded != 0 || again.FromCache != 0 || emb.calls != 1 {
		t.Errorf("second Embed = %+v, %v, %d model calls; want nothing to do", again, err, emb.calls)
	}

	// Another model re-embeds everything; the counts follow the configured model.
	other := &fakeEmbedder{model: "other-model", dims: Dims}
	ix.SetEmbedModel(other.model)
	if c, _ := ix.Counts(ctx); c.Unembedded != int64(total) {
		t.Errorf("unembedded for another model = %d", c.Unembedded)
	}
	if s, err := ix.Embed(ctx, st, other); err != nil || s.Embedded != total || s.Remaining != 0 {
		t.Errorf("Embed with another model = %+v, %v", s, err)
	}
	if n, _ := ix.Unembedded(ctx, ""); n != 0 {
		t.Errorf("Unembedded(any) = %d", n)
	}
}

func TestEmbed_RebuildUsesCachedVectors(t *testing.T) {
	ix, _ := openIndex(t)
	ctx := context.Background()
	st := newStore(t)
	run := fetchedRun(t, st)
	ix.SetEmbedModel("nomic-embed-text")
	if _, err := ix.Sync(ctx, st); err != nil {
		t.Fatal(err)
	}
	emb := &fakeEmbedder{model: "nomic-embed-text", dims: Dims}
	if _, err := ix.Embed(ctx, st, emb); err != nil {
		t.Fatal(err)
	}
	var before []int64
	rows, _ := ix.pool.Query(ctx, `SELECT id FROM passages ORDER BY id`)
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		before = append(before, id)
	}
	rows.Close()

	stats, err := ix.Rebuild(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats.Ingested) != 1 || stats.Ingested[0].RunID != run.ID() || stats.Ingested[0].Embedded != total {
		t.Errorf("rebuild = %+v; want every passage's vector from the cache", stats.Ingested)
	}
	if c, _ := ix.Counts(ctx); c.Passages != int64(total) || c.Unembedded != 0 {
		t.Errorf("counts after rebuild = %+v", c)
	}
	var after []int64
	rows, _ = ix.pool.Query(ctx, `SELECT id FROM passages ORDER BY id`)
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		after = append(after, id)
	}
	rows.Close()
	if len(after) != len(before) || after[0] != before[0] || after[len(after)-1] != before[len(before)-1] {
		t.Errorf("passage ids changed across rebuild: %v vs %v", before, after)
	}
	// A cleared embedding is filled from the cache without calling the model.
	ix.pool.Exec(ctx, `UPDATE passages SET embedding = NULL, embed_model = NULL`)
	s, err := ix.Embed(ctx, st, emb)
	if err != nil || s.FromCache != total || s.Embedded != 0 || emb.calls != 1 {
		t.Errorf("Embed from cache = %+v, %v, %d calls", s, err, emb.calls)
	}
}

func TestEmbed_Errors(t *testing.T) {
	ix, _ := openIndex(t)
	ctx := context.Background()
	st := newStore(t)
	run := fetchedRun(t, st)
	if _, err := ix.IngestRun(ctx, run.Dir(), false); err != nil {
		t.Fatal(err)
	}
	wrong := &fakeEmbedder{model: "small", dims: 384}
	if _, err := ix.Embed(ctx, st, wrong); err == nil || !strings.Contains(err.Error(), "384-dimension") {
		t.Errorf("wrong dims err = %v", err)
	}
	if _, ok, _ := st.ReadVector("small", store.HashText(docText)); ok {
		t.Error("a wrong-size vector was cached")
	}
	down := &fakeEmbedder{model: "nomic-embed-text", dims: Dims, err: errors.New("connection refused")}
	s, err := ix.Embed(ctx, st, down)
	if err == nil || s.Remaining != int64(total) || s.Embedded != 0 {
		t.Errorf("model down = %+v, %v; want the error and everything remaining", s, err)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if s, err := ix.Embed(cctx, st, &fakeEmbedder{model: "nomic-embed-text", dims: Dims}); err == nil || s.Remaining != int64(total) {
		t.Errorf("cancelled Embed = %+v, %v", s, err)
	}
}

func TestDocumentAndPassageIDs(t *testing.T) {
	d := DocumentID("go.dev/x", "abc")
	if d <= 0 || d != DocumentID("go.dev/x", "abc") || d == DocumentID("go.dev/x", "abd") || d == DocumentID("go.dev/y", "abc") {
		t.Errorf("DocumentID not stable or not distinct")
	}
	p := PassageID(d, 0, 10)
	if p <= 0 || p != PassageID(d, 0, 10) || p == PassageID(d, 0, 11) {
		t.Errorf("PassageID not stable or not distinct")
	}
}

func TestSourceKind(t *testing.T) {
	doi := "10.1/x"
	cases := []struct {
		key  string
		doi  *string
		want string
	}{
		{"doi.org/10.1/x", &doi, "paper"},
		{"arxiv.org/abs/2401.1", nil, "paper"},
		{"pmc.ncbi.nlm.nih.gov/articles/PMC1", nil, "paper"},
		{"youtube.com/watch?v=x", nil, "video"},
		{"youtu.be/x", nil, "video"},
		{"courtlistener.com/opinion/1", nil, "court"},
		{"example.org/post", nil, "web"},
	}
	for _, c := range cases {
		if got := sourceKind(c.key, c.doi); got != c.want {
			t.Errorf("sourceKind(%q) = %q, want %q", c.key, got, c.want)
		}
	}
}

func TestPublishedCols(t *testing.T) {
	if at, _, _, _ := publishedCols(nil); at != nil {
		t.Error("nil date should be null")
	}
	if at, _, _, _ := publishedCols(&store.Published{Date: "2024-13", Precision: "month"}); at != nil {
		t.Error("bad month should be null")
	}
	if at, _, _, _ := publishedCols(&store.Published{Date: "2024", Precision: "decade"}); at != nil {
		t.Error("unknown precision should be null")
	}
	at, precision, from, weak := publishedCols(&store.Published{Date: "2024-05", Precision: "month", From: "x", Weak: true})
	if at.(time.Time).Format("2006-01-02") != "2024-05-01" || precision != "month" || from != "x" || !weak {
		t.Errorf("publishedCols = %v %v %v %v", at, precision, from, weak)
	}
}

// reportRun is a finished run whose report the store holds.
func reportRun(t *testing.T, st *store.Store, report string) *store.Run {
	t.Helper()
	run, err := st.StartRun(store.RunRecord{Kind: "dive", Topic: "go generics", Backend: "hybrid"})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "report.md")
	if err := os.WriteFile(path, []byte(report), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run.Finish(store.Finish{Status: store.StatusSucceeded, ReportPath: path}); err != nil {
		t.Fatal(err)
	}
	return run
}

func TestIngestRun_ReportIsASynthesisDocument(t *testing.T) {
	ix, _ := openIndex(t)
	ctx := context.Background()
	st := newStore(t)
	run := reportRun(t, st, "# Go generics\n\nGo 1.18 added type parameters [E1].")
	stats, err := ix.IngestRun(ctx, run.Dir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Documents != 1 || stats.Passages != 1 {
		t.Errorf("stats = %+v, want the report's document and passage", stats)
	}
	var origin, kind, domain, url, from string
	var docID int64
	var published time.Time
	err = ix.pool.QueryRow(ctx, `SELECT d.id, d.origin, s.kind, s.domain, s.url, d.published_from, d.published_at
		FROM runs r JOIN documents d ON d.id = r.report_document_id JOIN sources s ON s.id = d.source_id WHERE r.id = $1`, run.ID()).
		Scan(&docID, &origin, &kind, &domain, &url, &from, &published)
	if err != nil {
		t.Fatal(err)
	}
	rec := run.Record()
	if origin != "synthesis" || kind != "report" || domain != "researchguy" || url != ReportKey(run.ID()) || from != "run" ||
		docID != DocumentID(ReportKey(run.ID()), rec.ReportSHA256) || published.Format(time.DateOnly) != rec.FinishedAt.Format(time.DateOnly) {
		t.Errorf("report document = %d %s %s %s %s %s %s", docID, origin, kind, domain, url, from, published)
	}

	// Forced re-ingest and a rebuild find the same document.
	again, err := ix.IngestRun(ctx, run.Dir(), true)
	if err != nil || again.Documents != 0 || again.Passages != 0 {
		t.Errorf("re-ingest = %+v, %v", again, err)
	}
	if _, err := ix.Rebuild(ctx, st); err != nil {
		t.Fatal(err)
	}
	var rebuilt int64
	ix.pool.QueryRow(ctx, `SELECT report_document_id FROM runs WHERE id = $1`, run.ID()).Scan(&rebuilt)
	if rebuilt != docID {
		t.Errorf("after rebuild the run points at %d, want %d", rebuilt, docID)
	}

	// A report whose text left the store points at nothing.
	gone := reportRun(t, st, "# Another\n\nText.")
	sha := gone.Record().ReportSHA256
	if err := os.Remove(filepath.Join(st.Dir(), "text", "sha256", sha[:2], sha+".txt")); err != nil {
		t.Fatal(err)
	}
	stats, err = ix.IngestRun(ctx, gone.Dir(), false)
	if err != nil || stats.MissingTexts != 1 {
		t.Errorf("missing report text = %+v, %v", stats, err)
	}
	var none *int64
	ix.pool.QueryRow(ctx, `SELECT report_document_id FROM runs WHERE id = $1`, gone.ID()).Scan(&none)
	if none != nil {
		t.Errorf("run without its report text points at %d", *none)
	}
}
