package store

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// CitationsFile is where the citation check writes a run's report
// citations.
const CitationsFile = "citations.jsonl"

// What the citation check found for a quote.
const (
	QuoteFound        = "found"
	QuoteNotFound     = "not_found"
	QuoteUnverifiable = "unverifiable" // the store has only an abstract or snippets, or no text
	QuoteUnchecked    = "unchecked"    // the target couldn't be looked up
)

// Citation is one citation marker in a run's report and how it resolved.
type Citation struct {
	Ord          int    `json:"ord"`
	Marker       string `json:"marker"`
	TargetKind   string `json:"target_kind"`
	TargetID     string `json:"target_id"`
	ReportOffset int    `json:"report_offset"`
	Group        int    `json:"group"`
	Quote        string `json:"quote,omitempty"`
	// Resolved is nil when the target couldn't be looked up, such as a
	// passage with no index to look in.
	Resolved    *bool  `json:"resolved"`
	QuoteStatus string `json:"quote_status,omitempty"`
	Note        string `json:"note,omitempty"`
}

// WriteCitations replaces the run's citations.jsonl.
func WriteCitations(dir string, cs []Citation) error {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	for _, c := range cs {
		if err := enc.Encode(c); err != nil {
			return err
		}
	}
	return writeAtomic(filepath.Join(dir, CitationsFile), b.Bytes())
}

// ReadCitations reads the run's citations.jsonl. ok is false when the
// check hasn't run.
func ReadCitations(dir string) (cs []Citation, ok bool, err error) {
	f, err := os.Open(filepath.Join(dir, CitationsFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for line := 1; sc.Scan(); line++ {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		var c Citation
		if err := json.Unmarshal(sc.Bytes(), &c); err != nil {
			return cs, true, fmt.Errorf("%s line %d: %w", CitationsFile, line, err)
		}
		cs = append(cs, c)
	}
	return cs, true, sc.Err()
}

// CitationsSize is the size of a run's citations.jsonl, -1 when there is
// none, so an empty file still counts as checked.
func CitationsSize(dir string) int64 {
	info, err := os.Stat(filepath.Join(dir, CitationsFile))
	if err != nil {
		return -1
	}
	return info.Size()
}
