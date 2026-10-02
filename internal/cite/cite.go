// Package cite finds the citation markers in a report and checks each one
// against what it cites: [E<seq>] a capture in the report's run, [P:<id>] a
// store passage, [S:<id>] a source. It also checks that a direct quote
// before a marker appears in the cited text. The check is deterministic:
// no model reads the report.
package cite

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// What a marker cites.
const (
	KindCapture = "capture"
	KindPassage = "passage"
	KindSource  = "source"
)

// Marker is one citation in a report.
type Marker struct {
	Ord    int    // 1-based, in report order
	Marker string // as cited: E12, P:123, S:456
	Kind   string // KindCapture, KindPassage or KindSource
	ID     string // the seq or store ID
	Offset int    // characters into the report, to the marker's bracket
	// Group numbers the citations made together, one bracket or adjacent
	// brackets. They share a quote.
	Group int
	Quote string // the direct quote right before the group, if any
}

var (
	bracketRE = regexp.MustCompile(`\[\s*((?:E\d+|[PS]:\d+)(?:\s*[,;]\s*(?:E\d+|[PS]:\d+))*)\s*\]`)
	markerRE  = regexp.MustCompile(`E\d+|[PS]:\d+`)
)

// MinQuoteWords is the shortest quoted string treated as a direct quote.
// Shorter ones are terms or scare quotes.
const MinQuoteWords = 4

// maxQuoteGap is how far, in characters, a quote's closing mark may sit
// before its citation.
const maxQuoteGap = 100

// Parse returns the report's citation markers in order.
func Parse(report string) []Marker {
	var out []Marker
	group, segStart, prevEnd := 0, 0, -1
	quote := ""
	for _, loc := range bracketRE.FindAllStringSubmatchIndex(report, -1) {
		start, end := loc[0], loc[1]
		// Brackets with only spaces or separators between them are one
		// group: "quote" [P:1][P:2].
		if prevEnd < 0 || strings.Trim(report[prevEnd:start], " \t,;") != "" {
			group++
			if prevEnd > 0 {
				segStart = prevEnd
			}
			quote = quoteBefore(report[segStart:start])
		}
		offset := utf8.RuneCountInString(report[:start])
		for _, m := range markerRE.FindAllString(report[loc[2]:loc[3]], -1) {
			mk := Marker{Ord: len(out) + 1, Marker: m, Offset: offset, Group: group, Quote: quote}
			switch m[0] {
			case 'E':
				mk.Kind, mk.ID = KindCapture, m[1:]
			case 'P':
				mk.Kind, mk.ID = KindPassage, m[2:]
			case 'S':
				mk.Kind, mk.ID = KindSource, m[2:]
			}
			out = append(out, mk)
		}
		prevEnd = end
	}
	return out
}

// quoteBefore returns the double-quoted string that ends at most
// maxQuoteGap characters before the end of text, on its last line, when it
// has at least MinQuoteWords words.
func quoteBefore(text string) string {
	if i := strings.LastIndexByte(text, '\n'); i >= 0 {
		text = text[i+1:]
	}
	closeAt, open := -1, ""
	for _, q := range []struct{ close, open string }{{`"`, `"`}, {"”", "“"}} {
		if i := strings.LastIndex(text, q.close); i > closeAt {
			closeAt, open = i, q.open
		}
	}
	if closeAt < 0 || utf8.RuneCountInString(text[closeAt:]) > maxQuoteGap {
		return ""
	}
	openAt := strings.LastIndex(text[:closeAt], open)
	if openAt < 0 {
		return ""
	}
	quote := strings.TrimSpace(text[openAt+len(open) : closeAt])
	if len(strings.Fields(quote)) < MinQuoteWords {
		return ""
	}
	return quote
}

// ellipses are where a quote leaves text out.
var ellipses = regexp.MustCompile(`\[\s*(?:\.\.\.|…)\s*\]|\.\.\.|…`)

// Found reports whether quote appears in text, ignoring case, punctuation
// and spacing. Text a quote leaves out with an ellipsis may be anything,
// but the remaining fragments must appear in order.
func Found(quote, text string) bool {
	hay := normalize(text)
	at := 0
	matched := false
	for _, frag := range ellipses.Split(quote, -1) {
		frag = normalize(frag)
		if frag == "" {
			continue
		}
		i := strings.Index(hay[at:], frag)
		if i < 0 {
			return false
		}
		// Keep the fragment's closing space: the next one starts with it.
		at += i + len(frag) - 1
		matched = true
	}
	return matched
}

// normalize lowercases s and turns everything but letters and digits into
// single spaces, with a space at each end so matches fall on word
// boundaries.
func normalize(s string) string {
	var b strings.Builder
	b.WriteByte(' ')
	space := true
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			space = false
			continue
		}
		if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	if !space {
		b.WriteByte(' ')
	}
	if b.Len() == 1 {
		return ""
	}
	return b.String()
}
