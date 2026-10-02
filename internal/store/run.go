// Package store is researchguy's on-disk research store (store.dir). Every
// research task gets a run directory holding its run record and everything
// it collected, written as it happens, so nothing a run gathered depends on
// fitting into one prompt or surviving to the end of the process.
//
//	<store.dir>/runs/<run_id>/
//	  run.json        run record: task, status, timing, report path, metadata
//	  captures.jsonl  one line per raw tool result, fsynced as it arrives
//	  <files>         worker drafts, the aggregator prompt and raw output
//
// The on-disk store is the record of truth. The Postgres index
// (internal/store/index) is derived from it and can be rebuilt from it.
// See docs/research-store-design.md.
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

// Run statuses. A run whose process died before it finished stays
// "running" on disk until Reconcile marks it interrupted.
const (
	StatusRunning     = "running"
	StatusSucceeded   = "succeeded"
	StatusFailed      = "failed"
	StatusInterrupted = "interrupted"
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
	ID          string   `json:"id"`
	Kind        string   `json:"kind"`
	Topic       string   `json:"topic"`
	Backend     string   `json:"backend"`
	Mode        string   `json:"mode,omitempty"`
	BranchCount int      `json:"branch_count,omitempty"`
	Sources     []string `json:"sources,omitempty"`
	Status      string   `json:"status"`
	Error       string   `json:"error,omitempty"`
	// PID and Host are the process that ran it, so a run left "running" by
	// a process that died can be told apart from one still in progress.
	PID        int             `json:"pid,omitempty"`
	Host       string          `json:"host,omitempty"`
	StartedAt  time.Time       `json:"started_at"`
	FinishedAt *time.Time      `json:"finished_at,omitempty"`
	ReportPath string          `json:"report_path,omitempty"`
	Captures   int             `json:"captures"`
	Files      []string        `json:"files,omitempty"`
	Metadata   json.RawMessage `json:"metadata,omitempty"`
}

// Capture is one raw tool result, a line of captures.jsonl. Seq is unique
// within the run and is the number the aggregator cites as [E<seq>].
type Capture struct {
	Seq        int       `json:"seq"`
	CapturedAt time.Time `json:"captured_at"`
	// Worker is the 1-based hybrid worker that collected it, 0 for a
	// single-shot provider.
	Worker  int    `json:"worker,omitempty"`
	Shard   string `json:"shard,omitempty"`
	Backend string `json:"backend,omitempty"`
	Model   string `json:"model,omitempty"`
	// WorkerError is set only on captures written after their worker
	// failed. Captures written as they arrive can't know how it ended;
	// workers.jsonl has every worker's error.
	WorkerError string `json:"worker_error,omitempty"`
	Call
	Label   string `json:"label"`
	Content string `json:"content"`
}

// Call is the tool call a capture came from. Captures written before
// phase 2 have only Label and Content.
type Call struct {
	Tool string `json:"tool,omitempty"` // web_search, web_fetch
	// Action is what the tool did: search, open (a page the model read),
	// find_in_page, or fetch.
	Action  string          `json:"action,omitempty"`
	Query   string          `json:"query,omitempty"`
	URL     string          `json:"url,omitempty"` // the page opened or fetched
	Results []CaptureResult `json:"results,omitempty"`
}

// CaptureResult is one item a search or page open returned. Rank is the
// 1-based position among the search results in the capture, 0 for a page
// the model opened.
type CaptureResult struct {
	Rank    int    `json:"rank,omitempty"`
	Opened  bool   `json:"opened,omitempty"`
	Title   string `json:"title,omitempty"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
	RefID   string `json:"ref_id,omitempty"`
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
	seq    int // last seq handed out
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
	rec.PID = os.Getpid()
	rec.Host, _ = os.Hostname()
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

// Append numbers captures with the run's next seqs, appends them to
// captures.jsonl and fsyncs before returning. The run is the only source of
// seqs, so captures from parallel workers never share one. The seqs are
// returned, and used up, even when the write fails, so a number already
// cited is never handed out twice. CapturedAt is set to now when zero.
func (r *Run) Append(captures ...Capture) ([]int, error) {
	if r == nil || len(captures) == 0 {
		return nil, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().UTC()
	seqs := make([]int, len(captures))
	var buf []byte
	var encErr error
	for i, c := range captures {
		r.seq++
		c.Seq = r.seq
		seqs[i] = c.Seq
		if c.CapturedAt.IsZero() {
			c.CapturedAt = now
		}
		line, err := json.Marshal(c)
		if err != nil {
			encErr = fmt.Errorf("encoding capture %d: %w", c.Seq, err)
			continue
		}
		buf = append(append(buf, line...), '\n')
	}
	written, err := r.appendLines(buf)
	r.record.Captures += written
	if written > 0 {
		r.addFile(captureFile)
	}
	if err != nil {
		return seqs, err
	}
	return seqs, encErr
}

// appendLines appends buf, whole lines, to captures.jsonl and fsyncs. It
// reports how many lines are on disk.
func (r *Run) appendLines(buf []byte) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	f, err := os.OpenFile(filepath.Join(r.dir, captureFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, fmt.Errorf("opening captures: %w", err)
	}
	if _, err := f.Write(buf); err != nil {
		f.Close()
		return 0, fmt.Errorf("writing captures: %w", err)
	}
	lines := bytes.Count(buf, []byte{'\n'})
	if err := f.Sync(); err != nil {
		f.Close()
		return lines, fmt.Errorf("syncing captures: %w", err)
	}
	if err := f.Close(); err != nil {
		return lines, fmt.Errorf("closing captures: %w", err)
	}
	return lines, nil
}

var runFileName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// reservedFiles are the run files the store writes itself.
var reservedFiles = []string{recordFile, captureFile, fetchFile, fetchSummaryFile, fetchLockFile}

// WriteFile atomically writes a file in the run directory. name is a plain
// file name; the store's own files (run.json, captures.jsonl, the fetch
// log) are reserved.
func (r *Run) WriteFile(name string, data []byte) error {
	if r == nil {
		return nil
	}
	if !runFileName.MatchString(name) || slices.Contains(reservedFiles, name) {
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
	return writeRecordFile(r.dir, r.record)
}

func writeRecordFile(dir string, rec RunRecord) error {
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding run record: %w", err)
	}
	return writeAtomic(filepath.Join(dir, recordFile), append(b, '\n'))
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
// A line that doesn't decode is an error; ScanCaptures reads past it.
func ReadCaptures(runDir string) ([]Capture, error) {
	log, err := ScanCaptures(runDir)
	if err != nil {
		return log.Captures, err
	}
	if len(log.BadLines) > 0 {
		return log.Captures, fmt.Errorf("decoding capture line %d: %s", log.BadLines[0], log.BadErr)
	}
	return log.Captures, nil
}

// CaptureLog is what ScanCaptures found in a captures.jsonl.
type CaptureLog struct {
	Captures []Capture
	// Bytes is the length of the complete lines read. A last line without
	// a newline is a write still in progress (or cut off by a crash), and
	// is neither decoded nor counted. Size is the whole file.
	Bytes    int64
	Size     int64
	Partial  bool
	BadLines []int // 1-based numbers of complete lines that didn't decode
	BadErr   string
}

// ScanCaptures reads every complete line of a run's captures.jsonl,
// skipping lines that don't decode instead of stopping at them.
func ScanCaptures(runDir string) (CaptureLog, error) {
	var log CaptureLog
	data, err := os.ReadFile(filepath.Join(runDir, captureFile))
	if os.IsNotExist(err) {
		return log, nil
	}
	if err != nil {
		return log, fmt.Errorf("reading captures: %w", err)
	}
	log.Size = int64(len(data))
	for n := 1; len(data) > 0; n++ {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			log.Partial = true
			break
		}
		line := data[:i]
		data = data[i+1:]
		log.Bytes += int64(i + 1)
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var c Capture
		if err := json.Unmarshal(line, &c); err != nil {
			log.BadLines = append(log.BadLines, n)
			if log.BadErr == "" {
				log.BadErr = err.Error()
			}
			continue
		}
		log.Captures = append(log.Captures, c)
	}
	return log, nil
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
