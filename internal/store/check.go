package store

import (
	"os"
	"sort"
	"time"
)

// Health is what Check found in the store directory.
type Health struct {
	Runs     int            `json:"runs"`
	ByStatus map[string]int `json:"by_status"`
	Captures int            `json:"captures"`
	// NoRecord are run dirs with no readable run.json.
	NoRecord []string `json:"no_record,omitempty"`
	// Orphaned are runs left running by a process that's gone; ingest
	// marks them interrupted.
	Orphaned []string `json:"orphaned,omitempty"`
	// Unverifiable are runs still running with no PID recorded, so nothing
	// can tell whether their process is alive.
	Unverifiable []string `json:"unverifiable,omitempty"`
	// CountMismatch are finished runs whose record's capture count isn't
	// what captures.jsonl holds.
	CountMismatch []string `json:"count_mismatch,omitempty"`
	// BadLines maps runs to the capture lines that don't decode.
	BadLines map[string][]int `json:"bad_lines,omitempty"`
	// Truncated are finished runs whose capture log ends mid-line.
	Truncated []string `json:"truncated,omitempty"`
	Oldest    *time.Time `json:"oldest,omitempty"`
	Newest    *time.Time `json:"newest,omitempty"`
}

// Problems counts what Check found wrong. Running runs aren't problems;
// orphaned ones are, until ingest marks them.
func (h Health) Problems() int {
	return len(h.NoRecord) + len(h.Orphaned) + len(h.CountMismatch) + len(h.BadLines) + len(h.Truncated)
}

// Check reads every run in the store and reports its state. It changes
// nothing.
func (s *Store) Check() (Health, error) {
	h := Health{ByStatus: map[string]int{}}
	ids, err := s.RunIDs()
	if err != nil {
		return h, err
	}
	host, _ := os.Hostname()
	for _, id := range ids {
		dir := s.RunDir(id)
		rec, err := ReadRecord(dir)
		if err != nil {
			h.NoRecord = append(h.NoRecord, id)
			continue
		}
		h.Runs++
		h.ByStatus[rec.Status]++
		if h.Oldest == nil || rec.StartedAt.Before(*h.Oldest) {
			t := rec.StartedAt
			h.Oldest = &t
		}
		if h.Newest == nil || rec.StartedAt.After(*h.Newest) {
			t := rec.StartedAt
			h.Newest = &t
		}
		log, err := ScanCaptures(dir)
		if err != nil {
			h.NoRecord = append(h.NoRecord, id)
			continue
		}
		h.Captures += len(log.Captures)
		if len(log.BadLines) > 0 {
			if h.BadLines == nil {
				h.BadLines = map[string][]int{}
			}
			h.BadLines[id] = log.BadLines
		}
		running := rec.Status == StatusRunning
		switch {
		case Orphaned(rec, host):
			h.Orphaned = append(h.Orphaned, id)
		case running && rec.PID <= 0:
			h.Unverifiable = append(h.Unverifiable, id)
		}
		if !running && log.Partial {
			h.Truncated = append(h.Truncated, id)
		}
		if !running && rec.Status != StatusInterrupted && rec.Captures != len(log.Captures) {
			h.CountMismatch = append(h.CountMismatch, id)
		}
	}
	sort.Strings(h.NoRecord)
	return h, nil
}
