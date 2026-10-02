package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"time"
)

// RunIDs lists the store's run IDs, oldest first. Directories without a
// run.json are included, so callers can report them.
func (s *Store) RunIDs() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(s.dir, "runs"))
	if err != nil {
		return nil, fmt.Errorf("listing runs: %w", err)
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() && runFileName.MatchString(e.Name()) {
			ids = append(ids, e.Name())
		}
	}
	slices.Sort(ids)
	return ids, nil
}

// RunDir is the directory of run id.
func (s *Store) RunDir(id string) string {
	return filepath.Join(s.dir, "runs", id)
}

// Reconcile marks interrupted every run whose process died before it
// finished: status running, started on this host, and no process left with
// its PID. Their capture counts are set from captures.jsonl and their end
// time from the newest file in the run directory. A run with no PID
// (recorded before PIDs were) is left alone, as is one whose PID is in use,
// even if a later process took it. It returns the IDs it marked.
func (s *Store) Reconcile() ([]string, error) {
	ids, err := s.RunIDs()
	if err != nil {
		return nil, err
	}
	host, _ := os.Hostname()
	var marked []string
	for _, id := range ids {
		dir := s.RunDir(id)
		rec, err := ReadRecord(dir)
		if err != nil || !Orphaned(rec, host) {
			continue
		}
		log, err := ScanCaptures(dir)
		if err != nil {
			return marked, fmt.Errorf("run %s: %w", id, err)
		}
		end := lastModified(dir)
		rec.Status = StatusInterrupted
		rec.Error = fmt.Sprintf("process %d exited before the run finished", rec.PID)
		rec.FinishedAt = &end
		rec.Captures = len(log.Captures)
		if err := writeRecordFile(dir, rec); err != nil {
			return marked, fmt.Errorf("run %s: %w", id, err)
		}
		marked = append(marked, id)
	}
	return marked, nil
}

// Orphaned reports whether rec is a run left running by a process on host
// that no longer exists.
func Orphaned(rec RunRecord, host string) bool {
	if rec.Status != StatusRunning || rec.PID <= 0 {
		return false
	}
	if rec.Host != "" && rec.Host != host {
		return false
	}
	return !processExists(rec.PID)
}

// processExists reports whether a process with pid exists. A process owned
// by another user (EPERM) exists.
func processExists(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	if err == nil || errors.Is(err, syscall.EPERM) {
		return true
	}
	return !errors.Is(err, os.ErrProcessDone) && !errors.Is(err, syscall.ESRCH)
}

// lastModified is the newest modification time of the files in dir, which
// for a dead run is the last time it wrote anything.
func lastModified(dir string) time.Time {
	var newest time.Time
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if info, err := e.Info(); err == nil && info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	return newest.UTC()
}

// OfRunDir is the store holding the run directory dir
// (<store.dir>/runs/<id>).
func OfRunDir(dir string) *Store {
	return &Store{dir: filepath.Dir(filepath.Dir(filepath.Clean(dir)))}
}
