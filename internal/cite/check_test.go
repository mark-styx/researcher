package cite

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/marklubin/researchguy/internal/store"
	"github.com/marklubin/researchguy/internal/store/index"
	"github.com/marklubin/researchguy/internal/store/index/indextest"
	"github.com/marklubin/researchguy/internal/store/retrieve"
)

const (
	catURL      = "https://zoo.example/cats"
	abstractURL = "https://journal.example/paper"
	catText     = "Cats sleep twelve to sixteen hours daily, mostly in warm places near windows."
	abstText    = "Abstract: we measured feline sleep in shelters."
)

type checkFixture struct {
	r        *retrieve.Retriever
	captures []store.Capture
	catP     int64
	abstP    int64
	catS     int64
}

func newCheckFixture(t *testing.T) *checkFixture {
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
	run, err := st.StartRun(store.RunRecord{Kind: "dive", Topic: "cats"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run.Append(
		store.Capture{Call: store.Call{Tool: "web_search", Action: "search", Query: "cats",
			Results: []store.CaptureResult{{Rank: 1, Title: "Cats", URL: catURL, Snippet: "felines nap in the sun for hours"}}}},
		store.Capture{Call: store.Call{Tool: "web_fetch", Action: "open", URL: catURL}, Content: "The page says cats sleep a lot."},
	); err != nil {
		t.Fatal(err)
	}
	log, err := store.OpenFetchLog(run.Dir())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []struct{ url, text, kind string }{{catURL, catText, store.KindFull}, {abstractURL, abstText, store.KindAbstract}} {
		sha, err := st.PutText(d.text)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := log.Append(store.FetchRecord{URL: d.url, Reason: store.ReasonCited, Via: "direct", AttemptedAt: time.Now().UTC(),
			Attempts: 1, HTTPStatus: 200, ContentType: "text/html", TextSHA256: sha, TextChars: len([]rune(d.text)), ContentKind: d.kind}); err != nil {
			t.Fatal(err)
		}
	}
	log.Close()
	if err := run.Finish(store.Finish{Status: store.StatusSucceeded}); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.IngestRun(ctx, run.Dir(), false); err != nil {
		t.Fatal(err)
	}
	f := &checkFixture{r: &retrieve.Retriever{Index: ix, Store: st}}
	if f.captures, err = store.ReadCaptures(run.Dir()); err != nil {
		t.Fatal(err)
	}
	q := `SELECT p.id, s.id FROM passages p JOIN documents d ON d.id = p.document_id JOIN sources s ON s.id = d.source_id WHERE s.url = $1`
	if err := ix.Pool().QueryRow(ctx, q, catURL).Scan(&f.catP, &f.catS); err != nil {
		t.Fatal(err)
	}
	var abstS int64
	if err := ix.Pool().QueryRow(ctx, q, abstractURL).Scan(&f.abstP, &abstS); err != nil {
		t.Fatal(err)
	}
	return f
}

func id(n int64) string { return strconv.FormatInt(n, 10) }

func TestCheck_ResolvesAndVerifiesQuotes(t *testing.T) {
	f := newCheckFixture(t)
	report := strings.Join([]string{
		`Cats "sleep twelve to sixteen hours daily" [P:` + id(f.catP) + `].`,
		`Snippet: "felines nap in the sun" [E1].`,
		`Beyond the snippet: "felines hunt at night always" [E1].`,
		`Misquoted: "the page claims something else entirely" [E2].`,
		`Missing capture [E99] and passage [P:12345].`,
		`Abstract only: "abstract does not say this" [P:` + id(f.abstP) + `].`,
		`Source: "Cats sleep twelve to sixteen" [S:` + id(f.catS) + `].`,
		`Group: "mostly in warm places near windows" [P:12345][P:` + id(f.catP) + `].`,
	}, "\n")
	cs := Check(context.Background(), report, f.captures, f.r)
	type want struct {
		marker   string
		resolved bool
		quote    string
	}
	wants := []want{
		{"P:" + id(f.catP), true, store.QuoteFound},
		{"E1", true, store.QuoteFound},
		{"E1", true, store.QuoteUnverifiable},
		{"E2", true, store.QuoteNotFound},
		{"E99", false, ""},
		{"P:12345", false, ""},
		{"P:" + id(f.abstP), true, store.QuoteUnverifiable},
		{"S:" + id(f.catS), true, store.QuoteFound},
		{"P:12345", false, store.QuoteNotFound},
		{"P:" + id(f.catP), true, store.QuoteFound},
	}
	if len(cs) != len(wants) {
		t.Fatalf("got %d citations: %+v", len(cs), cs)
	}
	for i, w := range wants {
		c := cs[i]
		if c.Marker != w.marker || c.Resolved == nil || *c.Resolved != w.resolved || c.QuoteStatus != w.quote {
			t.Errorf("citation %d = %+v (resolved %v), want %+v", i, c, c.Resolved != nil && *c.Resolved, w)
		}
	}
	if cs[4].Note != "no capture E99 in the run" {
		t.Errorf("note = %q", cs[4].Note)
	}

	s := Summarize(cs)
	if s.Citations != 10 || s.Resolved != 7 || s.Unresolved != 3 || s.Unchecked != 0 {
		t.Errorf("counts = %+v", s)
	}
	// The group's quote is in one of its passages, so only the misquote fails.
	if s.Quotes != 7 || s.QuotesFound != 4 || s.QuotesNotFound != 1 || s.QuotesUnverified != 2 {
		t.Errorf("quote counts = %+v", s)
	}
	if len(s.Failures) != 4 || !strings.Contains(s.Failures[3], `"the page claims something else entirely" [E2]`) {
		t.Errorf("failures = %q", s.Failures)
	}
	notes := s.Notes()
	if !strings.HasPrefix(notes, "10 citations: 7 resolved, 3 unresolved, 0 not checked. 7 quotes: 4 found, 1 not found, 2 unverifiable") {
		t.Errorf("notes = %s", notes)
	}
}

func TestCheck_WithoutIndex(t *testing.T) {
	report := `"one two three four" [P:5] and [S:6] and "a snippet that we saw" [E1]`
	captures := []store.Capture{{Seq: 1, Call: store.Call{Action: "search"}, Content: "here is a snippet that we saw today"}}
	cs := Check(context.Background(), report, captures, nil)
	if len(cs) != 3 || cs[0].Resolved != nil || cs[0].QuoteStatus != store.QuoteUnchecked || cs[1].Resolved != nil {
		t.Fatalf("citations = %+v", cs)
	}
	if cs[2].Resolved == nil || !*cs[2].Resolved || cs[2].QuoteStatus != store.QuoteFound {
		t.Errorf("capture citation = %+v", cs[2])
	}
	if s := Summarize(cs); s.Unchecked != 2 || len(s.Failures) != 0 || s.Notes() != "" {
		t.Errorf("summary = %+v", s)
	}
}

func TestPassages_ForCritics(t *testing.T) {
	f := newCheckFixture(t)
	draft := "A [P:" + id(f.catP) + "] B [P:" + id(f.catP) + "] C [P:12345] D [E1]"
	ev := Passages(context.Background(), draft, f.r)
	if len(ev) != 1 || ev[0].ID != "P:"+id(f.catP) || ev[0].Content != catText || !strings.Contains(ev[0].Label, "zoo.example") {
		t.Errorf("evidence = %+v", ev)
	}
	if Passages(context.Background(), draft, nil) != nil {
		t.Error("no index should give no passages")
	}
}

func TestNotesSection(t *testing.T) {
	if got := NotesSection("report", ""); got != "" {
		t.Errorf("no notes = %q", got)
	}
	if got := NotesSection("report", "- bad\n"); got != "\n\n---\n\n## Citation Check\n\n- bad\n" {
		t.Errorf("own section = %q", got)
	}
	critic := "report\n\n---\n\n## Critic Notes\n\n### Groundedness Review\n\nok"
	if got := NotesSection(critic, "- bad\n"); got != "\n\n### Citation Check\n\n- bad\n" {
		t.Errorf("under critic notes = %q", got)
	}
}
