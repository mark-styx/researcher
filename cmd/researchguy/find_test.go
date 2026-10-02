package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/marklubin/researchguy/internal/store"
	"github.com/marklubin/researchguy/internal/store/index/indextest"
	"github.com/marklubin/researchguy/internal/store/retrieve"
)

func TestFind_BadFlags(t *testing.T) {
	storeSetup(t, "postgres://localhost:1/none")
	for _, args := range [][]string{{"find", "x", "--since", "soon"}, {"find", "x", "--prefer-recent", "fast"}} {
		if _, _, err := runCmdStdout(t, args...); err == nil || (!strings.Contains(err.Error(), "since") && !strings.Contains(err.Error(), "prefer_recent")) {
			t.Errorf("%v: err = %v", args, err)
		}
	}
}

func TestFindAndLookups(t *testing.T) {
	srv := fetchSite(t, nil)
	st := fetchSetup(t, srv, indextest.DSN(t))
	mustStdout(t, "store", "init")

	ing := decode[retrieve.IngestResult](t, mustStdout(t, "store", "ingest-url", srv.URL+"/article", "--json"))
	if len(ing.Documents) != 1 || len(ing.Documents[0].Passages) == 0 || ing.Embedded != len(ing.Documents[0].Passages) {
		t.Fatalf("ingest-url = %+v", ing)
	}
	if rec, err := store.ReadRecord(st.RunDir(ing.RunID)); err != nil || rec.Kind != retrieve.KindManual {
		t.Errorf("manual run = %+v, %v", rec, err)
	}
	doc := ing.Documents[0]

	res := decode[retrieve.Result](t, mustStdout(t, "find", "fetched article paragraph", "--json"))
	if res.Mode != retrieve.ModeHybrid || len(res.Cards) == 0 || res.Cards[0].DocumentID != doc.DocumentID || res.Cards[0].Title != "Article" {
		t.Fatalf("find = %+v", res)
	}
	card := res.Cards[0]
	out := mustStdout(t, "find", "fetched article paragraph")
	if !strings.Contains(out, "1. ["+card.Ref+"] Article ("+card.Domain+", passage)") || !strings.Contains(out, "undated; collected") || !strings.Contains(out, "full-text and meaning") {
		t.Errorf("find text = %q", out)
	}
	if none := decode[retrieve.Result](t, mustStdout(t, "find", "fetched article", "--kind", "report", "--json")); len(none.Cards) != 0 {
		t.Errorf("report-only find = %+v", none.Cards)
	}
	if _, _, err := runCmdStdout(t, "find", "x", "--kind", "claim"); err == nil {
		t.Error("bad --kind accepted")
	}

	p := decode[retrieve.PassageResult](t, mustStdout(t, "store", "passage", card.Ref, "--json"))
	if p.PassageID != card.PassageID || p.URL != srv.URL+"/article" {
		t.Errorf("passage = %+v", p)
	}
	if out := mustStdout(t, "store", "passage", fmt.Sprint(card.PassageID)); !strings.Contains(out, ">>> ") {
		t.Errorf("passage text = %q", out)
	}
	d := decode[retrieve.DocumentResult](t, mustStdout(t, "store", "document", fmt.Sprint(doc.DocumentID), "--chars", "20", "--json"))
	if len([]rune(d.Text)) != 20 || !d.Truncated || len(d.Fetches) != 1 || d.Fetches[0].Reason != store.ReasonIngest {
		t.Errorf("document = %+v", d)
	}
	_, stderr, err := runCmdStdout(t, "store", "document", fmt.Sprint(doc.DocumentID), "--chars", "20")
	if err != nil || !strings.Contains(stderr, "continue with --offset 20") {
		t.Errorf("document text: %v %q", err, stderr)
	}
	s := decode[retrieve.SourceResult](t, mustStdout(t, "store", "source", srv.URL+"/article?utm_source=x", "--json"))
	if s.ID != doc.SourceID || len(s.Documents) != 1 {
		t.Errorf("source = %+v", s)
	}
	if out := mustStdout(t, "store", "source", s.Ref); !strings.Contains(out, "1 document(s), 0 sighting(s), 1 fetch attempt(s)") {
		t.Errorf("source text = %q", out)
	}

	// Into a named run; a missing page is reported, not an error.
	miss := decode[retrieve.IngestResult](t, mustStdout(t, "store", "ingest-url", srv.URL+"/missing", "--run", ing.RunID, "--json"))
	if miss.RunID != ing.RunID || len(miss.Documents) != 0 || len(miss.Errors) != 1 {
		t.Errorf("missing page = %+v", miss)
	}
	for _, args := range [][]string{
		{"store", "passage", "P:x"}, {"store", "passage", "999"}, {"store", "document", "-4"},
		{"store", "source", "https://nowhere.example"}, {"store", "ingest-url", "ftp://x.example/a"},
		{"store", "ingest-url", srv.URL + "/article", "--run", "../etc"},
	} {
		if _, _, err := runCmdStdout(t, args...); err == nil {
			t.Errorf("%v succeeded", args)
		}
	}
}
