package fetch

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/marklubin/researchguy/internal/graph"
	"github.com/marklubin/researchguy/internal/store"
)

// How a document was reached.
const (
	ViaDirect     = "direct"      // the URL itself
	ViaOpenAlex   = "openalex"    // OpenAlex's metadata and abstract for its DOI
	ViaRepository = "repository"  // a repository copy OpenAlex lists
	ViaOpenAccess = "open_access" // another open-access copy OpenAlex lists
)

// Target is a URL the fetch stage fetches for a run.
type Target struct {
	URL    string
	Key    string // graph.NormalizeURL of URL
	Reason string // store.ReasonCited, ReasonOpened, ReasonResult
	Rank   int    // search rank, for results
	DOI    string // from the URL, when it names one
}

var reasonOrder = map[string]int{store.ReasonCited: 0, store.ReasonOpened: 1, store.ReasonResult: 2}

// Plan lists what a run's fetch stage fetches, once per normalized URL and
// in the order it fetches them, so a time budget cuts the least useful
// first: URLs cited in a worker draft or the report, then pages workers
// opened, then each search's top topN results by rank.
func Plan(dir string, rec store.RunRecord, captures []store.Capture, topN int) []Target {
	byKey := map[string]*Target{}
	var order []string
	add := func(raw, reason string, rank int) {
		raw = strings.TrimSpace(raw)
		if !fetchable(raw) {
			return
		}
		key, err := graph.NormalizeURL(raw)
		if err != nil {
			return
		}
		t, ok := byKey[key]
		if !ok {
			byKey[key] = &Target{URL: raw, Key: key, Reason: reason, Rank: rank, DOI: DOIFromURL(raw)}
			order = append(order, key)
			return
		}
		if reasonOrder[reason] < reasonOrder[t.Reason] || (reason == t.Reason && rank > 0 && (t.Rank == 0 || rank < t.Rank)) {
			t.Reason, t.Rank = reason, rank
		}
	}
	for _, u := range CitedURLs(dir, rec) {
		add(u, store.ReasonCited, 0)
	}
	for _, c := range captures {
		if c.URL != "" && c.Action != "search" {
			add(c.URL, store.ReasonOpened, 0)
		}
		for _, r := range c.Results {
			switch {
			case r.Opened:
				add(r.URL, store.ReasonOpened, 0)
			case r.Rank > 0 && r.Rank <= topN:
				add(r.URL, store.ReasonResult, r.Rank)
			}
		}
	}
	out := make([]Target, 0, len(order))
	for _, k := range order {
		out = append(out, *byKey[k])
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if reasonOrder[a.Reason] != reasonOrder[b.Reason] {
			return reasonOrder[a.Reason] < reasonOrder[b.Reason]
		}
		return a.Reason == store.ReasonResult && a.Rank < b.Rank
	})
	return out
}

// CitedURLs lists the URLs in a run's worker drafts and in its report.
// For a watch file, which holds every update, only the run's own update is
// read.
func CitedURLs(dir string, rec store.RunRecord) []string {
	var out []string
	if f, err := os.Open(filepath.Join(dir, "workers.jsonl")); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		for sc.Scan() {
			var w struct {
				Content string `json:"content"`
			}
			if json.Unmarshal(sc.Bytes(), &w) == nil {
				out = append(out, ExtractURLs(w.Content)...)
			}
		}
		f.Close()
	}
	if rec.ReportPath != "" {
		if b, err := os.ReadFile(rec.ReportPath); err == nil {
			out = append(out, ExtractURLs(store.ReportSection(b, rec.ID))...)
		}
	}
	return out
}
