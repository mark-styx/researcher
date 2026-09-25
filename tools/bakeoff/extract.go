package main

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/marklubin/researchguy/internal/graph"
)

// Doc is one research output split into the parts the metrics care about.
type Doc struct {
	Body     string              // prose, minus sources block, references, and critic notes
	Sources  []graph.SourceEntry // bookworm SOURCES_START..SOURCES_END entries
	Refs     []Ref               // entries of a References/Sources/Bibliography section
	Critic   string              // the "Critic Notes" section, if any
	Sections []Section           // headed sections of Body
}

// Ref is one entry of a references list. Num is its [n] or "n." label, 0 if none.
type Ref struct {
	Num  int
	Text string
	URLs []string
}

// Section is a markdown heading and the text under it (up to the next
// heading of any level).
type Section struct {
	Heading string
	Level   int
	Text    string
}

var (
	urlRe       = regexp.MustCompile(`https?://[^\s<>"'` + "`" + `\]\[{}|\\^]+`)
	headingRe   = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)
	refHeadRe   = regexp.MustCompile(`(?i)^(?:(?:key|selected|primary|full)\s+)?(?:references|sources|bibliography|works cited|citations|sources consulted|sources cited)(?:\s+(?:and|&)\s+(?:sources|references|further reading|notes|citations))?(?:\s*\(.*\))?:?$`)
	headNumRe   = regexp.MustCompile(`^(?:[IVXLC]+|\d+(?:\.\d+)*)[.)]?\s+`)
	refNumRe    = regexp.MustCompile(`^\s*(?:\[(\d+)\]|(\d+)[.)])\s+`)
	listItemRe  = regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)]|\[\d+\])\s+`)
	sourcesJSON = regexp.MustCompile(`(?s)(?:` + "```" + `json\s*)?SOURCES_START\s*(.*?)\s*SOURCES_END\s*(?:` + "```" + `)?`)
)

// ExtractURLs returns every http(s) URL in text, in order, with trailing
// punctuation and unbalanced closing parentheses trimmed. A Wikipedia-style
// URL like /wiki/Foo_(bar) keeps its balanced parentheses.
func ExtractURLs(text string) []string {
	var out []string
	for _, m := range urlRe.FindAllString(text, -1) {
		u := trimURL(m)
		if len(u) > len("https://") {
			out = append(out, u)
		}
	}
	return out
}

func trimURL(u string) string {
	for {
		trimmed := strings.TrimRight(u, ".,;:!?*_'\"")
		if strings.HasSuffix(trimmed, ")") && strings.Count(trimmed, "(") < strings.Count(trimmed, ")") {
			trimmed = trimmed[:len(trimmed)-1]
		}
		if trimmed == u {
			return u
		}
		u = trimmed
	}
}

// UniqueURLs maps normalized URL identity to the first raw URL seen for it.
// URLs that fail to normalize are skipped.
func UniqueURLs(urls []string) map[string]string {
	out := make(map[string]string)
	for _, u := range urls {
		key, err := graph.NormalizeURL(u)
		if err != nil {
			continue
		}
		if _, ok := out[key]; !ok {
			out[key] = u
		}
	}
	return out
}

// ParseDoc splits a research output into body, sources, references, and
// critic notes.
func ParseDoc(md string) Doc {
	var d Doc

	if m := sourcesJSON.FindStringSubmatchIndex(md); m != nil {
		var entries []graph.SourceEntry
		if err := json.Unmarshal([]byte(md[m[2]:m[3]]), &entries); err == nil {
			d.Sources = entries
		}
		md = md[:m[0]] + md[m[1]:]
	}

	var body strings.Builder
	var refLines, criticLines []string
	mode := "body" // body | refs | critic
	modeLevel := 0
	for _, line := range strings.Split(md, "\n") {
		if h := headingRe.FindStringSubmatch(line); h != nil {
			level := len(h[1])
			title := headingTitle(h[2])
			switch {
			case strings.EqualFold(title, "Critic Notes"):
				mode, modeLevel = "critic", level
				continue
			case mode != "body" && level > modeLevel:
				// Subheading inside references or critic notes.
			case refHeadRe.MatchString(title):
				mode, modeLevel = "refs", level
				continue
			default:
				mode = "body"
			}
		}
		switch mode {
		case "refs":
			refLines = append(refLines, line)
		case "critic":
			criticLines = append(criticLines, line)
		default:
			body.WriteString(line)
			body.WriteString("\n")
		}
	}
	d.Body = strings.TrimRight(body.String(), "\n")
	d.Refs = parseRefs(refLines)
	d.Critic = strings.TrimSpace(strings.Join(criticLines, "\n"))
	d.Sections = splitSections(d.Body)
	return d
}

func parseRefs(lines []string) []Ref {
	var refs []Ref
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if t == "" || t == "---" || headingRe.MatchString(t) {
			continue
		}
		if !listItemRe.MatchString(line) {
			// Continuation of the previous entry.
			if len(refs) > 0 {
				last := &refs[len(refs)-1]
				last.Text += " " + t
				last.URLs = append(last.URLs, ExtractURLs(t)...)
			}
			continue
		}
		r := Ref{}
		if m := refNumRe.FindStringSubmatch(line); m != nil {
			n := m[1]
			if n == "" {
				n = m[2]
			}
			r.Num = atoi(n)
		}
		r.Text = strings.TrimSpace(listItemRe.ReplaceAllString(line, ""))
		r.URLs = ExtractURLs(r.Text)
		refs = append(refs, r)
	}
	return refs
}

func splitSections(body string) []Section {
	var out []Section
	cur := Section{}
	var text []string
	flush := func() {
		cur.Text = strings.TrimSpace(strings.Join(text, "\n"))
		if cur.Heading != "" || cur.Text != "" {
			out = append(out, cur)
		}
	}
	for _, line := range strings.Split(body, "\n") {
		if h := headingRe.FindStringSubmatch(line); h != nil {
			flush()
			cur = Section{Heading: headingTitle(h[2]), Level: len(h[1])}
			text = nil
			continue
		}
		text = append(text, line)
	}
	flush()
	return out
}

// CriticFlags counts list items under each critic subsection. A subsection
// that only says "No changes." counts zero.
func CriticFlags(critic string) map[string]int {
	out := make(map[string]int)
	sub := "Critic Notes"
	for _, line := range strings.Split(critic, "\n") {
		if h := headingRe.FindStringSubmatch(line); h != nil {
			sub = stripInline(h[2])
			if _, ok := out[sub]; !ok {
				out[sub] = 0
			}
			continue
		}
		if listItemRe.MatchString(line) {
			out[sub]++
		}
	}
	return out
}

var counterHeadRe = regexp.MustCompile(`(?i)counter|contradict|disconfirm|disput|critic|skeptic|null result|limitation|debunk|rebut|objection|contested|unverified|unsubstantiated|not (?:been )?(?:verified|substantiated|established)|weak(?:ness|er)? evidence|evidence against|alternative explanation`)

// CounterEvidence counts list items and paragraphs under body headings that
// announce counter-evidence, criticism, or limitations. It is a heading
// heuristic: counter-evidence woven into other sections is not counted.
func CounterEvidence(sections []Section) (items int, headings []string) {
	for _, s := range sections {
		if !counterHeadRe.MatchString(s.Heading) {
			continue
		}
		n := countBlocks(s.Text)
		if n == 0 {
			continue
		}
		items += n
		headings = append(headings, s.Heading)
	}
	return items, headings
}

// countBlocks counts list items plus non-list paragraphs.
func countBlocks(text string) int {
	n := 0
	inPara := false
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case t == "" || t == "---":
			inPara = false
		case listItemRe.MatchString(line):
			n++
			inPara = false
		case strings.HasPrefix(t, "|"):
			inPara = false
		case !inPara:
			n++
			inPara = true
		}
	}
	return n
}

var inlineMarkRe = regexp.MustCompile(`[*_` + "`" + `]+`)

// headingTitle strips emphasis and a leading section number ("7.", "VII.",
// "2.3") so "## 7. References" matches like "## References".
func headingTitle(s string) string {
	return headNumRe.ReplaceAllString(stripInline(s), "")
}

// stripInline removes emphasis and code markers.
func stripInline(s string) string {
	return strings.TrimSpace(inlineMarkRe.ReplaceAllString(s, ""))
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

var counterSentenceRe = regexp.MustCompile(`(?i)\b(?:no (?:credible |documentary |direct |hard |public |known |independent )?evidence|not (?:been )?(?:documented|verified|substantiated|established|proven|supported|replicated)|unverified|unsubstantiated|unproven|disputed|contradict\w*|counter-?evidence|counter-?argument\w*|disconfirm\w*|failed to replicate|did not replicate|null results?|debunk\w*|critics (?:argue|note|say|point|contend|counter)|overstate[sd]?|no (?:documentary )?record|remains? (?:unclear|speculative|unconfirmed|unproven)|cannot be (?:shown|confirmed|verified|established)|lacks? (?:of )?(?:evidence|documentation)|lack of (?:evidence|documentation))\b`)

// CounterSentences counts body sentences that carry disconfirming or
// limiting language ("no evidence", "not documented", "failed to
// replicate", "disputed"). Unlike CounterEvidence it does not depend on
// section headings, so counter-evidence woven into other sections counts.
func CounterSentences(body string) (n int, examples []string) {
	for _, block := range proseBlocks(body) {
		for _, s := range Sentences(block) {
			if counterSentenceRe.MatchString(s) {
				n++
				examples = append(examples, stripInline(s))
			}
		}
	}
	return n, examples
}
