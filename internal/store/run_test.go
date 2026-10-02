package store

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

func startRun(t *testing.T) (*Store, *Run) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.StartRun(RunRecord{Kind: "dive", Topic: "topic", Backend: "hybrid", Mode: "inquiry"})
	if err != nil {
		t.Fatal(err)
	}
	return s, r
}

func TestOpen_EmptyDir(t *testing.T) {
	if _, err := Open(""); err == nil {
		t.Fatal("Open(\"\") should fail")
	}
}

func TestStartRun_WritesRunningRecord(t *testing.T) {
	s, r := startRun(t)
	if !regexp.MustCompile(`^\d{8}T\d{6}Z-[0-9a-f]{6}$`).MatchString(r.ID()) {
		t.Errorf("run id %q is not <utc timestamp>-<6 hex>", r.ID())
	}
	if r.Dir() != filepath.Join(s.Dir(), "runs", r.ID()) {
		t.Errorf("run dir = %q", r.Dir())
	}
	rec, err := ReadRecord(r.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if rec.ID != r.ID() || rec.Status != StatusRunning || rec.Kind != "dive" || rec.Mode != "inquiry" {
		t.Errorf("record = %+v", rec)
	}
	if rec.StartedAt.IsZero() || rec.FinishedAt != nil {
		t.Errorf("started %v finished %v, want started set and not finished", rec.StartedAt, rec.FinishedAt)
	}
}

func TestStartRun_CallerCannotPresetStatusOrCounts(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.StartRun(RunRecord{ID: "mine", Status: StatusSucceeded, Captures: 9, Files: []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	rec := r.Record()
	if rec.ID == "mine" || rec.Status != StatusRunning || rec.Captures != 0 || len(rec.Files) != 0 {
		t.Errorf("record = %+v, want a fresh id, running, no captures or files", rec)
	}
}

func TestStartRun_IDsAreUnique(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := s.StartRun(RunRecord{Kind: "ask"})
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if seen[r.ID()] {
				t.Errorf("duplicate run id %s", r.ID())
			}
			seen[r.ID()] = true
		}()
	}
	wg.Wait()
}

func TestCapture_AppendsAcrossCallsAndCounts(t *testing.T) {
	_, r := startRun(t)
	at := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	if err := r.Capture(
		Capture{Seq: 1, Worker: 1, Shard: "a", Label: "web_search: x", Content: "one", CapturedAt: at},
		Capture{Seq: 2, Worker: 1, Shard: "a", Label: "web_search: y", Content: "two\nlines"},
	); err != nil {
		t.Fatal(err)
	}
	if err := r.Capture(Capture{Seq: 3, Worker: 2, Shard: "b", WorkerError: "boom", Label: "l", Content: "three"}); err != nil {
		t.Fatal(err)
	}

	got, err := ReadCaptures(r.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("read %d captures, want 3", len(got))
	}
	if got[0].Seq != 1 || got[0].Content != "one" || !got[0].CapturedAt.Equal(at) {
		t.Errorf("capture 1 = %+v, want its preset time kept", got[0])
	}
	if got[1].Content != "two\nlines" || got[1].CapturedAt.IsZero() {
		t.Errorf("capture 2 = %+v, want content intact and time filled in", got[1])
	}
	if got[2].WorkerError != "boom" || got[2].Worker != 2 {
		t.Errorf("capture 3 = %+v", got[2])
	}
	if rec := r.Record(); rec.Captures != 3 || !contains(rec.Files, "captures.jsonl") {
		t.Errorf("record captures=%d files=%v", rec.Captures, rec.Files)
	}
}

func TestCapture_NoneIsNoop(t *testing.T) {
	_, r := startRun(t)
	if err := r.Capture(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(r.Dir(), "captures.jsonl")); !os.IsNotExist(err) {
		t.Errorf("empty capture created captures.jsonl (stat err %v)", err)
	}
	if got, err := ReadCaptures(r.Dir()); err != nil || got != nil {
		t.Errorf("ReadCaptures on a run without captures = %v, %v", got, err)
	}
}

func TestReadCaptures_ReportsCorruptLine(t *testing.T) {
	_, r := startRun(t)
	if err := r.Capture(Capture{Seq: 1, Label: "l", Content: "ok"}); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(r.Dir(), "captures.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("{not json\n")
	f.Close()
	got, err := ReadCaptures(r.Dir())
	if err == nil {
		t.Fatal("want an error for a corrupt line")
	}
	if len(got) != 1 {
		t.Errorf("want the 1 good capture before the corrupt line, got %d", len(got))
	}
}

func TestWriteFile(t *testing.T) {
	_, r := startRun(t)
	if err := r.WriteFile("workers.jsonl", []byte("a\n")); err != nil {
		t.Fatal(err)
	}
	if err := r.WriteFile("workers.jsonl", []byte("b\n")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(r.Dir(), "workers.jsonl"))
	if err != nil || string(data) != "b\n" {
		t.Errorf("workers.jsonl = %q, %v; want the second write", data, err)
	}
	if rec := r.Record(); len(rec.Files) != 1 || rec.Files[0] != "workers.jsonl" {
		t.Errorf("files = %v, want workers.jsonl once", rec.Files)
	}
	entries, _ := os.ReadDir(r.Dir())
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestWriteFile_RejectsBadNames(t *testing.T) {
	_, r := startRun(t)
	for _, name := range []string{"", "../escape", "a/b", ".hidden", "run.json", "captures.jsonl"} {
		if err := r.WriteFile(name, []byte("x")); err == nil {
			t.Errorf("WriteFile(%q) should fail", name)
		}
	}
}

func TestFinish(t *testing.T) {
	_, r := startRun(t)
	if err := r.Capture(Capture{Seq: 1, Label: "l", Content: "c"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Finish(Finish{Status: StatusSucceeded, ReportPath: "/r/report.md", Metadata: `{"mode":"hybrid"}`}); err != nil {
		t.Fatal(err)
	}
	rec, err := ReadRecord(r.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != StatusSucceeded || rec.ReportPath != "/r/report.md" || rec.FinishedAt == nil || rec.Captures != 1 {
		t.Errorf("record = %+v", rec)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, rec.Metadata); err != nil || compact.String() != `{"mode":"hybrid"}` {
		t.Errorf("metadata = %s, want the JSON embedded as an object", rec.Metadata)
	}
}

func TestFinish_NonJSONMetadataIsQuoted(t *testing.T) {
	_, r := startRun(t)
	if err := r.Finish(Finish{Status: StatusFailed, Error: "boom", Metadata: "plain text"}); err != nil {
		t.Fatal(err)
	}
	rec, err := ReadRecord(r.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != StatusFailed || rec.Error != "boom" || string(rec.Metadata) != `"plain text"` {
		t.Errorf("record = %+v metadata %s", rec, rec.Metadata)
	}
}

func TestNilRunRecordsNothing(t *testing.T) {
	var r *Run
	if r.ID() != "" || r.Dir() != "" {
		t.Error("nil run should have no id or dir")
	}
	if err := r.Capture(Capture{Seq: 1}); err != nil {
		t.Error(err)
	}
	if err := r.WriteFile("x", nil); err != nil {
		t.Error(err)
	}
	if err := r.Finish(Finish{Status: StatusSucceeded}); err != nil {
		t.Error(err)
	}
	if rec := r.Record(); rec.ID != "" {
		t.Errorf("nil run record = %+v", rec)
	}
}

func TestReadRecord_Missing(t *testing.T) {
	if _, err := ReadRecord(t.TempDir()); err == nil {
		t.Error("want an error for a dir with no run.json")
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
