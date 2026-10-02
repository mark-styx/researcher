package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func finishedRun(t *testing.T, s *Store) *Run {
	t.Helper()
	run, err := s.StartRun(RunRecord{Kind: "dive", Topic: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Finish(Finish{Status: StatusSucceeded}); err != nil {
		t.Fatal(err)
	}
	return run
}

func TestFetchLog_AppendNumbersAndResumes(t *testing.T) {
	s := testStore(t)
	run := finishedRun(t, s)
	log, err := OpenFetchLog(run.Dir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	a, err := log.Append(FetchRecord{URL: "https://a.example/x", Reason: ReasonCited, Via: "direct", AttemptedAt: now, Attempts: 1, HTTPStatus: 200})
	if err != nil || a.Seq != 1 {
		t.Fatalf("first Append = %+v, %v", a, err)
	}
	b, err := log.Append(FetchRecord{URL: "https://b.example/y", Reason: ReasonResult, Rank: 2, Via: "direct", AttemptedAt: now, Attempts: 3, Error: "HTTP 403"})
	if err != nil || b.Seq != 2 {
		t.Fatalf("second Append = %+v, %v", b, err)
	}
	if err := log.WriteSummary(FetchSummary{Queued: 2, Fetched: 1, Failed: 1, Records: 2}); err != nil {
		t.Fatal(err)
	}
	log.Close()

	// A later pass numbers after what's there.
	log2, err := OpenFetchLog(run.Dir())
	if err != nil {
		t.Fatal(err)
	}
	c, err := log2.Append(FetchRecord{URL: "https://c.example", Reason: ReasonOpened, Via: "direct"})
	log2.Close()
	if err != nil || c.Seq != 3 {
		t.Fatalf("resumed Append = %+v, %v", c, err)
	}
	scan, err := ScanFetches(run.Dir())
	if err != nil || len(scan.Records) != 3 || scan.Partial || len(scan.BadLines) != 0 {
		t.Fatalf("ScanFetches = %+v, %v", scan, err)
	}
	if scan.Records[1].Error != "HTTP 403" || scan.Records[1].Attempts != 3 || scan.Records[1].Rank != 2 {
		t.Errorf("record 2 = %+v", scan.Records[1])
	}
	if scan.Size != FetchSize(run.Dir()) || scan.Size == 0 {
		t.Errorf("Size = %d, FetchSize = %d", scan.Size, FetchSize(run.Dir()))
	}
	sum, ok, err := ReadFetchSummary(run.Dir())
	if err != nil || !ok || sum.Queued != 2 || !sum.Done() {
		t.Errorf("ReadFetchSummary = %+v, %v, %v", sum, ok, err)
	}
}

func TestFetchLog_SecondOpenerIsBusy(t *testing.T) {
	s := testStore(t)
	run := finishedRun(t, s)
	log, err := OpenFetchLog(run.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFetchLog(run.Dir()); !errors.Is(err, ErrFetchBusy) {
		t.Fatalf("second open err = %v, want ErrFetchBusy", err)
	}
	log.Close()
	again, err := OpenFetchLog(run.Dir())
	if err != nil {
		t.Fatalf("open after Close: %v", err)
	}
	again.Close()
}

func TestFetchLog_FinishesCutOffLine(t *testing.T) {
	s := testStore(t)
	run := finishedRun(t, s)
	path := filepath.Join(run.Dir(), fetchFile)
	if err := os.WriteFile(path, []byte(`{"seq":1,"url":"https://a.example","reason":"cited","via":"direct","attempted_at":"2026-10-02T00:00:00Z","attempts":1,"duration_ms":5}`+"\n"+`{"seq":2,"url":"https://b.exa`), 0o644); err != nil {
		t.Fatal(err)
	}
	scan, _ := ScanFetches(run.Dir())
	if !scan.Partial || len(scan.Records) != 1 {
		t.Fatalf("before: %+v", scan)
	}
	log, err := OpenFetchLog(run.Dir())
	if err != nil {
		t.Fatal(err)
	}
	rec, err := log.Append(FetchRecord{URL: "https://c.example", Reason: ReasonCited, Via: "direct"})
	log.Close()
	if err != nil || rec.Seq != 2 {
		t.Fatalf("Append = %+v, %v", rec, err)
	}
	scan, _ = ScanFetches(run.Dir())
	if scan.Partial || len(scan.Records) != 2 || len(scan.BadLines) != 1 || scan.Records[1].URL != "https://c.example" {
		t.Errorf("after: %+v", scan)
	}
}

func TestPendingFetch(t *testing.T) {
	s := testStore(t)
	done := finishedRun(t, s)
	partial := finishedRun(t, s)
	never := finishedRun(t, s)
	running, err := s.StartRun(RunRecord{Kind: "ask"})
	if err != nil {
		t.Fatal(err)
	}
	for dir, sum := range map[string]FetchSummary{done.Dir(): {Queued: 3}, partial.Dir(): {Queued: 3, Remaining: 1}} {
		log, err := OpenFetchLog(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := log.WriteSummary(sum); err != nil {
			t.Fatal(err)
		}
		log.Close()
	}
	got, err := s.PendingFetch()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{partial.ID(): true, never.ID(): true}
	if len(got) != 2 || !want[got[0]] || !want[got[1]] {
		t.Errorf("PendingFetch = %v, want %s and %s (not %s, not running %s)", got, partial.ID(), never.ID(), done.ID(), running.ID())
	}
}

func TestWriteFile_ReservesFetchFiles(t *testing.T) {
	s := testStore(t)
	run := finishedRun(t, s)
	for _, name := range []string{fetchFile, fetchSummaryFile, fetchLockFile} {
		if err := run.WriteFile(name, []byte("x")); err == nil {
			t.Errorf("WriteFile(%s) allowed", name)
		}
	}
}
