// Package store is researchguy's on-disk research store (store.dir). Every
// research task gets a run directory holding its run record and everything
// it collected, written as it happens, so nothing a run gathered depends on
// fitting into one prompt or surviving to the end of the process.
//
//	<store.dir>/runs/<run_id>/
//	  run.json        run record: task, status, timing, report path, metadata
//	  captures.jsonl  one line per raw tool result, fsynced per batch
//	  <files>         worker drafts, the aggregator prompt and raw output
//
// See docs/research-store-design.md. This is phase 1: run records and the
// full evidence ledger. The index (Postgres) and fetching come later.
package store

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sync"
	"time"
)

// Run statuses. A run whose process dies stays "running"; nothing marks it
// interrupted yet.
const (
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
)

const (
	recordFile  = "run.json"
	captureFile = "captures.jsonl"
)

// Store is a store directory.
type Store struct {
	dir string
}

// Open returns the store rooted at dir, creating its runs directory.
func Open(dir string) (*Store, error) {
	if dir == "" {
		return nil, fmt.Errorf("store dir is empty")
	}
	if err := os.MkdirAll(filepath.Join(dir, "runs"), 0o755); err != nil {
		return nil, fmt.Errorf("creating store: %w", err)
	}
	return &Store{dir: dir}, nil
}

// Dir is the store's root directory.
func (s *Store) Dir() string { return s.dir }

// RunRecord is run.json.
type RunRecord struct {
	ID          string          `json:"id"`
	Kind        string          `json:"kind"`
	Topic       string          `json:"topic"`
	Backend     string          `json:"backend"`
	Mode        string          `json:"mode,omitempty"`
	BranchCount int             `json:"branch_count,omitempty"`
	Sources     []string        `json:"sources,omitempty"`
	Status      string          `json:"status"`
	Error       string          `json:"error,omitempty"`
	StartedAt   time.Time       `json:"started_at"`
	FinishedAt  *time.Time      `json:"finished_at,omitempty"`
	ReportPath  string          `json:"report_path,omitempty"`
	Captures    int             `json:"captures"`
	Files       []string        `json:"files,omitempty"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`
}

// Capture is one raw tool result, a line of captures.jsonl. Seq is unique
// within the run and is the number the aggregator cites as [E<seq>].
type Capture struct {
	Seq        int       `json:"seq"`
	CapturedAt time.Time `json:"captured_at"`
	// Worker is the 1-based hybrid worker that collected it, 0 for a
	// single-shot provider.
	Worker      int    `json:"worker,omitempty"`
	Shard       string `json:"shard,omitempty"`
	Backend     string `json:"backend,omitempty"`
	Model       string `json:"model,omitempty"`
	WorkerError string `json:"worker_error,omitempty"`
	Label       string `json:"label"`
	Content     string `json:"content"`
}

// Finish is what a run ends with.
type Finish struct {
	Status     string
	Error      string
	ReportPath string
	Metadata   string
}

// Run is an open run directory. A nil *Run is valid and records nothing,
// so callers without a store don't need to check.
type Run struct {
	dir string

	mu     sync.Mutex
	record RunRecord
}

// StartRun creates a run directory and writes its record with status
// running. ID, Status and StartedAt are set here.
func (s *Store) StartRun(rec RunRecord) (*Run, error) {
	now := time.Now().UTC()
	id, err := newRunID(now)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(s.dir, "runs", id)
	if err := os.Mkdir(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating run dir: %w", err)
	}
	rec.ID = id
	rec.Status = StatusRunning
	rec.StartedAt = now
	rec.FinishedAt = nil
	rec.Captures = 0
	rec.Files = nil
	r := &Run{dir: dir, record: rec}
	if err := r.writeRecord(); err != nil {
		return nil, err
	}
	return r, nil
}

// newRunID is a UTC timestamp, so run directories sort by start time, plus
// random bytes so parallel runs started in the same second don't collide.
func newRunID(t time.Time) (string, error) {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating run id: %w", err)
	}
	return t.Format("20060102T150405Z") + "-" + hex.EncodeToString(b), nil
}

// ID is the run's ID, "" for a nil run.
func (r *Run) ID() string {
	if r == nil {
		return ""
	}
	return r.record.ID
}

// Dir is the run's directory, "" for a nil run.
func (r *Run) Dir() string {
	if r == nil {
		return ""
	}
	return r.dir
}

// Record returns a copy of the run record as last written.
func (r *Run) Record() RunRecord {
	if r == nil {
		return RunRecord{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := r.record
	rec.Files = slices.Clone(r.record.Files)
	return rec
}

// Capture appends captures to captures.jsonl and fsyncs before returning.
// Callers number them; CapturedAt is set to now when zero.
func (r *Run) Capture(captures ...Capture) error {
	if r == nil || len(captures) == 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	f, err := os.OpenFile(filepath.Join(r.dir, captureFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("opening captures: %w", err)
	}
	now := time.Now().UTC()
	var buf []byte
	for _, c := range captures {
		if c.CapturedAt.IsZero() {
			c.CapturedAt = now
		}
		line, err := json.Marshal(c)
		if err != nil {
			f.Close()
			return fmt.Errorf("encoding capture %d: %w", c.Seq, err)
		}
		buf = append(append(buf, line...), '\n')
	}
	if _, err := f.Write(buf); err != nil {
		f.Close()
		return fmt.Errorf("writing captures: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("syncing captures: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing captures: %w", err)
	}
	r.record.Captures += len(captures)
	r.addFile(captureFile)
	return nil
}

var runFileName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// WriteFile atomically writes a file in the run directory. name is a plain
// file name; run.json and captures.jsonl are reserved.
func (r *Run) WriteFile(name string, data []byte) error {
	if r == nil {
		return nil
	}
	if !runFileName.MatchString(name) || name == recordFile || name == captureFile {
		return fmt.Errorf("invalid run file name %q", name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := writeAtomic(filepath.Join(r.dir, name), data); err != nil {
		return err
	}
	r.addFile(name)
	return nil
}

// Finish records how the run ended and rewrites run.json.
func (r *Run) Finish(f Finish) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().UTC()
	r.record.Status = f.Status
	r.record.Error = f.Error
	r.record.ReportPath = f.ReportPath
	r.record.FinishedAt = &now
	r.record.Metadata = metadataJSON(f.Metadata)
	return r.writeRecord()
}

// metadataJSON embeds provider metadata as-is when it's JSON, otherwise as
// a JSON string.
func metadataJSON(s string) json.RawMessage {
	if s == "" {
		return nil
	}
	if json.Valid([]byte(s)) {
		return json.RawMessage(s)
	}
	b, _ := json.Marshal(s)
	return b
}

func (r *Run) addFile(name string) {
	if !slices.Contains(r.record.Files, name) {
		r.record.Files = append(r.record.Files, name)
	}
}

func (r *Run) writeRecord() error {
	b, err := json.MarshalIndent(r.record, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding run record: %w", err)
	}
	return writeAtomic(filepath.Join(r.dir, recordFile), append(b, '\n'))
}

// writeAtomic writes data to a temp file beside path, fsyncs it and renames
// it over path, so a reader never sees a partial file.
func writeAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("writing %s: %w", filepath.Base(path), err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("syncing %s: %w", filepath.Base(path), err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", filepath.Base(path), err)
	}
	if err := os.Chmod(tmp, 0o644); err != nil {
		return fmt.Errorf("setting mode on %s: %w", filepath.Base(path), err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("renaming %s: %w", filepath.Base(path), err)
	}
	return nil
}

// ReadCaptures reads a run's captures.jsonl. A missing file is no captures.
func ReadCaptures(runDir string) ([]Capture, error) {
	data, err := os.ReadFile(filepath.Join(runDir, captureFile))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading captures: %w", err)
	}
	var out []Capture
	dec := json.NewDecoder(bytes.NewReader(data))
	for dec.More() {
		var c Capture
		if err := dec.Decode(&c); err != nil {
			return out, fmt.Errorf("decoding capture %d: %w", len(out)+1, err)
		}
		out = append(out, c)
	}
	return out, nil
}

// ReadRecord reads a run's run.json.
func ReadRecord(runDir string) (RunRecord, error) {
	var rec RunRecord
	data, err := os.ReadFile(filepath.Join(runDir, recordFile))
	if err != nil {
		return rec, fmt.Errorf("reading run record: %w", err)
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		return rec, fmt.Errorf("decoding run record: %w", err)
	}
	return rec, nil
}
