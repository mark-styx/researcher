package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

const (
	fetchFile        = "fetches.jsonl"
	fetchSummaryFile = "fetch.json"
	fetchLockFile    = "fetch.lock"
)

// Fetch reasons: why a URL was fetched for a run.
const (
	ReasonCited  = "cited"  // linked from a worker draft or the report
	ReasonOpened = "opened" // a page a worker opened or fetched
	ReasonResult = "result" // a top-ranked search result
	ReasonIngest = "ingest" // asked for directly
)

// Content kinds of fetched text.
const (
	KindFull     = "full"     // the page or paper itself
	KindAbstract = "abstract" // an abstract only, from OpenAlex
)

// FetchRecord is one line of a run's fetches.jsonl: one try at getting a
// source's content, failed or not. A source can have several, such as the
// publisher page refusing, then OpenAlex's abstract, then a repository
// copy. URL is always the source's; FetchURL is what was requested when
// that differs.
type FetchRecord struct {
	Seq         int       `json:"seq"`
	URL         string    `json:"url"`
	FetchURL    string    `json:"fetch_url,omitempty"`
	Reason      string    `json:"reason"`
	Rank        int       `json:"rank,omitempty"`
	Via         string    `json:"via"` // direct, openalex, repository, open_access
	AttemptedAt time.Time `json:"attempted_at"`
	Attempts    int       `json:"attempts"`
	DurationMS  int64     `json:"duration_ms"`
	HTTPStatus  int       `json:"http_status,omitempty"`
	FinalURL    string    `json:"final_url,omitempty"`
	ContentType string    `json:"content_type,omitempty"`
	Error       string    `json:"error,omitempty"`
	RawSHA256   string    `json:"raw_sha256,omitempty"`
	RawBytes    int64     `json:"raw_bytes,omitempty"`
	// TextSHA256 is the extracted text in the text store, "" when nothing
	// usable came back.
	TextSHA256  string     `json:"text_sha256,omitempty"`
	TextChars   int        `json:"text_chars,omitempty"`
	ContentKind string     `json:"content_kind,omitempty"`
	Title       string     `json:"title,omitempty"`
	DOI         string     `json:"doi,omitempty"`
	Published   *Published `json:"published,omitempty"`
}

// Published is when a source says it was published, and how that's known.
// An unknown date is a nil *Published, never the fetch date.
type Published struct {
	Date      string `json:"date"`      // YYYY, YYYY-MM or YYYY-MM-DD
	Precision string `json:"precision"` // year, month, day
	From      string `json:"from"`      // the meta tag or service it came from
	// Weak marks dates that say when a file changed rather than when it
	// was published: HTTP Last-Modified, a PDF's creation date.
	Weak bool `json:"weak,omitempty"`
}

// FetchSummary is fetch.json, written when a run's fetch stage ends.
// Remaining counts URLs the time budget didn't reach; a later fetch picks
// them up.
type FetchSummary struct {
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	Queued     int       `json:"queued"`
	Fetched    int       `json:"fetched"` // got text
	Failed     int       `json:"failed"`
	Skipped    int       `json:"skipped"` // fetched by an earlier pass
	Remaining  int       `json:"remaining"`
	Records    int       `json:"records"`
}

// Done reports whether the fetch stage has reached every URL it planned.
func (f FetchSummary) Done() bool { return f.Remaining == 0 }

// ErrFetchBusy means another process is fetching for the run.
var ErrFetchBusy = errors.New("another process is fetching for this run")

// FetchLog appends to a run's fetches.jsonl. It holds the run's fetch lock
// until Close, so one process at a time fetches for a run, and it numbers
// records after those already in the log.
type FetchLog struct {
	dir  string
	lock *os.File

	mu  sync.Mutex
	seq int
}

// OpenFetchLog takes the fetch lock of the run in dir without waiting:
// ErrFetchBusy if another process holds it.
func OpenFetchLog(dir string) (*FetchLog, error) {
	f, err := os.OpenFile(filepath.Join(dir, fetchLockFile), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening fetch lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrFetchBusy
		}
		return nil, fmt.Errorf("locking fetches: %w", err)
	}
	log, err := ScanFetches(dir)
	if err != nil {
		f.Close()
		return nil, err
	}
	seq := 0
	for _, r := range log.Records {
		seq = max(seq, r.Seq)
	}
	// A cut-off last line is a record that was never finished; finish the
	// line so the next record starts on its own.
	if log.Partial {
		if err := appendSynced(filepath.Join(dir, fetchFile), []byte("\n")); err != nil {
			f.Close()
			return nil, err
		}
	}
	return &FetchLog{dir: dir, lock: f, seq: seq}, nil
}

// Append numbers rec, writes it as one fsynced line, and returns it.
func (l *FetchLog) Append(rec FetchRecord) (FetchRecord, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	rec.Seq = l.seq
	line, err := json.Marshal(rec)
	if err != nil {
		return rec, fmt.Errorf("encoding fetch record: %w", err)
	}
	return rec, appendSynced(filepath.Join(l.dir, fetchFile), append(line, '\n'))
}

// WriteSummary writes fetch.json.
func (l *FetchLog) WriteSummary(sum FetchSummary) error {
	b, err := json.MarshalIndent(sum, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(l.dir, fetchSummaryFile), append(b, '\n'))
}

// Close releases the fetch lock.
func (l *FetchLog) Close() error {
	if l == nil || l.lock == nil {
		return nil
	}
	err := l.lock.Close()
	l.lock = nil
	return err
}

func appendSynced(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("opening %s: %w", filepath.Base(path), err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("writing %s: %w", filepath.Base(path), err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("syncing %s: %w", filepath.Base(path), err)
	}
	return f.Close()
}

// FetchLogScan is what ScanFetches found in a fetches.jsonl, read the way
// ScanCaptures reads captures: complete lines only, bad lines skipped.
type FetchLogScan struct {
	Records  []FetchRecord
	Size     int64
	Partial  bool
	BadLines []int
}

// ScanFetches reads a run's fetches.jsonl. A missing file is no records.
func ScanFetches(dir string) (FetchLogScan, error) {
	var out FetchLogScan
	data, err := os.ReadFile(filepath.Join(dir, fetchFile))
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return out, fmt.Errorf("reading fetches: %w", err)
	}
	out.Size = int64(len(data))
	for n := 1; len(data) > 0; n++ {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			out.Partial = true
			break
		}
		line := data[:i]
		data = data[i+1:]
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var r FetchRecord
		if err := json.Unmarshal(line, &r); err != nil {
			out.BadLines = append(out.BadLines, n)
			continue
		}
		out.Records = append(out.Records, r)
	}
	return out, nil
}

// ReadFetchSummary reads a run's fetch.json. ok is false when the run's
// fetch stage hasn't finished a pass.
func ReadFetchSummary(dir string) (sum FetchSummary, ok bool, err error) {
	data, err := os.ReadFile(filepath.Join(dir, fetchSummaryFile))
	if os.IsNotExist(err) {
		return sum, false, nil
	}
	if err != nil {
		return sum, false, err
	}
	if err := json.Unmarshal(data, &sum); err != nil {
		return sum, false, fmt.Errorf("decoding fetch summary: %w", err)
	}
	return sum, true, nil
}

// FetchSize is the size of a run's fetches.jsonl, 0 when there is none.
func FetchSize(dir string) int64 {
	info, err := os.Stat(filepath.Join(dir, fetchFile))
	if err != nil {
		return 0
	}
	return info.Size()
}

// PendingFetch lists finished runs whose fetch stage hasn't run, or ran
// out of time before reaching every URL. Running runs are left for when
// they finish.
func (s *Store) PendingFetch() ([]string, error) {
	ids, err := s.RunIDs()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, id := range ids {
		dir := s.RunDir(id)
		rec, err := ReadRecord(dir)
		if err != nil || rec.Status == StatusRunning {
			continue
		}
		sum, ok, err := ReadFetchSummary(dir)
		if err == nil && ok && sum.Done() {
			continue
		}
		out = append(out, id)
	}
	return out, nil
}
