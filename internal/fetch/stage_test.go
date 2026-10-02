package fetch

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marklubin/researchguy/internal/store"
)

var longText = strings.Repeat("Real article text with enough words to count as the document itself. ", 60)

// site serves the pages the stage tests fetch, and OpenAlex.
func site(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/article":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, `<html><head><title>Article</title><meta name="citation_publication_date" content="2024-05-06"></head><body><article><p>%s</p></article></body></html>`, longText)
		case "/blocked/doi/10.1234/blocked":
			w.WriteHeader(http.StatusForbidden)
		case "/works/https://doi.org/10.1234/blocked":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"title":"Blocked paper","publication_date":"2020-02-03","abstract_inverted_index":{"Short":[0],"abstract.":[1]},
				"locations":[{"is_oa":true,"pdf_url":null,"landing_page_url":"%s/repo/copy","source":{"type":"repository"}}]}`, srv.URL)
		case "/repo/copy":
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte(longText))
		case "/stub":
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(`<html><head><meta http-equiv="refresh" content="0;url=/article"></head><body>Redirecting</body></html>`))
		case "/image":
			w.Header().Set("Content-Type", "image/png")
			w.Write([]byte("\x89PNG...."))
		case "/slow":
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func stageFor(t *testing.T, srv *httptest.Server) (*Stage, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	c := testClient()
	c.Retries = 0
	return &Stage{
		Store:       st,
		Client:      c,
		OpenAlex:    &OpenAlex{Base: srv.URL, Client: c},
		PDF:         FindPDFTools(),
		TopResults:  3,
		Concurrency: 4,
	}, st
}

func runWith(t *testing.T, st *store.Store, results ...store.CaptureResult) *store.Run {
	t.Helper()
	run, err := st.StartRun(store.RunRecord{Kind: "dive", Topic: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run.Append(store.Capture{Call: store.Call{Tool: "web_search", Action: "search", Query: "q", Results: results}}); err != nil {
		t.Fatal(err)
	}
	if err := run.Finish(store.Finish{Status: store.StatusSucceeded}); err != nil {
		t.Fatal(err)
	}
	return run
}

func byURL(recs []store.FetchRecord) map[string][]store.FetchRecord {
	out := map[string][]store.FetchRecord{}
	for _, r := range recs {
		out[r.URL] = append(out[r.URL], r)
	}
	return out
}

func TestStage_FetchesStoresAndRecords(t *testing.T) {
	srv, _ := site(t)
	s, st := stageFor(t, srv)
	run := runWith(t, st,
		store.CaptureResult{Rank: 1, URL: srv.URL + "/article"},
		store.CaptureResult{Rank: 2, URL: srv.URL + "/missing"},
		store.CaptureResult{Rank: 3, URL: srv.URL + "/stub"},
		store.CaptureResult{Rank: 4, URL: srv.URL + "/not-top-3"},
	)
	sum, err := s.Run(context.Background(), run.ID(), false)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Queued != 3 || sum.Fetched != 2 || sum.Failed != 1 || sum.Remaining != 0 || sum.Records != 3 {
		t.Errorf("summary = %+v", sum)
	}
	scan, _ := store.ScanFetches(run.Dir())
	recs := byURL(scan.Records)
	art := recs[srv.URL+"/article"][0]
	if art.HTTPStatus != 200 || art.TextSHA256 == "" || art.RawSHA256 == "" || art.ContentKind != store.KindFull || art.Title != "Article" || art.Reason != store.ReasonResult || art.Rank != 1 {
		t.Errorf("article record = %+v", art)
	}
	if art.Published == nil || art.Published.Date != "2024-05-06" {
		t.Errorf("article date = %+v", art.Published)
	}
	text, err := st.ReadText(art.TextSHA256)
	if err != nil || !strings.Contains(text, "Real article text") {
		t.Errorf("stored text = %.60q, %v", text, err)
	}
	if raw, err := st.ReadBlob(art.RawSHA256); err != nil || !strings.Contains(string(raw), "<article>") {
		t.Errorf("stored raw = %v", err)
	}
	if miss := recs[srv.URL+"/missing"][0]; miss.HTTPStatus != 404 || miss.Error != "HTTP 404" || miss.TextSHA256 != "" {
		t.Errorf("404 record = %+v", miss)
	}
	stub := recs[srv.URL+"/stub"][0]
	if stub.FinalURL != srv.URL+"/article" || stub.TextSHA256 != art.TextSHA256 || stub.Attempts != 2 {
		t.Errorf("meta refresh not followed: %+v", stub)
	}
	if _, ok, _ := store.ReadFetchSummary(run.Dir()); !ok {
		t.Error("no fetch.json")
	}

	// A second pass skips what's been tried; force fetches again.
	again, err := s.Run(context.Background(), run.ID(), false)
	if err != nil || again.Skipped != 3 || again.Records != 0 {
		t.Errorf("second pass = %+v, %v", again, err)
	}
	forced, err := s.Run(context.Background(), run.ID(), true)
	if err != nil || forced.Records != 3 {
		t.Errorf("forced pass = %+v, %v", forced, err)
	}
	scan, _ = store.ScanFetches(run.Dir())
	if len(scan.Records) != 6 || scan.Records[5].Seq != 6 {
		t.Errorf("after force: %d records", len(scan.Records))
	}
}

func TestStage_DOIRescue(t *testing.T) {
	srv, _ := site(t)
	s, st := stageFor(t, srv)
	blocked := srv.URL + "/blocked/doi/10.1234/blocked"
	run := runWith(t, st, store.CaptureResult{Rank: 1, URL: blocked})
	sum, err := s.Run(context.Background(), run.ID(), false)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Fetched != 1 || sum.Records != 3 {
		t.Fatalf("summary = %+v", sum)
	}
	scan, _ := store.ScanFetches(run.Dir())
	direct, oa, repo := scan.Records[0], scan.Records[1], scan.Records[2]
	if direct.Via != ViaDirect || direct.HTTPStatus != 403 || direct.DOI != "10.1234/blocked" {
		t.Errorf("direct = %+v", direct)
	}
	if oa.Via != ViaOpenAlex || oa.URL != blocked || oa.ContentKind != store.KindAbstract || oa.Title != "Blocked paper" ||
		oa.Published == nil || oa.Published.Date != "2020-02-03" || oa.Published.From != "openalex" {
		t.Errorf("openalex = %+v", oa)
	}
	if abs, _ := st.ReadText(oa.TextSHA256); abs != "Short abstract." {
		t.Errorf("abstract = %q", abs)
	}
	if repo.Via != ViaRepository || repo.URL != blocked || repo.FetchURL != srv.URL+"/repo/copy" || repo.ContentKind != store.KindFull || repo.TextChars < DefaultUsableChars {
		t.Errorf("repository = %+v", repo)
	}
}

func TestStage_NoOpenAlexMeansNoRescue(t *testing.T) {
	srv, _ := site(t)
	s, st := stageFor(t, srv)
	s.OpenAlex = nil
	run := runWith(t, st, store.CaptureResult{Rank: 1, URL: srv.URL + "/blocked/doi/10.1234/blocked"})
	sum, err := s.Run(context.Background(), run.ID(), false)
	if err != nil || sum.Records != 1 || sum.Failed != 1 {
		t.Errorf("summary = %+v, %v", sum, err)
	}
}

func TestStage_NonDocumentRecorded(t *testing.T) {
	srv, _ := site(t)
	s, st := stageFor(t, srv)
	run := runWith(t, st, store.CaptureResult{Rank: 1, URL: srv.URL + "/image"})
	if _, err := s.Run(context.Background(), run.ID(), false); err != nil {
		t.Fatal(err)
	}
	scan, _ := store.ScanFetches(run.Dir())
	if len(scan.Records) != 1 || !strings.Contains(scan.Records[0].Error, "not a document") || scan.Records[0].RawSHA256 != "" {
		t.Errorf("image record = %+v", scan.Records)
	}
}

func TestStage_BudgetLeavesRemaining(t *testing.T) {
	srv, _ := site(t)
	s, st := stageFor(t, srv)
	s.Concurrency = 1
	s.Budget = 200 * time.Millisecond
	run := runWith(t, st,
		store.CaptureResult{Rank: 1, URL: srv.URL + "/slow"},
		store.CaptureResult{Rank: 2, URL: srv.URL + "/article"},
	)
	start := time.Now()
	sum, err := s.Run(context.Background(), run.ID(), false)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 3*time.Second {
		t.Error("budget not enforced")
	}
	if sum.Remaining != 2 || sum.Done() || sum.Records != 0 {
		t.Errorf("summary = %+v, want both URLs remaining and nothing recorded", sum)
	}
	pending, _ := st.PendingFetch()
	if len(pending) != 1 || pending[0] != run.ID() {
		t.Errorf("PendingFetch = %v, want the unfinished run", pending)
	}
	// The next pass, without a budget, finishes it.
	s.Budget = 0
	s.Client.Timeout = 300 * time.Millisecond
	sum, err = s.Run(context.Background(), run.ID(), false)
	if err != nil || sum.Remaining != 0 || sum.Records != 2 {
		t.Errorf("resumed pass = %+v, %v", sum, err)
	}
}

func TestStage_Busy(t *testing.T) {
	srv, _ := site(t)
	s, st := stageFor(t, srv)
	run := runWith(t, st, store.CaptureResult{Rank: 1, URL: srv.URL + "/article"})
	log, err := store.OpenFetchLog(run.Dir())
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	if _, err := s.Run(context.Background(), run.ID(), false); !errors.Is(err, store.ErrFetchBusy) {
		t.Errorf("err = %v, want ErrFetchBusy", err)
	}
}

func TestStage_FetchURLs(t *testing.T) {
	srv, _ := site(t)
	s, st := stageFor(t, srv)
	run := runWith(t, st, store.CaptureResult{Rank: 1, URL: srv.URL + "/article"})
	if _, err := s.Run(context.Background(), run.ID(), false); err != nil {
		t.Fatal(err)
	}
	planned, _, _ := store.ReadFetchSummary(run.Dir())

	// Asked-for URLs are fetched even when tried, once per normalized URL.
	sum, err := s.FetchURLs(context.Background(), run.ID(), []string{srv.URL + "/article", srv.URL + "/article#top", " " + srv.URL + "/missing"})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Queued != 2 || sum.Fetched != 1 || sum.Failed != 1 || sum.Records != 2 {
		t.Errorf("summary = %+v", sum)
	}
	scan, _ := store.ScanFetches(run.Dir())
	if len(scan.Records) != 3 {
		t.Fatalf("%d records, want 3", len(scan.Records))
	}
	for _, r := range scan.Records[1:] {
		if r.Reason != store.ReasonIngest || r.Rank != 0 {
			t.Errorf("ingest record = %+v", r)
		}
	}
	if after, _, _ := store.ReadFetchSummary(run.Dir()); after.Queued != planned.Queued || !after.StartedAt.Equal(planned.StartedAt) {
		t.Errorf("fetch.json changed: %+v, was %+v", after, planned)
	}

	for _, bad := range []string{"ftp://example.com/x", "not a url", ""} {
		if _, err := s.FetchURLs(context.Background(), run.ID(), []string{bad}); err == nil {
			t.Errorf("FetchURLs(%q) succeeded", bad)
		}
	}
	log, err := store.OpenFetchLog(run.Dir())
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	if _, err := s.FetchURLs(context.Background(), run.ID(), []string{srv.URL + "/article"}); !errors.Is(err, store.ErrFetchBusy) {
		t.Errorf("busy err = %v", err)
	}
}

func TestKindOf(t *testing.T) {
	cases := []struct {
		ct, body, want string
	}{
		{"text/html; charset=utf-8", "<p>", "html"},
		{"application/xhtml+xml", "", "html"},
		{"application/octet-stream", "%PDF-1.7", "pdf"},
		{"application/pdf", "", "pdf"},
		{"text/plain", "x", "text"},
		{"", "<!DOCTYPE html><html>", "html"},
		{"application/json", "{}", ""},
		{"image/png", "x", ""},
	}
	for _, c := range cases {
		if got := kindOf(&Response{ContentType: c.ct, Body: []byte(c.body)}); got != c.want {
			t.Errorf("kindOf(%q, %q) = %q, want %q", c.ct, c.body, got, c.want)
		}
	}
}
