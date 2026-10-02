package fetch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/marklubin/researchguy/internal/store"
)

// OpenAlexBase is the OpenAlex API. It needs no key.
const OpenAlexBase = "https://api.openalex.org"

// OpenAlex resolves DOIs to metadata, abstracts and open-access copies.
type OpenAlex struct {
	Base   string
	Client *Client
}

// Work is the part of an OpenAlex work the fetcher uses.
type Work struct {
	Title           string
	PublicationDate string
	Year            int
	Abstract        string
	Locations       []Location
}

// Location is one place OpenAlex lists a copy of a work.
type Location struct {
	LandingURL string
	PDFURL     string
	IsOA       bool
	Repository bool // a repository (PubMed Central, a university), not the publisher
}

type openAlexWork struct {
	Title           string           `json:"title"`
	DisplayName     string           `json:"display_name"`
	PublicationDate string           `json:"publication_date"`
	PublicationYear int              `json:"publication_year"`
	Abstract        map[string][]int `json:"abstract_inverted_index"`
	Locations       []struct {
		IsOA       bool   `json:"is_oa"`
		LandingURL string `json:"landing_page_url"`
		PDFURL     string `json:"pdf_url"`
		Source     *struct {
			Type string `json:"type"`
		} `json:"source"`
	} `json:"locations"`
}

// WorkURL is the API URL for a DOI.
func (o *OpenAlex) WorkURL(doi string) string {
	base := o.Base
	if base == "" {
		base = OpenAlexBase
	}
	return strings.TrimRight(base, "/") + "/works/https://doi.org/" + doi
}

// Work looks a DOI up. A nil Work with a nil error means OpenAlex doesn't
// have it (resp holds the 404).
func (o *OpenAlex) Work(ctx context.Context, doi string) (*Work, *Response, error) {
	resp, err := o.Client.Get(ctx, o.WorkURL(doi))
	if err != nil {
		return nil, nil, err
	}
	if resp.Status == http.StatusNotFound {
		return nil, resp, nil
	}
	if resp.Status != http.StatusOK {
		return nil, resp, fmt.Errorf("OpenAlex: HTTP %d", resp.Status)
	}
	var raw openAlexWork
	if err := json.Unmarshal(resp.Body, &raw); err != nil {
		return nil, resp, fmt.Errorf("decoding OpenAlex work: %w", err)
	}
	w := &Work{
		Title:           firstNonEmpty(raw.Title, raw.DisplayName),
		PublicationDate: raw.PublicationDate,
		Year:            raw.PublicationYear,
		Abstract:        invertedAbstract(raw.Abstract),
	}
	for _, l := range raw.Locations {
		w.Locations = append(w.Locations, Location{
			LandingURL: l.LandingURL,
			PDFURL:     l.PDFURL,
			IsOA:       l.IsOA,
			Repository: l.Source != nil && l.Source.Type == "repository",
		})
	}
	return w, resp, nil
}

// invertedAbstract rebuilds an abstract from OpenAlex's word -> positions
// index.
func invertedAbstract(idx map[string][]int) string {
	if len(idx) == 0 {
		return ""
	}
	type wp struct {
		pos  int
		word string
	}
	var words []wp
	for w, ps := range idx {
		for _, p := range ps {
			words = append(words, wp{p, w})
		}
	}
	sort.Slice(words, func(i, j int) bool { return words[i].pos < words[j].pos })
	out := make([]string, len(words))
	for i, w := range words {
		out[i] = w.word
	}
	return strings.Join(out, " ")
}

// Published is the work's publication date. OpenAlex fills a year-only
// date in as January 1, so a YYYY-01-01 date counts only as its year.
func (w *Work) Published(now time.Time) *store.Published {
	if w.PublicationDate != "" {
		if strings.HasSuffix(w.PublicationDate, "-01-01") {
			return published(w.PublicationDate[:4], "openalex", false, now)
		}
		if p := published(w.PublicationDate, "openalex", false, now); p != nil {
			return p
		}
	}
	if w.Year > 0 {
		return published(fmt.Sprintf("%04d", w.Year), "openalex", false, now)
	}
	return nil
}

// Candidates lists URLs that may hold the full text, best first:
// repository copies (PDF, then landing page), then other open-access
// locations.
func (w *Work) Candidates() []Candidate {
	var out []Candidate
	seen := map[string]bool{}
	add := func(u, via string) {
		if u != "" && !seen[u] && fetchable(u) {
			seen[u] = true
			out = append(out, Candidate{URL: u, Via: via})
		}
	}
	for _, l := range w.Locations {
		if l.Repository {
			add(l.PDFURL, ViaRepository)
			add(l.LandingURL, ViaRepository)
		}
	}
	for _, l := range w.Locations {
		if l.IsOA && !l.Repository {
			add(l.PDFURL, ViaOpenAccess)
			add(l.LandingURL, ViaOpenAccess)
		}
	}
	return out
}

// Candidate is a URL to try for a work's text.
type Candidate struct {
	URL string
	Via string
}
