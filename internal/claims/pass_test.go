package claims

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/marklubin/researchguy/internal/store"
	"github.com/marklubin/researchguy/internal/store/index"
	"github.com/marklubin/researchguy/internal/store/index/indextest"
)

var (
	goodText = strings.Join([]string{
		"The bridge opened to traffic in the spring of that year. " + strings.Repeat("Filler words follow on. ", 2),
		"Its main span is four hundred metres long between towers. " + strings.Repeat("Filler words follow on. ", 2),
		"Tolls were removed after the construction debt was paid off. " + strings.Repeat("Filler words follow on. ", 2),
	}, "\n\n")
	failText = strings.Join([]string{
		"The harbour handles most of the region's container freight. " + strings.Repeat("More filler text here. ", 2),
		"A FAIL paragraph the model can't answer for, about dredging. " + strings.Repeat("More filler text here. ", 2),
		"The port authority was founded by an act of the legislature. " + strings.Repeat("More filler text here. ", 2),
	}, "\n\n")
)

// indexed returns an index holding a finished run that fetched goodText,
// failText and a page too short to extract.
func indexed(t *testing.T) (*index.Index, *store.Store) {
	t.Helper()
	ctx := context.Background()
	ix, err := index.Open(ctx, indextest.DSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ix.Close)
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := st.StartRun(store.RunRecord{Kind: "dive", Topic: "bridges", Backend: "hybrid"})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Finish(store.Finish{Status: store.StatusSucceeded}); err != nil {
		t.Fatal(err)
	}
	log, err := store.OpenFetchLog(run.Dir())
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	for i, page := range []struct{ url, title, text string }{
		{"https://example.org/bridge", "Bridge", goodText},
		{"https://example.org/harbour", "Harbour", failText},
		{"https://example.org/stub", "Stub", "Too short to bother with."},
	} {
		sha, err := st.PutText(page.text)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := log.Append(store.FetchRecord{URL: page.url, Reason: store.ReasonOpened, Via: "direct", AttemptedAt: at.Add(time.Duration(i) * time.Minute),
			Attempts: 1, HTTPStatus: 200, TextSHA256: sha, TextChars: len(page.text), ContentKind: store.KindFull, Title: page.title}); err != nil {
			t.Fatal(err)
		}
	}
	if err := log.WriteSummary(store.FetchSummary{Records: 3}); err != nil {
		t.Fatal(err)
	}
	log.Close()
	if _, err := ix.Sync(ctx, st); err != nil {
		t.Fatal(err)
	}
	return ix, st
}

type unitEmbedder struct{ texts int }

func (u *unitEmbedder) Model() string { return "unit" }
func (u *unitEmbedder) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	u.texts += len(texts)
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = make([]float32, index.Dims)
		out[i][i%index.Dims] = 1
	}
	return out, nil
}

func TestRun_ExtractsRetriesAndGivesUp(t *testing.T) {
	ix, st := indexed(t)
	ctx := context.Background()
	f, ex := newFake(t)
	ex.ChunkChars = 120
	f.fail = func(chunk string) (int, string) {
		if strings.Contains(chunk, "FAIL") {
			return 500, ""
		}
		return 0, ""
	}
	emb := &unitEmbedder{}
	opts := Options{MaxAttempts: 2, Embedder: emb, Progress: t.Logf}

	stats, err := Run(ctx, ix, st, ex, opts)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Waiting != 2 || stats.Extracted != 2 || stats.Claims != 5 || stats.Failed != 1 || stats.Stopped || f.unloads != 1 {
		t.Fatalf("first pass = %+v, %d unloads", stats, f.unloads)
	}
	if stats.Sync == nil || stats.Sync.Claims != 5 || stats.Sync.Verified != 5 {
		t.Fatalf("sync = %+v", stats.Sync)
	}
	if stats.Embed == nil || stats.Embed.Embedded != 5 || emb.texts != 5 {
		t.Fatalf("embed = %+v", stats.Embed)
	}
	good, ok, err := st.ReadExtraction(ex.Name(), store.HashText(goodText))
	if err != nil || !ok || !good.Complete() || good.Attempts != 1 || good.Chunks != 3 {
		t.Fatalf("good extraction = %+v, %v, %v", good, ok, err)
	}

	// The failed text is retried, only its failed chunk.
	before := f.chatCount()
	stats, err = Run(ctx, ix, st, ex, opts)
	if err != nil || stats.Waiting != 1 || stats.Extracted != 1 || stats.Failed != 1 || f.chatCount()-before != 1 {
		t.Fatalf("second pass = %+v, %v, %d calls", stats, err, f.chatCount()-before)
	}
	// Out of attempts, it's skipped without calling the model.
	before = f.chatCount()
	stats, err = Run(ctx, ix, st, ex, opts)
	if err != nil || stats.Exhausted != 1 || stats.Extracted != 0 || f.chatCount() != before || f.unloads != 2 {
		t.Fatalf("third pass = %+v, %v, %d unloads", stats, err, f.unloads)
	}
	// With more attempts allowed and the model fixed, it completes.
	f.fail = nil
	opts.MaxAttempts = 3
	stats, err = Run(ctx, ix, st, ex, opts)
	if err != nil || stats.Extracted != 1 || stats.Failed != 0 || stats.Sync.Claims != 3 {
		t.Fatalf("fourth pass = %+v, %v", stats, err)
	}
	stats, err = Run(ctx, ix, st, ex, opts)
	if err != nil || stats.Waiting != 0 || stats.Sync != nil {
		t.Fatalf("nothing left = %+v, %v", stats, err)
	}
}

func TestRun_PausedAndLimited(t *testing.T) {
	ix, st := indexed(t)
	ctx := context.Background()
	f, ex := newFake(t)
	ex.ChunkChars = 120

	stats, err := Run(ctx, ix, st, ex, Options{Pause: func() bool { return true }})
	if err != nil || !stats.Stopped || stats.Extracted != 0 || f.chatCount() != 0 || f.unloads != 0 {
		t.Fatalf("paused = %+v, %v", stats, err)
	}
	files, _ := st.Extractions()
	if len(files) != 0 {
		t.Fatalf("a paused pass wrote %d files", len(files))
	}

	stats, err = Run(ctx, ix, st, ex, Options{Limit: 1})
	if err != nil || stats.Extracted != 1 || stats.Waiting != 2 {
		t.Fatalf("limited = %+v, %v", stats, err)
	}

	// Ollama down: the pass reports it and writes nothing.
	down := &Extractor{Host: "http://127.0.0.1:1", Model: ex.Model, ChunkChars: 120}
	stats, err = Run(ctx, ix, st, down, Options{})
	if err == nil || stats.Extracted != 0 {
		t.Fatalf("down = %+v, %v", stats, err)
	}
}
