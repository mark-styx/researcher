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
	if host, _ := os.Hostname(); rec.PID != os.Getpid() || rec.Host != host {
		t.Errorf("pid %d host %q, want this process", rec.PID, rec.Host)
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

func TestAppend_NumbersAcrossCallsAndCounts(t *testing.T) {
	_, r := startRun(t)
	at := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	seqs, err := r.Append(
		// A caller's seq is ignored: the run numbers every capture.
		Capture{Seq: 7, Worker: 1, Shard: "a", Label: "web_search: x", Content: "one", CapturedAt: at},
		Capture{Worker: 1, Shard: "a", Label: "web_search: y", Content: "two\nlines"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(seqs) != 2 || seqs[0] != 1 || seqs[1] != 2 {
		t.Errorf("seqs = %v, want [1 2]", seqs)
	}
	seqs, err = r.Append(Capture{Worker: 2, Shard: "b", WorkerError: "boom", Label: "l", Content: "three",
		Call: Call{Tool: "web_search", Action: "open", URL: "https://x.test/a",
			Results: []CaptureResult{{Opened: true, URL: "https://x.test/a", Title: "A", RefID: "turn1view0"}}}})
	if err != nil || len(seqs) != 1 || seqs[0] != 3 {
		t.Fatalf("seqs = %v, %v; want [3]", seqs, err)
	}

	got, err := ReadCaptures(r.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("read %d captures, want 3", len(got))
	}
	if got[0].Seq != 1 || got[0].Content != "one" || !got[0].CapturedAt.Equal(at) {
		t.Errorf("capture 1 = %+v, want seq 1 and its preset time kept", got[0])
	}
	if got[1].Content != "two\nlines" || got[1].CapturedAt.IsZero() {
		t.Errorf("capture 2 = %+v, want content intact and time filled in", got[1])
	}
	if got[2].WorkerError != "boom" || got[2].Worker != 2 || got[2].Action != "open" ||
		len(got[2].Results) != 1 || !got[2].Results[0].Opened || got[2].Results[0].RefID != "turn1view0" {
		t.Errorf("capture 3 = %+v", got[2])
	}
	if rec := r.Record(); rec.Captures != 3 || !contains(rec.Files, "captures.jsonl") {
		t.Errorf("record captures=%d files=%v", rec.Captures, rec.Files)
	}
}

func TestAppend_ParallelCallersGetDistinctSeqs(t *testing.T) {
	_, r := startRun(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				if _, err := r.Append(Capture{Worker: w, Label: "l", Content: "c"}); err != nil {
					t.Error(err)
				}
			}
		}(i + 1)
	}
	wg.Wait()
	got, err := ReadCaptures(r.Dir())
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int]bool{}
	for _, c := range got {
		if seen[c.Seq] {
			t.Fatalf("seq %d written twice", c.Seq)
		}
		seen[c.Seq] = true
	}
	if len(got) != 200 || !seen[1] || !seen[200] {
		t.Errorf("got %d captures, want seqs 1..200", len(got))
	}
}

func TestAppend_FailedWriteStillUsesSeq(t *testing.T) {
	_, r := startRun(t)
	// A directory where captures.jsonl should be makes the write fail.
	if err := os.Mkdir(filepath.Join(r.Dir(), "captures.jsonl"), 0o755); err != nil {
		t.Fatal(err)
	}
	seqs, err := r.Append(Capture{Label: "l", Content: "c"})
	if err == nil || len(seqs) != 1 || seqs[0] != 1 {
		t.Fatalf("seqs = %v, err = %v; want seq 1 and an error", seqs, err)
	}
	if rec := r.Record(); rec.Captures != 0 {
		t.Errorf("captures = %d, want 0 after a failed write", rec.Captures)
	}
	os.Remove(filepath.Join(r.Dir(), "captures.jsonl"))
	if seqs, err := r.Append(Capture{Label: "l", Content: "c"}); err != nil || seqs[0] != 2 {
		t.Errorf("next seq = %v, %v; want 2, never reusing a cited number", seqs, err)
	}
}

func TestAppend_NoneIsNoop(t *testing.T) {
	_, r := startRun(t)
	if seqs, err := r.Append(); err != nil || seqs != nil {
		t.Fatalf("Append() = %v, %v", seqs, err)
	}
	if _, err := os.Stat(filepath.Join(r.Dir(), "captures.jsonl")); !os.IsNotExist(err) {
		t.Errorf("empty append created captures.jsonl (stat err %v)", err)
	}
	if got, err := ReadCaptures(r.Dir()); err != nil || got != nil {
		t.Errorf("ReadCaptures on a run without captures = %v, %v", got, err)
	}
}

func TestReadCaptures_ReportsCorruptLine(t *testing.T) {
	_, r := startRun(t)
	if _, err := r.Append(Capture{Label: "l", Content: "ok"}); err != nil {
		t.Fatal(err)
	}
	appendRaw(t, r.Dir(), "{not json\n")
	if _, err := r.Append(Capture{Label: "l", Content: "after"}); err != nil {
		t.Fatal(err)
	}
	got, err := ReadCaptures(r.Dir())
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("err = %v, want one naming line 2", err)
	}
	if len(got) != 2 {
		t.Errorf("want the 2 good captures around the corrupt line, got %d", len(got))
	}
}

func TestScanCaptures_PartialLastLine(t *testing.T) {
	_, r := startRun(t)
	if _, err := r.Append(Capture{Label: "l", Content: "ok"}); err != nil {
		t.Fatal(err)
	}
	full, _ := os.Stat(filepath.Join(r.Dir(), "captures.jsonl"))
	appendRaw(t, r.Dir(), `{"seq":2,"label":"cut`)
	log, err := ScanCaptures(r.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if len(log.Captures) != 1 || !log.Partial || log.Bytes != full.Size() || log.Size != full.Size()+21 || len(log.BadLines) != 0 {
		t.Errorf("log = %+v, want 1 capture, partial, %d bytes, no bad lines", log, full.Size())
	}
	if _, err := ReadCaptures(r.Dir()); err != nil {
		t.Errorf("a partial last line is a write in progress, not an error: %v", err)
	}
}

func appendRaw(t *testing.T, dir, s string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, "captures.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
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
	if _, err := r.Append(Capture{Label: "l", Content: "c"}); err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(t.TempDir(), "report.md")
	if err := os.WriteFile(report, []byte("# Report\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.Finish(Finish{Status: StatusSucceeded, ReportPath: report, Metadata: `{"mode":"hybrid"}`}); err != nil {
		t.Fatal(err)
	}
	rec, err := ReadRecord(r.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != StatusSucceeded || rec.ReportPath != report || rec.ReportSHA256 != HashText("# Report") || rec.FinishedAt == nil || rec.Captures != 1 {
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
	if seqs, err := r.Append(Capture{Seq: 1}); err != nil || seqs != nil {
		t.Error(seqs, err)
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

func TestReportSection_WatchFile(t *testing.T) {
	watch := "# Topic — Watch Updates\n\n---\n\n## Update: 2026-09-01\n\n*Backend: x | Run: old*\n\nold https://old.example\n" +
		"\n\n---\n\n## Update: 2026-10-01\n\n*Backend: x | Run: new*\n\nnew https://new.example\n" +
		"\n\n---\n\n## Update: 2026-10-02\n\n*Backend: x | Run: newer*\n\nnewer https://newer.example\n"
	sec := ReportSection([]byte(watch), "new")
	if !strings.Contains(sec, "https://new.example") || strings.Contains(sec, "old.example") || strings.Contains(sec, "newer.example") {
		t.Errorf("section = %q", sec)
	}
	if !strings.HasPrefix(sec, "\n## Update: 2026-10-01") {
		t.Errorf("section doesn't start at its update header: %q", sec[:30])
	}
	if got := ReportSection([]byte("no tag https://x.example"), "zzz"); got != "no tag https://x.example" {
		t.Errorf("untagged report = %q", got)
	}
}

func TestFinish_CopiesReportSection(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.StartRun(RunRecord{Kind: "watch", Topic: "t"})
	if err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(t.TempDir(), "watch.md")
	body := "# T\n\n## Update: 2026-09-01\n\n*Run: earlier*\n\nold\n\n## Update: 2026-10-02\n\n*Run: " + r.ID() + "*\n\nnew findings\n"
	if err := os.WriteFile(report, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.Finish(Finish{Status: StatusSucceeded, ReportPath: report}); err != nil {
		t.Fatal(err)
	}
	rec, _ := ReadRecord(r.Dir())
	if rec.ReportSHA256 == "" {
		t.Fatal("no report_sha256")
	}
	text, err := s.ReadText(rec.ReportSHA256)
	if err != nil || !strings.Contains(text, "new findings") || strings.Contains(text, "old") {
		t.Errorf("stored section = %q, %v", text, err)
	}

	// A failed run's report isn't copied; a missing file still writes the
	// record and says why.
	f, _ := s.StartRun(RunRecord{Kind: "dive"})
	if err := f.Finish(Finish{Status: StatusFailed, ReportPath: report}); err != nil {
		t.Fatal(err)
	}
	if rec, _ := ReadRecord(f.Dir()); rec.ReportSHA256 != "" {
		t.Errorf("failed run copied its report: %+v", rec)
	}
	m, _ := s.StartRun(RunRecord{Kind: "dive"})
	if err := m.Finish(Finish{Status: StatusSucceeded, ReportPath: filepath.Join(t.TempDir(), "gone.md")}); err == nil || !strings.Contains(err.Error(), "copying report") {
		t.Errorf("missing report err = %v", err)
	}
	if rec, _ := ReadRecord(m.Dir()); rec.Status != StatusSucceeded || rec.ReportSHA256 != "" {
		t.Errorf("record after a missing report = %+v", rec)
	}
}
