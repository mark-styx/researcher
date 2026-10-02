package store

import "unicode"

// Passage sizes, in characters (runes).
const (
	// PassageTarget is the size passages are built up to.
	PassageTarget = 1500
	// passageMax is the most a passage takes to avoid a small remainder,
	// and the size above which one paragraph is split.
	passageMax = 2000
	// passageMin is the size below which a passage takes in its
	// neighbor when the two fit under passageMax.
	passageMin = 300
)

// Passage is a span of a document's text. Start and End are character
// (rune) offsets into the text, End exclusive, and Text is exactly that
// span, so a quote found in it can be located in the document.
type Passage struct {
	Ord   int    `json:"ord"`
	Start int    `json:"start"`
	End   int    `json:"end"`
	Text  string `json:"text"`
}

type span struct{ start, end int }

// Passages splits text into passages of about PassageTarget characters on
// paragraph boundaries (blank lines). A paragraph longer than passageMax is
// split at a sentence end, or failing that a space. The result depends only
// on the text, so a rebuild produces the same passages.
func Passages(text string) []Passage {
	runes := []rune(text)
	var pieces []span
	for _, p := range paragraphs(runes) {
		pieces = append(pieces, splitLong(runes, p)...)
	}
	var spans []span
	for _, p := range pieces {
		n := len(spans)
		if n > 0 {
			cur := spans[n-1]
			merged := p.end - cur.start
			if merged <= PassageTarget || (cur.end-cur.start < passageMin && merged <= passageMax) {
				spans[n-1].end = p.end
				continue
			}
		}
		spans = append(spans, p)
	}
	// A short tail joins the passage before it when they fit together.
	if n := len(spans); n > 1 {
		last := spans[n-1]
		if last.end-last.start < passageMin && last.end-spans[n-2].start <= passageMax {
			spans[n-2].end = last.end
			spans = spans[:n-1]
		}
	}
	out := make([]Passage, len(spans))
	for i, s := range spans {
		out[i] = Passage{Ord: i, Start: s.start, End: s.end, Text: string(runes[s.start:s.end])}
	}
	return out
}

// paragraphs returns the trimmed spans between blank lines.
func paragraphs(r []rune) []span {
	var out []span
	start := 0
	emit := func(end int) {
		s, e := trim(r, start, end)
		if s < e {
			out = append(out, span{s, e})
		}
	}
	for i := 0; i < len(r); i++ {
		if r[i] != '\n' {
			continue
		}
		// A newline, optional spaces, and another newline end a paragraph.
		j := i + 1
		for j < len(r) && r[j] != '\n' && unicode.IsSpace(r[j]) {
			j++
		}
		if j < len(r) && r[j] == '\n' {
			emit(i)
			for j < len(r) && unicode.IsSpace(r[j]) {
				j++
			}
			start = j
			i = j - 1
		}
	}
	emit(len(r))
	return out
}

func trim(r []rune, s, e int) (int, int) {
	for s < e && unicode.IsSpace(r[s]) {
		s++
	}
	for e > s && unicode.IsSpace(r[e-1]) {
		e--
	}
	return s, e
}

// splitLong cuts a paragraph longer than passageMax into pieces of about
// PassageTarget characters.
func splitLong(r []rune, p span) []span {
	var out []span
	for p.end-p.start > passageMax {
		cut := cutPoint(r, p.start, p.start+PassageTarget)
		s, e := trim(r, p.start, cut)
		if s < e {
			out = append(out, span{s, e})
		}
		p.start, _ = trim(r, cut, p.end)
	}
	if p.start < p.end {
		out = append(out, p)
	}
	return out
}

// cutPoint finds where to end a piece that should end near limit: after the
// last sentence end in the second half of [start, limit), else at the last
// space there, else at limit.
func cutPoint(r []rune, start, limit int) int {
	floor := start + (limit-start)/2
	for i := limit - 1; i > floor; i-- {
		if (r[i-1] == '.' || r[i-1] == '?' || r[i-1] == '!' || r[i-1] == '\n') && unicode.IsSpace(r[i]) {
			return i
		}
	}
	for i := limit - 1; i > floor; i-- {
		if unicode.IsSpace(r[i]) {
			return i
		}
	}
	return limit
}
