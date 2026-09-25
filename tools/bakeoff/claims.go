package main

import (
	"bufio"
	"fmt"
	"hash/fnv"
	"io"
	"math/rand/v2"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Claim is one attributed sentence from a research output.
type Claim struct {
	ID    string   `json:"id,omitempty"` // blinded review ID, set when sampled
	Arm   string   `json:"arm"`
	Topic string   `json:"topic"`
	Text  string   `json:"text"`
	Cites []string `json:"cites"` // resolved citations: URLs or source entries
}

var (
	bracketCiteRe = regexp.MustCompile(`\[(\d+(?:\s*[,\x{2013}-]\s*\d+)*)\]`)
	mdLinkRe      = regexp.MustCompile(`\[([^\]]+)\]\((https?://[^)\s]+)\)`)
	parenYearRe   = regexp.MustCompile(`\([^()]*\b(1[5-9]\d\d|20[0-2]\d)\b[^()]*\)`)
	yearRe        = regexp.MustCompile(`\b(1[5-9]\d\d|20[0-2]\d)\b`)
	fenceRe       = regexp.MustCompile("(?s)```.*?```")
	wordRe        = regexp.MustCompile(`[A-Za-z][A-Za-z'\x{2019}-]+`)
)

// abbreviations that end with a period but do not end a sentence.
var abbreviations = map[string]bool{
	"u.s": true, "u.k": true, "e.g": true, "i.e": true, "dr": true, "mr": true,
	"mrs": true, "ms": true, "prof": true, "vs": true, "al": true, "st": true,
	"jr": true, "sr": true, "no": true, "vol": true, "pp": true, "p": true,
	"ed": true, "eds": true, "inc": true, "co": true, "corp": true, "fig": true,
	"cf": true, "ca": true, "approx": true, "gen": true, "sen": true, "rep": true,
	"col": true, "lt": true, "sgt": true, "gov": true, "rev": true, "u.n": true,
}

// Sentences splits prose into sentences. It breaks after . ! ? when the next
// word starts with an uppercase letter, quote, or bracket, unless the word
// before the period is a known abbreviation or a single initial.
func Sentences(text string) []string {
	var out []string
	runes := []rune(text)
	start := 0
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		if c != '.' && c != '!' && c != '?' {
			continue
		}
		// Absorb closing quotes, brackets, and emphasis after the stop.
		j := i + 1
		for j < len(runes) && strings.ContainsRune(`"')]*_`+"”’", runes[j]) {
			j++
		}
		if j >= len(runes) || !unicode.IsSpace(runes[j]) {
			continue
		}
		k := j
		for k < len(runes) && unicode.IsSpace(runes[k]) {
			k++
		}
		if k < len(runes) && !startsSentence(runes[k]) {
			continue
		}
		if c == '.' && isAbbreviation(runes[start:i]) {
			continue
		}
		if s := strings.TrimSpace(string(runes[start:j])); s != "" {
			out = append(out, s)
		}
		start = k
		i = k - 1
	}
	if s := strings.TrimSpace(string(runes[start:])); s != "" {
		out = append(out, s)
	}
	return out
}

func startsSentence(r rune) bool {
	return unicode.IsUpper(r) || unicode.IsDigit(r) || strings.ContainsRune(`"'([*_`+"“‘", r)
}

func isAbbreviation(before []rune) bool {
	i := len(before)
	for i > 0 && !unicode.IsSpace(before[i-1]) && before[i-1] != '(' {
		i--
	}
	word := strings.ToLower(strings.Trim(string(before[i:]), `"'*_(`))
	if len([]rune(word)) == 1 && unicode.IsLetter([]rune(word)[0]) {
		return true // an initial, as in "J. Edgar Hoover"
	}
	return abbreviations[word]
}

// proseBlocks returns the paragraphs and list items of body, skipping
// headings, tables, code fences, and horizontal rules.
func proseBlocks(body string) []string {
	body = fenceRe.ReplaceAllString(body, "")
	var blocks []string
	var cur []string
	flush := func() {
		if len(cur) > 0 {
			blocks = append(blocks, strings.Join(cur, " "))
			cur = nil
		}
	}
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case t == "" || t == "---" || headingRe.MatchString(t) || strings.HasPrefix(t, "|"):
			flush()
		case listItemRe.MatchString(line):
			flush()
			cur = append(cur, strings.TrimSpace(listItemRe.ReplaceAllString(line, "")))
		default:
			cur = append(cur, strings.TrimPrefix(t, "> "))
		}
	}
	flush()
	return blocks
}

// minClaimLen drops fragments too short to carry a checkable claim.
const minClaimLen = 60

// ExtractClaims returns the attributed sentences of a document with their
// citations resolved against its sources or references list. A sentence is
// attributed if it carries a URL, a markdown link, a [n] reference, or a
// parenthetical with a year, such as "(1961)" or "(Lifton, 1961)".
func ExtractClaims(arm, topic string, d Doc) []Claim {
	var out []Claim
	for _, block := range proseBlocks(d.Body) {
		for _, s := range Sentences(block) {
			plain := strings.TrimSpace(mdLinkRe.ReplaceAllString(s, "$1"))
			plain = stripInline(plain)
			if len(plain) < minClaimLen {
				continue
			}
			if looksLikeCitation(plain) {
				continue
			}
			cites, attributed := resolveCites(s, d)
			if !attributed {
				continue
			}
			out = append(out, Claim{Arm: arm, Topic: topic, Text: plain, Cites: cites})
		}
	}
	return out
}

func resolveCites(s string, d Doc) (cites []string, attributed bool) {
	seen := make(map[string]bool)
	add := func(c string) {
		c = strings.TrimSpace(c)
		if c != "" && !seen[c] {
			seen[c] = true
			cites = append(cites, c)
		}
	}
	for _, u := range ExtractURLs(s) {
		attributed = true
		add(u)
	}
	for _, m := range bracketCiteRe.FindAllStringSubmatch(s, -1) {
		for _, n := range expandCiteNums(m[1]) {
			for _, r := range d.Refs {
				if r.Num == n {
					attributed = true
					add(fmt.Sprintf("[%d] %s", n, r.Text))
				}
			}
		}
	}
	if parenYearRe.MatchString(s) {
		attributed = true
	}
	if !attributed {
		return nil, false
	}
	if len(cites) == 0 {
		for _, c := range matchSources(s, d, 2) {
			add(c)
		}
	}
	return cites, true
}

var (
	volPagesRe  = regexp.MustCompile(`\b\d+\s*(?:,\s*)?(?:no\.\s*\d+\s*)?\((?:1[5-9]|20)\d\d\)\s*:\s*\d+|:\s*\d+\s*[-\x{2013}]\s*\d+\.?$`)
	publisherRe = regexp.MustCompile(`\([^()]*(?:Press|Publishing|Publishers|Books|Bloomsbury|Norton|Random House|Knopf|Penguin|Simon & Schuster|HarperCollins|Wiley|Routledge)[^()]*\)\.?$`)
	sourceTagRe = regexp.MustCompile(`(?i)^(?:sources?|source text|summary via|see also|via)\b`)
)

// looksLikeCitation reports whether a sentence is a bibliography entry or a
// source label rather than a claim: volume/issue/pages, a trailing
// (Publisher, year), or a "Source:"-style lead.
func looksLikeCitation(s string) bool {
	return volPagesRe.MatchString(s) || publisherRe.MatchString(s) || sourceTagRe.MatchString(s)
}

// expandCiteNums turns "1, 3-5" into [1 3 4 5].
func expandCiteNums(s string) []int {
	var out []int
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if lo, hi, ok := strings.Cut(strings.ReplaceAll(part, "–", "-"), "-"); ok {
			a, b := atoi(strings.TrimSpace(lo)), atoi(strings.TrimSpace(hi))
			if a > 0 && b >= a && b-a < 20 {
				for n := a; n <= b; n++ {
					out = append(out, n)
				}
			}
			continue
		}
		if n := atoi(part); n > 0 {
			out = append(out, n)
		}
	}
	return out
}

// stopwords are capitalized words that say nothing about which source a
// sentence cites.
var stopwords = map[string]bool{
	"the": true, "a": true, "an": true, "and": true, "of": true, "in": true,
	"on": true, "for": true, "to": true, "by": true, "with": true, "from": true,
	"this": true, "that": true, "these": true, "his": true, "her": true,
	"their": true, "its": true, "as": true, "at": true, "or": true, "but": true,
	"is": true, "was": true, "were": true, "are": true, "be": true, "it": true,
	"he": true, "she": true, "they": true, "we": true, "not": true, "who": true,
	"what": true, "which": true, "when": true, "how": true, "why": true,
}

// matchSources ranks a document's sources and references by overlap with
// the sentence's capitalized words (names, titles), with a bonus when a
// year in the sentence appears in the source.
func matchSources(s string, d Doc, limit int) []string {
	type cand struct {
		text  string
		score int
	}
	words := make(map[string]bool)
	for _, w := range wordRe.FindAllString(s, -1) {
		lw := strings.ToLower(w)
		if unicode.IsUpper([]rune(w)[0]) && len(w) > 2 && !stopwords[lw] {
			words[lw] = true
		}
	}
	years := yearRe.FindAllString(s, -1)

	var cands []cand
	score := func(text string) int {
		n := 0
		used := make(map[string]bool)
		lt := strings.ToLower(text)
		for _, w := range wordRe.FindAllString(lt, -1) {
			if words[w] && !used[w] {
				n += 2
				used[w] = true
			}
		}
		for _, y := range years {
			if strings.Contains(lt, y) {
				n++
			}
		}
		return n
	}
	for _, src := range d.Sources {
		text := strings.TrimSpace(strings.Join(nonEmpty(src.Title, src.Author, src.Date), ", "))
		if src.URL != "" {
			text += " <" + src.URL + ">"
		}
		if sc := score(src.Title + " " + src.Author + " " + src.Date); sc >= 2 {
			cands = append(cands, cand{text, sc})
		}
	}
	for _, r := range d.Refs {
		if sc := score(r.Text); sc >= 2 {
			cands = append(cands, cand{r.Text, sc})
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].score > cands[j].score })
	var out []string
	for i := 0; i < len(cands) && i < limit; i++ {
		out = append(out, cands[i].text)
	}
	return out
}

func nonEmpty(ss ...string) []string {
	var out []string
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
}

// SampleClaims draws up to n claims per arm, spread evenly across topics
// (round-robin over topics in sorted order), deterministically from seed.
func SampleClaims(claims []Claim, n int, seed uint64) []Claim {
	byArm := make(map[string]map[string][]Claim)
	for _, c := range claims {
		if byArm[c.Arm] == nil {
			byArm[c.Arm] = make(map[string][]Claim)
		}
		byArm[c.Arm][c.Topic] = append(byArm[c.Arm][c.Topic], c)
	}
	arms := sortedKeys(byArm)
	var out []Claim
	for _, arm := range arms {
		h := fnv.New64a()
		h.Write([]byte(arm))
		rng := rand.New(rand.NewPCG(seed, h.Sum64()))
		topics := sortedKeys(byArm[arm])
		for _, t := range topics {
			pool := byArm[arm][t]
			rng.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
		}
		taken := 0
		for round := 0; taken < n; round++ {
			progressed := false
			for _, t := range topics {
				if taken >= n {
					break
				}
				if round < len(byArm[arm][t]) {
					out = append(out, byArm[arm][t][round])
					taken++
					progressed = true
				}
			}
			if !progressed {
				break
			}
		}
	}
	return out
}

// Blind shuffles sampled claims across arms and assigns review IDs.
func Blind(claims []Claim, seed uint64) []Claim {
	out := append([]Claim(nil), claims...)
	rng := rand.New(rand.NewPCG(seed, 0x5eed))
	rng.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	for i := range out {
		out[i].ID = fmt.Sprintf("C%02d", i+1)
	}
	return out
}

// WriteReviewSheet writes blinded claims for hand review. Arms are left out;
// the key maps IDs back.
func WriteReviewSheet(w io.Writer, claims []Claim) error {
	bw := bufio.NewWriter(w)
	fmt.Fprintln(bw, "# Bake-off claim review")
	fmt.Fprintln(bw)
	fmt.Fprintln(bw, "For each claim, open the cited source and set `Verdict:` to one of")
	fmt.Fprintln(bw, "`supported`, `partial`, `unsupported`, or `unverifiable` (source")
	fmt.Fprintln(bw, "unreachable, paywalled, or no source given). Which arm wrote each claim is hidden; run")
	fmt.Fprintln(bw, "`go run ./tools/bakeoff tally` afterwards.")
	for _, c := range claims {
		fmt.Fprintln(bw)
		fmt.Fprintf(bw, "## %s (topic %s)\n\n", c.ID, c.Topic)
		fmt.Fprintf(bw, "> %s\n\n", c.Text)
		if len(c.Cites) == 0 {
			fmt.Fprintln(bw, "Cited: (no matching source entry)")
		} else {
			fmt.Fprintln(bw, "Cited:")
			for _, cite := range c.Cites {
				fmt.Fprintf(bw, "- %s\n", cite)
			}
		}
		fmt.Fprintln(bw)
		fmt.Fprintln(bw, "Verdict:")
		fmt.Fprintln(bw, "Note:")
	}
	return bw.Flush()
}

var (
	reviewIDRe = regexp.MustCompile(`^##\s+(C\d+)\b`)
	verdictRe  = regexp.MustCompile(`(?i)^verdict:\s*(\w*)`)
)

// Verdicts reads a filled-in review sheet and returns ID -> verdict for
// every claim with a recognized verdict.
func Verdicts(r io.Reader) (map[string]string, error) {
	valid := map[string]bool{"supported": true, "partial": true, "unsupported": true, "unverifiable": true}
	out := make(map[string]string)
	id := ""
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if m := reviewIDRe.FindStringSubmatch(line); m != nil {
			id = m[1]
			continue
		}
		if m := verdictRe.FindStringSubmatch(line); m != nil && id != "" {
			if v := strings.ToLower(m[1]); valid[v] {
				out[id] = v
			}
		}
	}
	return out, sc.Err()
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
