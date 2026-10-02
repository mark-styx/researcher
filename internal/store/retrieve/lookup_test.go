package retrieve

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/marklubin/researchguy/internal/fetch"
	"github.com/marklubin/researchguy/internal/store"
	"github.com/marklubin/researchguy/internal/store/index"
)

func TestPassage_WithNeighbors(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	res, err := f.r.Find(ctx, Query{Text: "chapter two", Limit: 1})
	if err != nil || len(res.Cards) != 1 || !strings.Contains(res.Cards[0].Text, "chapter two") {
		t.Fatalf("find = %+v, %v", res.Cards, err)
	}
	p, err := f.r.Passage(ctx, res.Cards[0].PassageID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if p.Ord != 1 || p.URL != longCat.url || p.Age == "" || len(p.Before) != 1 || len(p.After) != 1 ||
		!strings.Contains(p.Before[0].Text, "chapter one") || !strings.Contains(p.After[0].Text, "chapter three") {
		t.Errorf("passage = ord %d before %v after %v", p.Ord, p.Before, p.After)
	}
	alone, _ := f.r.Passage(ctx, res.Cards[0].PassageID, 0)
	if alone.Before != nil || alone.After != nil {
		t.Errorf("no neighbors asked, got %v %v", alone.Before, alone.After)
	}
	if _, err := f.r.Passage(ctx, 12345, 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing passage err = %v", err)
	}
}

func TestDocument_SliceVersionsAndFetches(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	id := f.byURL[catDoc.url]
	d, err := f.r.Document(ctx, id, 5, 10)
	if err != nil {
		t.Fatal(err)
	}
	if d.Text != []string{string([]rune(catDoc.text)[5:15])}[0] || !d.Truncated || d.Offset != 5 || d.TextChars != len([]rune(catDoc.text)) {
		t.Errorf("slice = %q truncated=%v offset=%d chars=%d", d.Text, d.Truncated, d.Offset, d.TextChars)
	}
	if d.Origin != "primary" || d.SourceRef != SourceRef(d.SourceID) || d.Published == nil || d.Published.Date != "2020-01-15" ||
		len(d.Fetches) != 1 || d.Fetches[0].RunID != f.runA || d.Fetches[0].Reason != store.ReasonCited || len(d.Versions) != 0 {
		t.Errorf("document = %+v", d)
	}
	whole, _ := f.r.Document(ctx, id, 0, 0)
	if whole.Text != catDoc.text || whole.Truncated {
		t.Errorf("default slice = %d chars, truncated=%v", len(whole.Text), whole.Truncated)
	}
	past, _ := f.r.Document(ctx, id, 1<<20, 10)
	if past.Text != "" || past.Truncated {
		t.Errorf("offset past the end = %q", past.Text)
	}

	// A re-fetch with new text is a second version of the source.
	again := fetchRun(t, f.st, doc{url: catDoc.url, text: para("Cats revised"), at: now})
	again.Finish(store.Finish{Status: store.StatusSucceeded})
	if _, err := f.ix.IngestRun(ctx, again.Dir(), false); err != nil {
		t.Fatal(err)
	}
	d, _ = f.r.Document(ctx, id, 0, 10)
	if len(d.Versions) != 1 || d.Versions[0].DocumentID == id || !d.Versions[0].Collected.Equal(now) {
		t.Errorf("versions = %+v", d.Versions)
	}
	if _, err := f.r.Document(ctx, 99, 0, 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing document err = %v", err)
	}
}

func TestSource_ByURLIDAndRef(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s, err := f.r.Source(ctx, "https://www.zoo.example/cats/?utm_source=x")
	if err != nil {
		t.Fatal(err)
	}
	if s.URL != catDoc.url || s.Domain != "zoo.example" || len(s.Documents) != 1 || len(s.Fetches) != 1 ||
		len(s.CitedBy) != 1 || s.CitedBy[0] != f.runA || s.Ref != SourceRef(s.ID) {
		t.Errorf("source = %+v", s)
	}
	for _, ref := range []string{s.Ref, fmt.Sprint(s.ID)} {
		if got, err := f.r.Source(ctx, ref); err != nil || got.ID != s.ID {
			t.Errorf("Source(%q) = %d, %v", ref, got.ID, err)
		}
	}
	rep, err := f.r.Source(ctx, index.ReportKey(f.runB))
	if err != nil || rep.Kind != "report" || len(rep.Documents) != 1 || rep.Documents[0].Origin != "synthesis" {
		t.Errorf("report source = %+v, %v", rep, err)
	}
	if _, err := f.r.Source(ctx, "https://nowhere.example/x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing source err = %v", err)
	}
	if _, err := f.r.Source(ctx, "not a url"); err == nil {
		t.Error("bad ref accepted")
	}
}

func TestSource_Sightings(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	run, _ := f.st.StartRun(store.RunRecord{Kind: "dive", Topic: "t"})
	run.Append(store.Capture{Call: store.Call{Tool: "web_search", Action: "search", Query: "cats",
		Results: []store.CaptureResult{{Rank: 2, Title: "Cats", URL: catDoc.url, Snippet: "small carnivorous"}}}})
	run.Finish(store.Finish{Status: store.StatusSucceeded})
	if _, err := f.ix.IngestRun(ctx, run.Dir(), false); err != nil {
		t.Fatal(err)
	}
	s, err := f.r.Source(ctx, catDoc.url)
	if err != nil || len(s.Sightings) != 1 {
		t.Fatalf("source = %+v, %v", s, err)
	}
	g := s.Sightings[0]
	if g.RunID != run.ID() || g.Ref != "E1" || g.Role != "result" || g.Rank == nil || *g.Rank != 2 || g.Snippet != "small carnivorous" {
		t.Errorf("sighting = %+v", g)
	}
}

func ingester(t *testing.T, f *fixture) (*Ingester, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprintf(w, `<html><head><title>Kittens</title><meta name="citation_publication_date" content="2023-04-05"></head><body><article><p>%s</p><p>%s</p></article></body></html>`,
				para("Kitten care for new owners"), strings.Repeat(filler+" ", 3))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c := fetch.NewClient(5 * time.Second)
	c.AllowPrivate, c.Retries, c.HostInterval = true, 0, 0
	return &Ingester{Index: f.ix, Store: f.st, Stage: &fetch.Stage{Store: f.st, Client: c, Concurrency: 1, UsableChars: fetch.DefaultUsableChars},
		Embed: fakeEmbedder{}}, srv
}

func TestIngestURL_ManualRun(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	in, srv := ingester(t, f)
	res, err := in.IngestURL(ctx, srv.URL+"/page", "")
	if err != nil {
		t.Fatal(err)
	}
	rec, err := store.ReadRecord(f.st.RunDir(res.RunID))
	if err != nil || rec.Kind != KindManual || rec.Status != store.StatusSucceeded {
		t.Errorf("manual run = %+v, %v", rec, err)
	}
	if res.Fetch.Fetched != 1 || len(res.Documents) != 1 || res.Note != "" || len(res.Errors) != 0 {
		t.Fatalf("result = %+v", res)
	}
	d := res.Documents[0]
	if d.Title != "Kittens" || d.Published == nil || d.Published.Date != "2023-04-05" || len(d.Passages) == 0 || res.Embedded != len(d.Passages) {
		t.Errorf("document = %+v, embedded %d", d, res.Embedded)
	}
	// It's findable right away, by vector too.
	found, _ := f.r.Find(ctx, Query{Text: "feline owners", Kinds: []string{KindPassage}, Limit: 1})
	if len(found.Cards) != 1 || found.Cards[0].DocumentID != d.DocumentID || found.Cards[0].VectorRank == 0 {
		t.Errorf("find after ingest = %+v", found.Cards)
	}
	var reason string
	f.ix.Pool().QueryRow(ctx, `SELECT reason FROM fetches WHERE run_id = $1`, res.RunID).Scan(&reason)
	if reason != store.ReasonIngest {
		t.Errorf("fetch reason = %q", reason)
	}
}

func TestIngestURL_IntoRunAndFailures(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	in, srv := ingester(t, f)
	res, err := in.IngestURL(ctx, srv.URL+"/gone", f.runA)
	if err != nil {
		t.Fatal(err)
	}
	if res.RunID != f.runA || len(res.Documents) != 0 || len(res.Errors) != 1 || !strings.Contains(res.Errors[0], "404") {
		t.Errorf("failed ingest = %+v", res)
	}
	scan, _ := store.ScanFetches(f.st.RunDir(f.runA))
	if last := scan.Records[len(scan.Records)-1]; last.Reason != store.ReasonIngest || last.HTTPStatus != 404 {
		t.Errorf("recorded attempt = %+v", last)
	}

	// Into an existing run, with no embedder: it says the passages wait.
	in.Embed = nil
	res, err = in.IngestURL(ctx, srv.URL+"/page", f.runA)
	if err != nil || len(res.Documents) != 1 || !strings.Contains(res.Note, "no embedding model") {
		t.Errorf("ingest without embedder = %+v, %v", res, err)
	}

	// A manual ingest that gets nothing leaves a failed run.
	res, _ = in.IngestURL(ctx, srv.URL+"/gone", "")
	if rec, _ := store.ReadRecord(f.st.RunDir(res.RunID)); rec.Status != store.StatusFailed {
		t.Errorf("manual run that fetched nothing = %+v", rec)
	}

	if _, err := in.IngestURL(ctx, "ftp://x.example/a", ""); err == nil {
		t.Error("ftp URL accepted")
	}
	if _, err := in.IngestURL(ctx, srv.URL+"/page", "20990101T000000Z-000000"); err == nil {
		t.Error("unknown run accepted")
	}
}
