package fetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const openAlexJSON = `{
  "title": "Feed exposure and opinion",
  "publication_date": "2021-06-15",
  "publication_year": 2021,
  "abstract_inverted_index": {"We": [0], "measure": [1], "feed": [2, 5], "effects.": [3], "The": [4], "matters.": [6]},
  "locations": [
    {"is_oa": false, "landing_page_url": "https://publisher.example/doi/10.1234/x", "pdf_url": null, "source": {"type": "journal"}},
    {"is_oa": true, "landing_page_url": "https://oa.example/x", "pdf_url": "https://oa.example/x.pdf", "source": {"type": "journal"}},
    {"is_oa": true, "landing_page_url": "https://europepmc.example/abs/1", "pdf_url": "https://europepmc.example/pdf/1", "source": {"type": "repository"}},
    {"is_oa": true, "landing_page_url": "https://europepmc.example/abs/1", "pdf_url": null, "source": null}
  ]
}`

func TestOpenAlex_Work(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.URL.Path == "/works/https://doi.org/10.1234/missing" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(openAlexJSON))
	}))
	defer srv.Close()
	o := &OpenAlex{Base: srv.URL, Client: testClient()}
	w, resp, err := o.Work(context.Background(), "10.1234/x")
	if err != nil || w == nil || resp.Status != 200 {
		t.Fatalf("Work = %+v, %+v, %v", w, resp, err)
	}
	if gotPath != "/works/https://doi.org/10.1234/x" {
		t.Errorf("path = %q", gotPath)
	}
	if w.Abstract != "We measure feed effects. The feed matters." {
		t.Errorf("Abstract = %q", w.Abstract)
	}
	if p := w.Published(testNow); p == nil || p.Date != "2021-06-15" || p.From != "openalex" {
		t.Errorf("Published = %+v", p)
	}
	cands := w.Candidates()
	want := []Candidate{
		{"https://europepmc.example/pdf/1", ViaRepository},
		{"https://europepmc.example/abs/1", ViaRepository},
		{"https://oa.example/x.pdf", ViaOpenAccess},
		{"https://oa.example/x", ViaOpenAccess},
	}
	if len(cands) != len(want) {
		t.Fatalf("Candidates = %+v", cands)
	}
	for i := range want {
		if cands[i] != want[i] {
			t.Errorf("candidate %d = %+v, want %+v", i, cands[i], want[i])
		}
	}

	w, resp, err = o.Work(context.Background(), "10.1234/missing")
	if w != nil || err != nil || resp.Status != 404 {
		t.Errorf("missing DOI: %+v, %+v, %v", w, resp, err)
	}
}

func TestWork_PublishedYearOnly(t *testing.T) {
	if p := (&Work{PublicationDate: "2019-01-01", Year: 2019}).Published(testNow); p == nil || p.Date != "2019" || p.Precision != "year" {
		t.Errorf("Jan 1 date = %+v, want year precision", p)
	}
	if p := (&Work{Year: 2018}).Published(testNow); p == nil || p.Date != "2018" {
		t.Errorf("year only = %+v", p)
	}
	if p := (&Work{}).Published(testNow); p != nil {
		t.Errorf("no date = %+v", p)
	}
}
