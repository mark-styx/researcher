package store

import (
	"os"
	"testing"
)

func TestCheck_ReportsEachKindOfProblem(t *testing.T) {
	s, ok := startRun(t)
	ok.Append(Capture{Label: "a", Content: "1"})
	ok.Finish(Finish{Status: StatusSucceeded})

	running, _ := s.StartRun(RunRecord{Kind: "ask"}) // this process: alive

	orphan, _ := s.StartRun(RunRecord{Kind: "ask"})
	pid := deadPID(t)
	setRecord(t, orphan.Dir(), func(r *RunRecord) { r.PID = pid })

	legacy, _ := s.StartRun(RunRecord{Kind: "ask"})
	setRecord(t, legacy.Dir(), func(r *RunRecord) { r.PID = 0 })

	mismatch, _ := s.StartRun(RunRecord{Kind: "ask"})
	mismatch.Append(Capture{Label: "a", Content: "1"})
	mismatch.Finish(Finish{Status: StatusSucceeded})
	appendRaw(t, mismatch.Dir(), "{bad\n")
	appendRaw(t, mismatch.Dir(), `{"seq":3,"label":"cut`)

	if err := os.Mkdir(s.RunDir("20000101T000000Z-000000"), 0o755); err != nil {
		t.Fatal(err)
	}

	h, err := s.Check()
	if err != nil {
		t.Fatal(err)
	}
	if h.Runs != 5 || h.ByStatus[StatusRunning] != 3 || h.ByStatus[StatusSucceeded] != 2 || h.Captures != 2 {
		t.Errorf("health = %+v", h)
	}
	if len(h.NoRecord) != 1 || len(h.Orphaned) != 1 || h.Orphaned[0] != orphan.ID() ||
		len(h.Unverifiable) != 1 || h.Unverifiable[0] != legacy.ID() {
		t.Errorf("no record %v, orphaned %v, unverifiable %v", h.NoRecord, h.Orphaned, h.Unverifiable)
	}
	if len(h.BadLines[mismatch.ID()]) != 1 || len(h.Truncated) != 1 || h.Truncated[0] != mismatch.ID() {
		t.Errorf("bad lines %v, truncated %v", h.BadLines, h.Truncated)
	}
	// The record says 1 capture and the log holds 1 good line: the bad and
	// cut lines are reported as such, not as a count mismatch.
	if len(h.CountMismatch) != 0 {
		t.Errorf("count mismatch = %v", h.CountMismatch)
	}
	if h.Problems() != 4 || h.Oldest == nil || h.Newest == nil {
		t.Errorf("problems = %d, oldest %v newest %v", h.Problems(), h.Oldest, h.Newest)
	}
	for _, r := range []*Run{running} {
		for _, id := range h.Orphaned {
			if id == r.ID() {
				t.Errorf("live run %s reported orphaned", id)
			}
		}
	}

	// A record that claims more captures than the log holds.
	setRecord(t, ok.Dir(), func(r *RunRecord) { r.Captures = 5 })
	if h, _ := s.Check(); len(h.CountMismatch) != 1 || h.CountMismatch[0] != ok.ID() {
		t.Errorf("count mismatch = %v, want %s", h.CountMismatch, ok.ID())
	}
}
