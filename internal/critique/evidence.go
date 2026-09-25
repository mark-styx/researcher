package critique

import (
	"fmt"
	"os"
	"unicode/utf8"
)

// DefaultMaxEvidenceChars bounds how much evidence one critique sends (both
// critics get it all), about 30k tokens.
const DefaultMaxEvidenceChars = 120_000

// Loaded is evidence read from files, with what the cap cut.
type Loaded struct {
	Evidence  []Evidence
	Chars     int
	Truncated bool
	// Skipped lists files dropped whole because the cap was already reached.
	Skipped []string
}

// LoadEvidence reads each file as one Evidence labeled with its path, in
// order, until maxChars (<= 0: DefaultMaxEvidenceChars) is reached. The file
// that crosses the cap is cut on a rune boundary; later files are skipped.
func LoadEvidence(paths []string, maxChars int) (*Loaded, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("no evidence files given")
	}
	if maxChars <= 0 {
		maxChars = DefaultMaxEvidenceChars
	}
	out := &Loaded{}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading evidence: %w", err)
		}
		room := maxChars - out.Chars
		if out.Truncated || room <= 0 {
			out.Skipped = append(out.Skipped, path)
			out.Truncated = true
			continue
		}
		content := string(data)
		if len(content) > room {
			content = truncateBytes(content, room)
			out.Truncated = true
		}
		out.Chars += len(content)
		out.Evidence = append(out.Evidence, Evidence{Label: path, Content: content})
	}
	return out, nil
}

func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
