package index

import (
	"context"
	"testing"

	"github.com/marklubin/researchguy/internal/store"
)

func TestIngestRun_Citations(t *testing.T) {
	ix, _ := openIndex(t)
	st := newStore(t)
	ctx := context.Background()
	run := sampleRun(t, st)
	yes, no := true, false
	cs := []store.Citation{
		{Ord: 1, Marker: "E1", TargetKind: "capture", TargetID: "1", ReportOffset: 5, Group: 1, Resolved: &yes},
		{Ord: 2, Marker: "P:9", TargetKind: "passage", TargetID: "9", ReportOffset: 40, Group: 2, Quote: "a b c d", Resolved: &no, QuoteStatus: store.QuoteNotFound, Note: "no such passage"},
		// Found in one of its group's citations: not a missed quote.
		{Ord: 3, Marker: "P:9", TargetKind: "passage", TargetID: "9", ReportOffset: 60, Group: 3, Quote: "e f g h", Resolved: &no, QuoteStatus: store.QuoteNotFound},
		{Ord: 4, Marker: "S:2", TargetKind: "source", TargetID: "2", ReportOffset: 66, Group: 3, Quote: "e f g h", Resolved: &yes, QuoteStatus: store.QuoteFound},
		{Ord: 5, Marker: "S:3", TargetKind: "source", TargetID: "3", ReportOffset: 80, Group: 4, QuoteStatus: store.QuoteUnchecked, Quote: "i j k l"},
	}
	if err := store.WriteCitations(run.Dir(), cs); err != nil {
		t.Fatal(err)
	}
	stats, err := ix.IngestRun(ctx, run.Dir(), false)
	if err != nil || stats.Citations != 5 {
		t.Fatalf("ingest = %+v, %v", stats, err)
	}
	var marker, note string
	var resolved *bool
	var status *string
	if err := ix.pool.QueryRow(ctx, `SELECT marker, resolved, quote_status, note FROM citations WHERE run_id = $1 AND ord = 2`, run.ID()).
		Scan(&marker, &resolved, &status, &note); err != nil || marker != "P:9" || resolved == nil || *resolved || *status != "not_found" || note != "no such passage" {
		t.Errorf("row = %s %v %v %q, %v", marker, resolved, status, note, err)
	}
	if err := ix.pool.QueryRow(ctx, `SELECT resolved FROM citations WHERE run_id = $1 AND ord = 5`, run.ID()).Scan(&resolved); err != nil || resolved != nil {
		t.Errorf("unchecked resolved = %v, %v", resolved, err)
	}
	c, err := ix.Counts(ctx)
	if err != nil || c.Citations != 5 || c.Unresolved != 2 || c.QuotesNotFound != 1 {
		t.Errorf("counts = %+v, %v", c, err)
	}
	fails, err := ix.CitationFailures(ctx, 10)
	if err != nil || len(fails) != 1 || fails[0] != (CitationFailure{RunID: run.ID(), Unresolved: 2, QuotesNotFound: 1}) {
		t.Errorf("failures = %+v, %v", fails, err)
	}

	// Unchanged: skipped. A rewritten check is pending and replaces the rows.
	if stats, _ := ix.IngestRun(ctx, run.Dir(), false); !stats.Skipped {
		t.Error("unchanged citations re-ingested")
	}
	if err := store.WriteCitations(run.Dir(), cs[:1]); err != nil {
		t.Fatal(err)
	}
	if pending, _, _ := ix.Pending(ctx, st); len(pending) != 1 || pending[0] != run.ID() {
		t.Errorf("pending = %v, want the rechecked run", pending)
	}
	if _, err := ix.IngestRun(ctx, run.Dir(), false); err != nil {
		t.Fatal(err)
	}
	if c, _ := ix.Counts(ctx); c.Citations != 1 || c.Unresolved != 0 || c.QuotesNotFound != 0 {
		t.Errorf("after recheck counts = %+v", c)
	}

	// Rebuild brings them back from the file.
	if _, err := ix.Rebuild(ctx, st); err != nil {
		t.Fatal(err)
	}
	if c, _ := ix.Counts(ctx); c.Citations != 1 {
		t.Errorf("after rebuild counts = %+v", c)
	}
}

func TestIngestRun_UncheckedRunIsNotPending(t *testing.T) {
	ix, _ := openIndex(t)
	st := newStore(t)
	ctx := context.Background()
	sampleRun(t, st)
	if _, err := ix.Sync(ctx, st); err != nil {
		t.Fatal(err)
	}
	if pending, _, _ := ix.Pending(ctx, st); len(pending) != 0 {
		t.Errorf("pending = %v; a run without citations.jsonl should be up to date", pending)
	}
	if c, _ := ix.Counts(ctx); c.Citations != 0 {
		t.Errorf("counts = %+v", c)
	}
}
