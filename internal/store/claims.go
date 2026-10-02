package store

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Claims extracted from a document's text, one file per text and
// extractor:
//
//	<store.dir>/claims/<extractor>/<ab>/<text hash>.json
//
// The extractor names the model and the prompt version, so changing
// either extracts again into new files rather than mixing two extractions.
// Links between claims go in one append-only log, <store.dir>/links.jsonl.

// Extraction is what an extractor found in one text.
type Extraction struct {
	TextSHA     string           `json:"text_sha256"`
	Extractor   string           `json:"extractor"`
	Model       string           `json:"model"`
	ExtractedAt time.Time        `json:"extracted_at"`
	DurationMS  int64            `json:"duration_ms"`
	Chunks      int              `json:"chunks"`
	Claims      []ExtractedClaim `json:"claims"`
	// Failed lists the chunks the model gave nothing usable for, with the
	// error. An extraction with failures is tried again, up to Attempts.
	Failed   []ChunkError `json:"failed,omitempty"`
	Attempts int          `json:"attempts"`
}

// ExtractedClaim is a claim as the model wrote it. Whether its quote is in
// the text is checked when it's indexed, not trusted from here.
type ExtractedClaim struct {
	Text     string `json:"text"`
	Quote    string `json:"quote"`
	AsOf     string `json:"as_of,omitempty"`
	Volatile bool   `json:"volatile"`
	Chunk    int    `json:"chunk"`
}

// ChunkError is a chunk an extraction failed on.
type ChunkError struct {
	Chunk int    `json:"chunk"`
	Error string `json:"error"`
}

// Complete reports whether every chunk was extracted.
func (e Extraction) Complete() bool { return len(e.Failed) == 0 }

func (s *Store) extractionPath(extractor, textSHA string) (string, error) {
	if !sha256Hex.MatchString(textSHA) {
		return "", fmt.Errorf("invalid text hash %q", textSHA)
	}
	name := ExtractorDir(extractor)
	if name == "" {
		return "", fmt.Errorf("invalid extractor %q", extractor)
	}
	return filepath.Join(s.dir, "claims", name, textSHA[:2], textSHA+".json"), nil
}

// ExtractorDir is the directory name an extractor's files go under.
func ExtractorDir(extractor string) string {
	return strings.Trim(unsafeModelChars.ReplaceAllString(extractor, "_"), "._")
}

// PutExtraction writes e, replacing an earlier attempt at the same text.
func (s *Store) PutExtraction(e Extraction) error {
	path, err := s.extractionPath(e.Extractor, e.TextSHA)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	return putFile(path, append(b, '\n'))
}

// ReadExtraction reads the extraction of textSHA by extractor. ok is false
// when there isn't one.
func (s *Store) ReadExtraction(extractor, textSHA string) (e Extraction, ok bool, err error) {
	path, err := s.extractionPath(extractor, textSHA)
	if err != nil {
		return e, false, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return e, false, nil
	}
	if err != nil {
		return e, false, err
	}
	if err := json.Unmarshal(b, &e); err != nil {
		return e, false, fmt.Errorf("extraction %s: %w", path, err)
	}
	return e, true, nil
}

// ExtractionFile is an extraction on disk, as Extractions lists it.
type ExtractionFile struct {
	Path    string
	TextSHA string
	Dir     string // the extractor's directory
	Size    int64
}

// Extractions lists every extraction file, of every extractor.
func (s *Store) Extractions() ([]ExtractionFile, error) {
	root := filepath.Join(s.dir, "claims")
	var out []ExtractionFile
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) && path == root {
				return filepath.SkipDir
			}
			return err
		}
		name := d.Name()
		if d.IsDir() || !strings.HasSuffix(name, ".json") || strings.HasPrefix(name, ".") {
			return nil
		}
		sha := strings.TrimSuffix(name, ".json")
		if !sha256Hex.MatchString(sha) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		out = append(out, ExtractionFile{Path: path, TextSHA: sha, Dir: strings.SplitN(rel, string(filepath.Separator), 2)[0], Size: info.Size()})
		return nil
	})
	return out, err
}

// ReadExtractionFile reads an extraction Extractions listed.
func ReadExtractionFile(f ExtractionFile) (Extraction, error) {
	var e Extraction
	b, err := os.ReadFile(f.Path)
	if err != nil {
		return e, err
	}
	if err := json.Unmarshal(b, &e); err != nil {
		return e, fmt.Errorf("extraction %s: %w", f.Path, err)
	}
	return e, nil
}

// LinksFile is the claim link log.
const LinksFile = "links.jsonl"

// How a link was made.
const (
	LinkRule  = "rule"  // same quote, same DOI: needs no judgment
	LinkModel = "model" // a model's label for the pair, an inference
	LinkHuman = "human" // set by hand; overrides the others for its pair
)

// Relations between claims. Unrelated is a label too: a pair a model
// judged unrelated isn't asked about again.
const (
	RelSame        = "same"
	RelSupports    = "supports"
	RelContradicts = "contradicts"
	RelRefines     = "refines"
	RelSupersedes  = "supersedes"
	RelUnrelated   = "unrelated"
)

// Relations lists every relation, unrelated last.
var Relations = []string{RelSame, RelSupports, RelContradicts, RelRefines, RelSupersedes, RelUnrelated}

// Link is one line of the link log: From relates to To. For supersedes,
// From is the newer claim. A later line for the same pair and method
// replaces an earlier one.
type Link struct {
	From       int64     `json:"from"`
	To         int64     `json:"to"`
	Relation   string    `json:"relation"`
	Method     string    `json:"method"`
	Model      string    `json:"model,omitempty"`
	Confidence float64   `json:"confidence,omitempty"`
	Note       string    `json:"note,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// Check reports what's wrong with l, if anything.
func (l Link) Check() error {
	if l.From == 0 || l.To == 0 || l.From == l.To {
		return fmt.Errorf("a link needs two different claims (got %d and %d)", l.From, l.To)
	}
	ok := false
	for _, r := range Relations {
		ok = ok || l.Relation == r
	}
	if !ok {
		return fmt.Errorf("unknown relation %q (want one of %s)", l.Relation, strings.Join(Relations, ", "))
	}
	if l.Method != LinkModel && l.Method != LinkHuman {
		return fmt.Errorf("link method %q isn't logged (want %s or %s)", l.Method, LinkModel, LinkHuman)
	}
	return nil
}

// AppendLinks adds links to the log, each as one line, fsynced, under an
// exclusive lock so two writers' lines don't interleave.
func (s *Store) AppendLinks(links []Link) error {
	if len(links) == 0 {
		return nil
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	for _, l := range links {
		if err := l.Check(); err != nil {
			return err
		}
		if err := enc.Encode(l); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(s.dir, ".links.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("opening link lock: %w", err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("locking links: %w", err)
	}
	path := filepath.Join(s.dir, LinksFile)
	data := b.Bytes()
	// A cut-off last line was never finished; start on a new one.
	if info, err := os.Stat(path); err == nil && info.Size() > 0 {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		last := make([]byte, 1)
		_, err = f.ReadAt(last, info.Size()-1)
		f.Close()
		if err != nil {
			return err
		}
		if last[0] != '\n' {
			data = append([]byte("\n"), data...)
		}
	}
	return appendSynced(path, data)
}

// LinkLog is what ReadLinks found.
type LinkLog struct {
	Links    []Link
	Size     int64 // bytes of complete lines read
	BadLines []int
}

// ReadLinks reads the link log from byte offset from (0 for all of it),
// complete lines only. A missing log is no links.
func (s *Store) ReadLinks(from int64) (LinkLog, error) {
	out := LinkLog{Size: from}
	f, err := os.Open(filepath.Join(s.dir, LinksFile))
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	defer f.Close()
	if _, err := f.Seek(from, 0); err != nil {
		return out, err
	}
	r := bufio.NewReaderSize(f, 64<<10)
	n := 0
	for {
		line, err := r.ReadBytes('\n')
		if len(line) == 0 || line[len(line)-1] != '\n' {
			// End of file, or a line still being written.
			break
		}
		n++
		out.Size += int64(len(line))
		if len(bytes.TrimSpace(line)) > 0 {
			var l Link
			if json.Unmarshal(line, &l) != nil || l.Check() != nil {
				out.BadLines = append(out.BadLines, n)
			} else {
				out.Links = append(out.Links, l)
			}
		}
		if err != nil {
			break
		}
	}
	return out, nil
}

// LinksSize is the link log's size in bytes, 0 when there isn't one.
func (s *Store) LinksSize() int64 {
	info, err := os.Stat(filepath.Join(s.dir, LinksFile))
	if err != nil {
		return 0
	}
	return info.Size()
}
