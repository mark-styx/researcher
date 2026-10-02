package retrieve

import (
	"context"
	"errors"
	"hash/fnv"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/marklubin/researchguy/internal/store"
	"github.com/marklubin/researchguy/internal/store/index"
	"github.com/marklubin/researchguy/internal/store/index/indextest"
)

// concepts maps words to a shared dimension, so the fake embedder puts
// synonyms near each other and the vector arm finds what full text can't.
var concepts = map[string]int{
	"cat": 0, "cats": 0, "feline": 0, "felines": 0, "kitten": 0,
	"dog": 1, "dogs": 1, "canine": 1, "canines": 1, "puppy": 1,
	"stock": 2, "stocks": 2, "market": 2, "markets": 2, "equity": 2,
	"animal": 3, "animals": 3, "mammal": 3, "mammals": 3,
}

// fakeEmbedder is a bag of concepts: known words add to their concept's
// dimension, others to a hashed one past the concepts.
type fakeEmbedder struct {
	err   error
	model string
}

func (f fakeEmbedder) Model() string {
	if f.model != "" {
		return f.model
	}
	return "fake-embed"
}

func vectorOf(text string) []float32 {
	v := make([]float32, index.Dims)
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) }) {
		if d, ok := concepts[w]; ok {
			v[d] += 4
			continue
		}
		h := fnv.New32a()
		h.Write([]byte(w))
		v[16+int(h.Sum32()%uint32(index.Dims-16))] += 0.05
	}
	var n float64
	for _, x := range v {
		n += float64(x) * float64(x)
	}
	if n == 0 {
		v[index.Dims-1] = 1
		return v
	}
	for i := range v {
		v[i] = float32(float64(v[i]) / math.Sqrt(n))
	}
	return v
}

func (f fakeEmbedder) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = vectorOf(t)
	}
	return out, nil
}

func (f fakeEmbedder) EmbedQuery(_ context.Context, text string) ([]float32, error) {
	if f.err != nil {
		return nil, f.err
	}
	return vectorOf(text), nil
}

// filler is varied prose with none of the concept words, so a paragraph
// is its own passage and reads as ordinary text.
const filler = "This ordinary filler prose describes nothing in particular. Rivers bend around old stone bridges while travelers count " +
	"lanterns along the northern road. Bakers open shutters before dawn, and the smell of rye drifts through narrow lanes. A clockmaker " +
	"repairs brass gears beside a window crowded with ferns. Children trade painted marbles near the fountain, arguing about rules nobody " +
	"wrote down. Later, rain softens the dust on wooden benches, and a violinist practices scales under the arcade. Librarians stamp " +
	"returned atlases, sailors mend nets on the quay, and gardeners prune quince trees behind the chapel. By evening the harbor lights " +
	"flicker, ferries depart for distant islands, and the town settles into quiet conversation over tea and bread."

// para is a paragraph of filler after the words that matter.
func para(words string) string { return words + ". " + filler }

type doc struct {
	url       string
	text      string
	kind      string // store.KindFull or KindAbstract
	published *store.Published
	at        time.Time
}

var (
	t2025 = time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC)
	t2026 = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	now   = time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

	catDoc = doc{url: "https://zoo.example/cats", text: para("Cats are small carnivorous mammals and the cat is a popular animal"),
		published: &store.Published{Date: "2020-01-15", Precision: "day", From: "citation_publication_date"}, at: t2025}
	dogDoc = doc{url: "https://zoo.example/dogs", text: para("Dogs are loyal and the dog is a popular animal"),
		published: &store.Published{Date: "2024-06", Precision: "month", From: "jsonld"}, at: t2026}
	marketDoc = doc{url: "https://news.example.com/markets", text: para("Stock markets fell as equity prices dropped"), at: t2026}
	padDoc    = doc{url: "https://figures.example/fig", at: t2026,
		text: "cat cats " + strings.Repeat("<pad> ", 300)}
	longCat = doc{url: "https://zoo.example/cat-book", at: t2025, kind: store.KindAbstract,
		text: para("The cat chapter one") + "\n\n" + para("The cat chapter two") + "\n\n" + para("The cat chapter three") + "\n\n" + para("The cat chapter four")}
)

// fixture is an index over runs that fetched the docs, plus a report.
type fixture struct {
	r       *Retriever
	ix      *index.Index
	st      *store.Store
	runA    string // fetched cat, dog, long cat
	runB    string // fetched market, pad; wrote the report
	byURL   map[string]int64
	reportD int64
}

func fetchRun(t *testing.T, st *store.Store, docs ...doc) *store.Run {
	t.Helper()
	run, err := st.StartRun(store.RunRecord{Kind: "dive", Topic: "animals"})
	if err != nil {
		t.Fatal(err)
	}
	log, err := store.OpenFetchLog(run.Dir())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range docs {
		sha, err := st.PutText(d.text)
		if err != nil {
			t.Fatal(err)
		}
		kind := d.kind
		if kind == "" {
			kind = store.KindFull
		}
		if _, err := log.Append(store.FetchRecord{URL: d.url, Reason: store.ReasonCited, Via: "direct", AttemptedAt: d.at, Attempts: 1,
			HTTPStatus: 200, ContentType: "text/html", TextSHA256: sha, TextChars: len([]rune(d.text)), ContentKind: kind,
			Title: "Title of " + d.url, Published: d.published}); err != nil {
			t.Fatal(err)
		}
	}
	if err := log.WriteSummary(store.FetchSummary{Records: len(docs)}); err != nil {
		t.Fatal(err)
	}
	log.Close()
	return run
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	ix, err := index.Open(ctx, indextest.DSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ix.Close)
	st, err := store.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	a := fetchRun(t, st, catDoc, dogDoc, longCat)
	if err := a.Finish(store.Finish{Status: store.StatusSucceeded}); err != nil {
		t.Fatal(err)
	}
	b := fetchRun(t, st, marketDoc, padDoc)
	report := filepath.Join(t.TempDir(), "report.md")
	if err := os.WriteFile(report, []byte("# Animals\n\n"+para("Our synthesis: the feline is an aloof animal")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := b.Finish(store.Finish{Status: store.StatusSucceeded, ReportPath: report}); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Sync(ctx, st); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Embed(ctx, st, fakeEmbedder{}); err != nil {
		t.Fatal(err)
	}
	f := &fixture{ix: ix, st: st, runA: a.ID(), runB: b.ID(), byURL: map[string]int64{},
		r: &Retriever{Index: ix, Store: st, Embed: fakeEmbedder{}, Now: func() time.Time { return now }}}
	rows, _ := ix.Pool().Query(ctx, `SELECT s.url, d.id FROM documents d JOIN sources s ON s.id = d.source_id`)
	for rows.Next() {
		var u string
		var id int64
		rows.Scan(&u, &id)
		f.byURL[u] = id
	}
	rows.Close()
	f.reportD = f.byURL[index.ReportKey(b.ID())]
	if f.reportD == 0 || len(f.byURL) != 6 {
		t.Fatalf("fixture documents = %v", f.byURL)
	}
	return f
}

func urls(cards []Card) []string {
	var out []string
	for _, c := range cards {
		out = append(out, c.URL)
	}
	return out
}

func TestFind_HybridFindsWhatFullTextMisses(t *testing.T) {
	f := newFixture(t)
	res, err := f.r.Find(context.Background(), Query{Text: "feline", Kinds: []string{KindPassage}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != ModeHybrid || res.EmbedModel != "fake-embed" || res.Note != "" {
		t.Errorf("mode = %s %s %q", res.Mode, res.EmbedModel, res.Note)
	}
	if len(res.Cards) == 0 || res.Cards[0].URL != catDoc.url && res.Cards[0].URL != longCat.url {
		t.Fatalf("top cards = %v, want the cat documents", urls(res.Cards))
	}
	top := res.Cards[0]
	if top.TextRank != 0 || top.VectorRank == 0 {
		t.Errorf("ranks = text %d vector %d, want a vector-only hit", top.TextRank, top.VectorRank)
	}
	for _, c := range res.Cards {
		if c.Kind != KindPassage || c.Domain == "researchguy" {
			t.Errorf("passage-only find returned %+v", c)
		}
	}
}

func TestFind_CardCarriesSourceAndDates(t *testing.T) {
	f := newFixture(t)
	res, err := f.r.Find(context.Background(), Query{Text: "carnivorous", Limit: 1})
	if err != nil || len(res.Cards) != 1 {
		t.Fatalf("find = %+v, %v", res, err)
	}
	c := res.Cards[0]
	if c.URL != catDoc.url || c.Domain != "zoo.example" || c.Title != "Title of "+catDoc.url || c.SourceKind != "web" || c.ContentKind != store.KindFull {
		t.Errorf("card = %+v", c)
	}
	if c.Ref != PassageRef(c.PassageID) || c.DocumentID != f.byURL[catDoc.url] || !strings.Contains(c.Text, "carnivorous") {
		t.Errorf("card ids/text = %+v", c)
	}
	if c.Published == nil || c.Published.Date != "2020-01-15" || c.Published.Precision != "day" || c.Published.From != "citation_publication_date" {
		t.Errorf("published = %+v", c.Published)
	}
	if !c.Collected.Equal(t2025) || len(c.Runs) != 1 || c.Runs[0] != f.runA {
		t.Errorf("collected %s runs %v", c.Collected, c.Runs)
	}
	if c.Age != "published 2020-01-15 (6y ago); collected 2025-03-01" {
		t.Errorf("age = %q", c.Age)
	}

	und, _ := f.r.Find(context.Background(), Query{Text: "equity", Limit: 1})
	if len(und.Cards) != 1 || und.Cards[0].Published != nil || und.Cards[0].Age != "undated; collected 2026-09-01" {
		t.Errorf("undated card = %+v", und.Cards)
	}
	abs, _ := f.r.Find(context.Background(), Query{Text: "chapter", Limit: 1})
	if len(abs.Cards) != 1 || abs.Cards[0].ContentKind != store.KindAbstract {
		t.Errorf("abstract card = %+v", abs.Cards)
	}
}

func TestFind_ReportsAreLabeledSynthesis(t *testing.T) {
	f := newFixture(t)
	res, err := f.r.Find(context.Background(), Query{Text: "aloof synthesis", Kinds: []string{KindReport}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Cards) != 1 || res.Cards[0].Kind != KindReport || res.Cards[0].DocumentID != f.reportD || res.Cards[0].SourceKind != "report" ||
		len(res.Cards[0].Runs) != 1 || res.Cards[0].Runs[0] != f.runB {
		t.Errorf("report cards = %+v", res.Cards)
	}
	both, _ := f.r.Find(context.Background(), Query{Text: "aloof animal"})
	kinds := map[string]bool{}
	for _, c := range both.Cards {
		kinds[c.Kind] = true
	}
	if !kinds[KindReport] || !kinds[KindPassage] {
		t.Errorf("unfiltered find kinds = %v", kinds)
	}
}

func TestFind_Filters(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	day := func(s string) *time.Time { d, _ := time.Parse(time.DateOnly, s); return &d }
	cases := []struct {
		name string
		q    Query
		want []string // the documents' URLs, in any order
	}{
		// A report is published the day it's written.
		{"published since leaves out undated", Query{Since: day("2021-01-01")}, []string{dogDoc.url, index.ReportKey(f.runB)}},
		{"published until", Query{Until: day("2021-01-01")}, []string{catDoc.url}},
		{"collected since", Query{DateField: DateCollected, Since: day("2026-01-01")}, []string{dogDoc.url, marketDoc.url, padDoc.url, index.ReportKey(f.runB)}},
		{"as of", Query{AsOf: day("2025-12-31")}, []string{catDoc.url, longCat.url}},
		{"run", Query{RunID: f.runB}, []string{marketDoc.url, padDoc.url, index.ReportKey(f.runB)}},
		{"domain and subdomains", Query{Domain: "www.example.com"}, []string{marketDoc.url}},
		{"exact domain", Query{Domain: "zoo.example"}, []string{catDoc.url, dogDoc.url, longCat.url}},
	}
	// Every document has the filler, so a query for it matches them all.
	for _, c := range cases {
		c.q.Text, c.q.Limit = "ordinary filler prose cat", 100
		res, err := f.r.Find(ctx, c.q)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		got := map[string]bool{}
		for _, card := range res.Cards {
			got[card.URL] = true
		}
		want := map[string]bool{}
		for _, u := range c.want {
			want[u] = true
		}
		if len(got) != len(want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
			continue
		}
		for u := range want {
			if !got[u] {
				t.Errorf("%s: got %v, want %v", c.name, got, c.want)
				break
			}
		}
	}
}

func TestFind_RankingAdjustments(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// One document gives at most two cards.
	res, err := f.r.Find(ctx, Query{Text: "cat chapter", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	per := map[int64]int{}
	for _, c := range res.Cards {
		per[c.DocumentID]++
	}
	if per[f.byURL[longCat.url]] != perDocument {
		t.Errorf("long document gave %d cards, want %d", per[f.byURL[longCat.url]], perDocument)
	}

	// The padding passage matches "cat cats" twice but ranks under prose.
	res, _ = f.r.Find(ctx, Query{Text: "cats", Kinds: []string{KindPassage}, Limit: 20})
	pos := map[string]int{}
	for i, c := range res.Cards {
		if _, ok := pos[c.URL]; !ok {
			pos[c.URL] = i
		}
	}
	if p, ok := pos[padDoc.url]; !ok || p < pos[catDoc.url] {
		t.Errorf("padding at %d, cat article at %d; want the padding below", p, pos[catDoc.url])
	}

	// Recency is off by default; prefer_recent lifts the newer document.
	old, _ := f.r.Find(ctx, Query{Text: "popular animal", Kinds: []string{KindPassage}, Domain: "zoo.example"})
	recent, _ := f.r.Find(ctx, Query{Text: "popular animal", Kinds: []string{KindPassage}, Domain: "zoo.example", PreferRecent: 365 * 24 * time.Hour})
	if len(old.Cards) < 2 || len(recent.Cards) < 2 {
		t.Fatalf("cards = %v / %v", urls(old.Cards), urls(recent.Cards))
	}
	if recent.Cards[0].URL != dogDoc.url {
		t.Errorf("prefer_recent top = %s, want the 2024 document", recent.Cards[0].URL)
	}
	if recent.Cards[0].Score >= old.Cards[0].Score {
		t.Errorf("recency weighting didn't lower scores: %v vs %v", recent.Cards[0].Score, old.Cards[0].Score)
	}
}

func TestFind_FullTextOnly(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	down := &Retriever{Index: f.ix, Store: f.st, Embed: fakeEmbedder{err: errors.New("connection refused")}}
	res, err := down.Find(ctx, Query{Text: "carnivorous"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != ModeFullText || !strings.Contains(res.Note, "connection refused") || len(res.Cards) != 1 || res.Cards[0].URL != catDoc.url {
		t.Errorf("degraded find = %s %q %v", res.Mode, res.Note, urls(res.Cards))
	}
	none := &Retriever{Index: f.ix, Store: f.st}
	res, _ = none.Find(ctx, Query{Text: "feline", Kinds: []string{KindPassage}})
	if res.Mode != ModeFullText || !strings.Contains(res.Note, "no embedding model") || len(res.Cards) != 0 {
		t.Errorf("no-embedder find = %s %q %v", res.Mode, res.Note, urls(res.Cards))
	}
	// A model nothing was embedded with finds by full text alone.
	other := &Retriever{Index: f.ix, Store: f.st, Embed: fakeEmbedder{model: "other"}}
	res, _ = other.Find(ctx, Query{Text: "feline", Kinds: []string{KindPassage}})
	if res.Mode != ModeHybrid || len(res.Cards) != 0 {
		t.Errorf("other-model find = %s %v", res.Mode, urls(res.Cards))
	}
}

func TestFind_BadQueries(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	since, until := now, now.Add(-time.Hour)
	for name, q := range map[string]Query{
		"empty":         {Text: "  "},
		"kind":          {Text: "x", Kinds: []string{"tweet"}},
		"date field":    {Text: "x", DateField: "modified"},
		"until < since": {Text: "x", Since: &since, Until: &until},
		"recency":       {Text: "x", PreferRecent: -time.Hour},
	} {
		if _, err := f.r.Find(ctx, q); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	for name, q := range map[string]Query{
		"min_similarity": {Text: "x", MinSimilarity: 1.5},
	} {
		if _, err := f.r.Find(ctx, q); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	// Full text matches nothing; the vector arm still has nearest
	// passages, with low similarity, unless min_similarity cuts them.
	ft := &Retriever{Index: f.ix, Store: f.st}
	if res, err := ft.Find(ctx, Query{Text: "zzzqqq"}); err != nil || res.Cards == nil || len(res.Cards) != 0 {
		t.Errorf("no full-text match = %+v, %v; want an empty, non-nil list", res, err)
	}
	near, _ := f.r.Find(ctx, Query{Text: "zzzqqq"})
	if len(near.Cards) == 0 || near.Cards[0].VectorRank == 0 || near.Cards[0].Similarity > 0.5 {
		t.Errorf("nonsense query cards = %v", near.Cards)
	}
	if res, _ := f.r.Find(ctx, Query{Text: "zzzqqq", MinSimilarity: 0.5}); len(res.Cards) != 0 {
		t.Errorf("min_similarity kept %v", urls(res.Cards))
	}
	if res, _ := f.r.Find(ctx, Query{Text: "feline", Kinds: []string{KindPassage}, MinSimilarity: 0.5}); len(res.Cards) == 0 || res.Cards[0].Similarity < 0.5 {
		t.Errorf("min_similarity dropped a close match: %v", res.Cards)
	}
}

func TestInformative(t *testing.T) {
	if got := informative("short text"); got != 1 {
		t.Errorf("short = %v", got)
	}
	if got := informative(para("A normal paragraph of reasonably varied english words")); got < 0.5 {
		t.Errorf("prose = %v", got)
	}
	if got := informative(strings.Repeat("<pad> ", 200)); got != 0.2 {
		t.Errorf("padding = %v, want the floor", got)
	}
}

func TestParseRef(t *testing.T) {
	if k, id, err := ParseRef(" P:42 "); err != nil || k != RefPassage || id != 42 {
		t.Errorf("P:42 = %s %d %v", k, id, err)
	}
	if k, id, err := ParseRef("S:7"); err != nil || k != RefSource || id != 7 {
		t.Errorf("S:7 = %s %d %v", k, id, err)
	}
	for _, bad := range []string{"E3", "P:", "P:x", "Q:1", "P:-1", ""} {
		if _, _, err := ParseRef(bad); err == nil {
			t.Errorf("ParseRef(%q) succeeded", bad)
		}
	}
	if PassageRef(5) != "P:5" || SourceRef(6) != "S:6" {
		t.Error("ref formatting")
	}
}

func TestSince(t *testing.T) {
	for _, c := range []struct {
		t    time.Time
		want string
	}{
		{now.Add(-3 * 24 * time.Hour), "3d ago"},
		{now.Add(-90 * 24 * time.Hour), "3mo ago"},
		{now.Add(-3 * 365 * 24 * time.Hour), "3y ago"},
		{now.Add(48 * time.Hour), "future date"},
	} {
		if got := since(c.t, now); got != c.want {
			t.Errorf("since(%s) = %q, want %q", c.t, got, c.want)
		}
	}
}

func TestParseBound(t *testing.T) {
	for _, c := range []struct {
		in   string
		end  bool
		want string
	}{
		{"2020", false, "2020-01-01T00:00:00Z"},
		{"2020", true, "2020-12-31T23:59:59.999999999Z"},
		{"2020-02", true, "2020-02-29T23:59:59.999999999Z"},
		{"2020-02-03", false, "2020-02-03T00:00:00Z"},
		{"2020-02-03", true, "2020-02-03T23:59:59.999999999Z"},
		{"2021-05-06T07:08:09Z", true, "2021-05-06T07:08:09Z"},
		{"30d", false, "2026-09-02T12:00:00Z"},
		{"1y", true, "2025-10-02T12:00:00Z"},
	} {
		got, err := ParseBound(c.in, now, c.end)
		if err != nil || got.Format(time.RFC3339Nano) != c.want {
			t.Errorf("ParseBound(%q, end=%v) = %s, %v; want %s", c.in, c.end, got.Format(time.RFC3339Nano), err, c.want)
		}
	}
	for _, bad := range []string{"", "yesterday", "2020-13", "0d", "-5d", "5x"} {
		if _, err := ParseBound(bad, now, false); err == nil {
			t.Errorf("ParseBound(%q) succeeded", bad)
		}
	}
	if d, err := ParseAge("2w"); err != nil || d != 14*24*time.Hour {
		t.Errorf("ParseAge(2w) = %v, %v", d, err)
	}
}

func TestArgs_Query(t *testing.T) {
	q, err := Args{Since: "2020", Until: "2021-06", AsOf: "30d", PreferRecent: "1y", Kinds: []string{"passage"}, RunID: " r1 ", Limit: 3}.Query("q", now)
	if err != nil {
		t.Fatal(err)
	}
	if q.Text != "q" || q.Since.Format(time.DateOnly) != "2020-01-01" || q.Until.Format(time.DateOnly) != "2021-06-30" ||
		q.AsOf.Format(time.DateOnly) != "2026-09-02" || q.PreferRecent != 365*24*time.Hour || q.RunID != "r1" || q.Limit != 3 || q.Kinds[0] != "passage" {
		t.Errorf("query = %+v", q)
	}
	for _, a := range []Args{{Since: "soon"}, {Until: "2020-13"}, {AsOf: "x"}, {PreferRecent: "fast"}} {
		if _, err := a.Query("q", now); err == nil {
			t.Errorf("%+v accepted", a)
		}
	}
}
