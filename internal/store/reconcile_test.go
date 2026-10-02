package store

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// deadPID is the PID of a process that has exited.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

// setRecord rewrites a run's record on disk, as another process would.
func setRecord(t *testing.T, dir string, edit func(*RunRecord)) {
	t.Helper()
	rec, err := ReadRecord(dir)
	if err != nil {
		t.Fatal(err)
	}
	edit(&rec)
	if err := writeRecordFile(dir, rec); err != nil {
		t.Fatal(err)
	}
}

func TestReconcile_MarksDeadRunsInterrupted(t *testing.T) {
	s, dead := startRun(t)
	if _, err := dead.Append(Capture{Label: "a", Content: "1"}, Capture{Label: "b", Content: "2"}); err != nil {
		t.Fatal(err)
	}
	pid := deadPID(t)
	setRecord(t, dead.Dir(), func(r *RunRecord) { r.PID = pid })

	live, err := s.StartRun(RunRecord{Kind: "ask"}) // this process: still running
	if err != nil {
		t.Fatal(err)
	}
	done, err := s.StartRun(RunRecord{Kind: "ask"})
	if err != nil {
		t.Fatal(err)
	}
	if err := done.Finish(Finish{Status: StatusSucceeded}); err != nil {
		t.Fatal(err)
	}
	setRecord(t, done.Dir(), func(r *RunRecord) { r.PID = pid })
	legacy, err := s.StartRun(RunRecord{Kind: "ask"})
	if err != nil {
		t.Fatal(err)
	}
	setRecord(t, legacy.Dir(), func(r *RunRecord) { r.PID = 0 })
	elsewhere, err := s.StartRun(RunRecord{Kind: "ask"})
	if err != nil {
		t.Fatal(err)
	}
	setRecord(t, elsewhere.Dir(), func(r *RunRecord) { r.PID = pid; r.Host = "another-host" })

	marked, err := s.Reconcile()
	if err != nil {
		t.Fatal(err)
	}
	if len(marked) != 1 || marked[0] != dead.ID() {
		t.Fatalf("marked %v, want only %s", marked, dead.ID())
	}
	rec, err := ReadRecord(dead.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != StatusInterrupted || rec.Captures != 2 || rec.FinishedAt == nil || rec.Error == "" {
		t.Errorf("dead run record = %+v, want interrupted with 2 captures, an end time and an error", rec)
	}
	for _, r := range []*Run{live, legacy, elsewhere} {
		if rec, _ := ReadRecord(r.Dir()); rec.Status != StatusRunning {
			t.Errorf("run %s status = %s, want left running", r.ID(), rec.Status)
		}
	}
	if rec, _ := ReadRecord(done.Dir()); rec.Status != StatusSucceeded {
		t.Errorf("finished run status = %s", rec.Status)
	}

	// Already reconciled: nothing more to mark.
	if again, err := s.Reconcile(); err != nil || len(again) != 0 {
		t.Errorf("second Reconcile = %v, %v", again, err)
	}
}

func TestRunIDs_SortedAndSkipsFiles(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"20261002T120000Z-bbbbbb", "20261001T120000Z-aaaaaa"} {
		if err := os.Mkdir(filepath.Join(s.Dir(), "runs", name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(s.Dir(), "runs", "stray.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	ids, err := s.RunIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != "20261001T120000Z-aaaaaa" {
		t.Errorf("ids = %v, want the two run dirs oldest first", ids)
	}
	// A run dir with no run.json is skipped, not an error.
	if marked, err := s.Reconcile(); err != nil || len(marked) != 0 {
		t.Errorf("Reconcile = %v, %v", marked, err)
	}
}

func TestOrphaned(t *testing.T) {
	host, _ := os.Hostname()
	pid := deadPID(t)
	cases := []struct {
		name string
		rec  RunRecord
		want bool
	}{
		{"dead pid here", RunRecord{Status: StatusRunning, PID: pid, Host: host}, true},
		{"dead pid, no host recorded", RunRecord{Status: StatusRunning, PID: pid}, true},
		{"live pid", RunRecord{Status: StatusRunning, PID: os.Getpid(), Host: host}, false},
		{"finished", RunRecord{Status: StatusFailed, PID: pid, Host: host}, false},
		{"no pid", RunRecord{Status: StatusRunning, Host: host}, false},
		{"other host", RunRecord{Status: StatusRunning, PID: pid, Host: "elsewhere"}, false},
	}
	for _, c := range cases {
		if got := Orphaned(c.rec, host); got != c.want {
			t.Errorf("%s: Orphaned = %v, want %v", c.name, got, c.want)
		}
	}
}
