package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCitations_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	if cs, ok, err := ReadCitations(dir); cs != nil || ok || err != nil || CitationsSize(dir) != -1 {
		t.Fatalf("unchecked run = %v %v %v", cs, ok, err)
	}
	if err := WriteCitations(dir, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := ReadCitations(dir); !ok || err != nil || CitationsSize(dir) != 0 {
		t.Fatalf("empty check: ok %v err %v size %d", ok, err, CitationsSize(dir))
	}
	yes := true
	in := []Citation{
		{Ord: 1, Marker: "E3", TargetKind: "capture", TargetID: "3", ReportOffset: 10, Group: 1, Resolved: &yes},
		{Ord: 2, Marker: "P:9", TargetKind: "passage", TargetID: "9", ReportOffset: 40, Group: 2, Quote: "a b c d", QuoteStatus: QuoteUnchecked},
	}
	if err := WriteCitations(dir, in); err != nil {
		t.Fatal(err)
	}
	out, ok, err := ReadCitations(dir)
	if err != nil || !ok || len(out) != 2 || out[0].Resolved == nil || !*out[0].Resolved || out[1].Resolved != nil || out[1].Quote != "a b c d" {
		t.Fatalf("read = %+v %v %v", out, ok, err)
	}
	if err := os.WriteFile(filepath.Join(dir, CitationsFile), []byte("{bad\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadCitations(dir); err == nil {
		t.Error("bad line accepted")
	}
}
