package claims

import (
	"context"
	"encoding/json"
	"errors"
	"hash/fnv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/marklubin/researchguy/internal/llm"
	"github.com/marklubin/researchguy/internal/store"
	"github.com/marklubin/researchguy/internal/store/index"
)

const (
	tollOld    = "The bridge toll was five dollars in 2024."
	pilotsA    = "The harbour pilots number twelve."
	tollNew    = "The bridge toll is six dollars as of 2025."
	pilotsB    = "Twelve harbour pilots work the port."
	rainfall   = "Rainfall averages seventy inches a year."
	linkFiller = " Filler text that pads the page out past the shortest length extracted from, and says nothing else of note."
)

// nearEmbedder gives every text nearly the same vector, so every pair of
// claims is close; which pairs get asked about is up to the word check.
type nearEmbedder struct{}

func (nearEmbedder) Model() string { return "near" }
func (nearEmbedder) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		h := fnv.New32a()
		h.Write([]byte(t))
		v := make([]float32, index.Dims)
		v[0] = 1
		v[1+int(h.Sum32()%uint32(index.Dims-1))] = 0.2
		out[i] = v
	}
	return out, nil
}

// linkFixture indexes two documents from different origins with their
// claims extracted and embedded, and returns the claim ids by text.
func linkFixture(t *testing.T) (*index.Index, *store.Store, map[string]int64) {
	t.Helper()
	ctx := context.Background()
	a := tollOld + " " + pilotsA + linkFiller
	b := tollNew + " " + pilotsB + " " + rainfall + linkFiller
	ix, st := indexPages(t, []page{{"https://a.example/x", "Old guide", a}, {"https://b.example/y", "New guide", b}})
	ids := map[string]int64{}
	for url, doc := range map[string]struct {
		text   string
		claims []string
	}{"a.example/x": {a, []string{tollOld, pilotsA}}, "b.example/y": {b, []string{tollNew, pilotsB, rainfall}}} {
		e := store.Extraction{TextSHA: store.HashText(doc.text), Extractor: "test/claims-v1", Model: "test", Chunks: 1, Attempts: 1,
			ExtractedAt: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)}
		for i, c := range doc.claims {
			e.Claims = append(e.Claims, store.ExtractedClaim{Text: c, Quote: strings.TrimSuffix(c, ".")})
			ids[c] = index.ClaimID(index.DocumentID(url, e.TextSHA), e.Extractor, 0, i)
		}
		if err := st.PutExtraction(e); err != nil {
			t.Fatal(err)
		}
	}
	cs, err := ix.SyncClaims(ctx, st)
	if err != nil || cs.Verified != 5 || cs.Origins == nil || cs.Origins.Origins != 2 {
		t.Fatalf("sync = %+v, %v", cs, err)
	}
	if _, err := ix.EmbedClaims(ctx, st, nearEmbedder{}); err != nil {
		t.Fatal(err)
	}
	return ix, st, ids
}

// fakeLabeler answers each pair with answer(a, b), the A and B claim
// texts from the prompt.
type fakeLabeler struct {
	mu      sync.Mutex
	calls   int
	prompts []string
	answer  func(a, b string) (relation, by string)
	raw     string
	err     error
}

func (f *fakeLabeler) Name() string { return "fake" }
func (f *fakeLabeler) Complete(_ context.Context, req llm.Request) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.prompts = append(f.prompts, req.UserPrompt)
	if f.err != nil {
		return "", f.err
	}
	if f.raw != "" {
		return f.raw, nil
	}
	type lab struct {
		Pair       int     `json:"pair"`
		Relation   string  `json:"relation"`
		By         string  `json:"by"`
		Confidence float64 `json:"confidence"`
		Note       string  `json:"note"`
	}
	var labels []lab
	var a string
	n := 0
	for _, line := range strings.Split(req.UserPrompt, "\n") {
		switch {
		case strings.HasPrefix(line, "A: "):
			a = strings.TrimPrefix(line, "A: ")
		case strings.HasPrefix(line, "B: "):
			n++
			rel, by := f.answer(a, strings.TrimPrefix(line, "B: "))
			if rel != "" {
				labels = append(labels, lab{Pair: n, Relation: rel, By: by, Confidence: 1.5, Note: "because"})
			}
		}
	}
	out, _ := json.Marshal(map[string]any{"labels": labels})
	return "Here you go:\n" + string(out), nil
}

func tollAndPilots(a, b string) (string, string) {
	switch {
	case strings.Contains(a+b, "toll"):
		if strings.Contains(a, "six") {
			return "supersedes", "A"
		}
		return "supersedes", "B"
	case strings.Contains(a+b, "pilots"):
		return "same", ""
	}
	return "unrelated", ""
}

func linkOpts() LinkOptions {
	return LinkOptions{Neighbors: 5, MinSimilarity: 0.75, BatchSize: 20, MaxPairsPerDay: 500, Model: "fake/test"}
}

func TestLink_LabelsPairsOnce(t *testing.T) {
	ix, st, ids := linkFixture(t)
	ctx := context.Background()
	f := &fakeLabeler{answer: tollAndPilots}

	stats, err := Link(ctx, ix, st, f, linkOpts())
	if err != nil {
		t.Fatal(err)
	}
	// Only the two pairs that share words are asked about, once each.
	if stats.Pairs != 2 || stats.Linked != 2 || stats.Checked != 5 || stats.Unanswered != 0 || f.calls != 1 ||
		stats.ByRelation["supersedes"] != 1 || stats.ByRelation["same"] != 1 {
		t.Fatalf("stats = %+v, %d calls", stats, f.calls)
	}
	if !strings.Contains(f.prompts[0], `source "New guide"`) || !strings.Contains(f.prompts[0], `quote: "The bridge toll`) {
		t.Errorf("prompt:\n%s", f.prompts[0])
	}
	log, _ := st.ReadLinks(0)
	var super store.Link
	for _, l := range log.Links {
		if l.Relation == "supersedes" {
			super = l
		}
	}
	if super.From != ids[tollNew] || super.To != ids[tollOld] || super.Method != store.LinkModel || super.Model != "fake/test" || super.Confidence != 1 {
		t.Errorf("supersedes link = %+v", super)
	}
	if stats.Sync == nil || stats.Sync.Links != 2 {
		t.Errorf("sync = %+v", stats.Sync)
	}
	// Linked pairs aren't neighbors to ask about any more.
	if near, _ := ix.NearestClaims(ctx, ids[tollOld], 5, 0.75); len(near) != 2 {
		t.Errorf("toll's unlinked neighbors = %d, want the other two of b's claims", len(near))
	}

	// Nothing's left to check: no call.
	stats, err = Link(ctx, ix, st, f, linkOpts())
	if err != nil || stats.Pairs != 0 || stats.Checked != 0 || f.calls != 1 {
		t.Fatalf("second pass = %+v, %v, %d calls", stats, err, f.calls)
	}
}

func TestLink_CapAndFailures(t *testing.T) {
	ctx := context.Background()

	ix, st, _ := linkFixture(t)
	f := &fakeLabeler{err: errors.New("not logged in")}
	stats, err := Link(ctx, ix, st, f, linkOpts())
	if err == nil || !strings.Contains(err.Error(), "not logged in") || stats.Checked != 0 {
		t.Fatalf("failing model = %+v, %v", stats, err)
	}
	// The claims weren't marked, so a working model gets them all.
	f = &fakeLabeler{answer: tollAndPilots}
	opts := linkOpts()
	opts.MaxPairsPerDay = 1
	stats, err = Link(ctx, ix, st, f, opts)
	if err != nil || !stats.Capped || stats.Pairs != 1 || stats.Linked != 1 {
		t.Fatalf("capped = %+v, %v", stats, err)
	}
	// The day's cap counts what's been logged.
	stats, err = Link(ctx, ix, st, f, opts)
	if err != nil || !stats.Capped || stats.Pairs != 0 || f.calls != 1 {
		t.Fatalf("over the cap = %+v, %v, %d calls", stats, err, f.calls)
	}

	// An answer that isn't JSON leaves the pairs unanswered, and the
	// claims checked so they aren't asked about again.
	ix, st, _ = linkFixture(t)
	f = &fakeLabeler{raw: "I can't help with that."}
	stats, err = Link(ctx, ix, st, f, linkOpts())
	if err != nil || stats.Unanswered != 2 || stats.Linked != 0 || stats.Checked != 5 || stats.Sync != nil {
		t.Fatalf("bad answer = %+v, %v", stats, err)
	}
}

func TestLabel_ValidatesAnswers(t *testing.T) {
	a := index.LinkClaim{ID: 10, Text: "x"}
	b := index.LinkClaim{ID: 3, Text: "y"}
	batch := []pair{{a, b}, {b, a}, {a, b}, {b, a}}
	raw := `{"labels":[{"pair":1,"relation":"Contradicts","confidence":-1},{"pair":1,"relation":"same"},
		{"pair":2,"relation":"supersedes","by":""},{"pair":3,"relation":"refines","by":"a","note":"` + strings.Repeat("n", 400) + `"},
		{"pair":4,"relation":"agrees"},{"pair":9,"relation":"same"}]}`
	links, unanswered, err := label(context.Background(), &fakeLabeler{raw: raw}, batch, "m")
	if err != nil || unanswered != 2 || len(links) != 2 {
		t.Fatalf("links = %+v, unanswered %d, %v", links, unanswered, err)
	}
	if links[0].From != 3 || links[0].To != 10 || links[0].Relation != "contradicts" || links[0].Confidence != 0 {
		t.Errorf("symmetric link = %+v", links[0])
	}
	if links[1].From != 10 || links[1].To != 3 || links[1].Relation != "refines" || len(links[1].Note) != 300 {
		t.Errorf("directed link = %+v", links[1])
	}
	// A long note is cut between characters, not inside one.
	raw = `{"labels":[{"pair":1,"relation":"same","note":"a` + strings.Repeat("é", 200) + `"}]}`
	links, _, err = label(context.Background(), &fakeLabeler{raw: raw}, batch[:1], "m")
	if err != nil || len(links) != 1 || !utf8.ValidString(links[0].Note) || len(links[0].Note) != 299 {
		t.Errorf("multibyte note = %q, %v", links[0].Note, err)
	}
}

func TestShareTerm(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{tollOld, tollNew, true},
		{"Sales rose in 2024.", "Costs fell in 2024.", true},
		{"This is about that.", "That was about this.", false},
		{pilotsA, rainfall, false},
		{"", "anything", false},
	} {
		if got := shareTerm(tc.a, tc.b); got != tc.want {
			t.Errorf("shareTerm(%q, %q) = %v", tc.a, tc.b, got)
		}
	}
}
