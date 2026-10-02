// Package quote finds where a quoted string occurs in a document's text,
// so a claim's quote can be anchored to the text it came from. Matching
// ignores case, punctuation and spacing, and the footnote markers ("[12]")
// that extracted pages keep but a quoting model drops. An ellipsis in the
// quote skips text, within a bounded span.
package quote

import (
	"regexp"
	"strings"
	"unicode"
)

// ellipses are where a quote leaves text out.
var ellipses = regexp.MustCompile(`\[\s*(?:\.\.\.|…)\s*\]|\.\.\.|…`)

// MaxSkip is the most text, in characters, an ellipsis may leave out.
const MaxSkip = 400

// Locate returns the span of text that quote quotes, as rune offsets
// [start, end) into text, from the start of the quote's first word to the
// end of its last. ok is false when the quote has no words or isn't there.
func Locate(q, text string) (start, end int, ok bool) {
	var frags []string
	for _, f := range ellipses.Split(q, -1) {
		if f := normalizeQuote(f); f != "" {
			frags = append(frags, f)
		}
	}
	if len(frags) == 0 {
		return 0, 0, false
	}
	hay, offs := normalizeText(text)
	// Try each place the first fragment occurs: a later one may be the
	// one the rest follow.
	for from := 0; ; {
		i := strings.Index(hay[from:], frags[0])
		if i < 0 {
			return 0, 0, false
		}
		first := from + i
		at, last, matched := first+len(frags[0]), first+len(frags[0]), true
		for _, f := range frags[1:] {
			// The fragment's leading space is the previous one's
			// trailing space, so step back onto it.
			j := strings.Index(hay[at-1:], f)
			if j < 0 || j > MaxSkip {
				matched = false
				break
			}
			at = at - 1 + j + len(f)
			last = at
		}
		if matched {
			// The match runs from a word's leading space to the space
			// after the last word: report the words themselves.
			return offs[first+1], offs[last-2] + 1, true
		}
		from = first + 1
	}
}

// Key is s as Locate compares it: lowercase words separated by single
// spaces. Two spans with the same key quote the same words.
func Key(s string) string { return strings.TrimSpace(normalizeQuote(s)) }

// Found reports whether quote occurs in text.
func Found(q, text string) bool {
	_, _, ok := Locate(q, text)
	return ok
}

// footnote is a reference marker such as "[12]" or "[a]".
var footnote = regexp.MustCompile(`\[(?:\d{1,3}|[a-z])\]`)

// normalizeQuote lowercases s and turns everything but letters and digits
// into single spaces, with a space at each end so matches fall on word
// boundaries. It's blank when s has no letters or digits.
func normalizeQuote(s string) string {
	n, _ := normalize(s, false)
	return n
}

// normalizeText is normalizeQuote for the document, also dropping footnote
// markers, with the rune offset in text of each byte of the result. A
// space's offset is that of the character it replaced, or of the next one
// when it was added.
func normalizeText(s string) (string, []int) {
	return normalize(s, true)
}

func normalize(s string, dropFootnotes bool) (string, []int) {
	var skip [][]int
	if dropFootnotes {
		skip = footnote.FindAllStringIndex(s, -1)
	}
	var b strings.Builder
	offs := make([]int, 0, len(s)+2)
	b.WriteByte(' ')
	offs = append(offs, 0)
	space := true
	runeAt := 0
	for i, r := range s {
		for len(skip) > 0 && i >= skip[0][1] {
			skip = skip[1:]
		}
		inFootnote := len(skip) > 0 && i >= skip[0][0]
		if !inFootnote && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			for _, lr := range strings.ToLower(string(r)) {
				n := b.Len()
				b.WriteRune(lr)
				for range b.Len() - n {
					offs = append(offs, runeAt)
				}
			}
			space = false
		} else if !inFootnote && !space {
			b.WriteByte(' ')
			offs = append(offs, runeAt)
			space = true
		}
		runeAt++
	}
	if !space {
		b.WriteByte(' ')
		offs = append(offs, runeAt)
	}
	if b.Len() == 1 {
		return "", nil
	}
	return b.String(), offs
}
