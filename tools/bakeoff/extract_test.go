package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestExtractURLs(t *testing.T) {
	text := `See [the report](https://example.com/a/b). Also https://en.wikipedia.org/wiki/Foo_(bar), and
(https://example.org/paper.pdf) plus "url": "https://news.site/x?id=1", and https://example.com/end.
Wrapped: **https://bold.example/path**`
	want := []string{
		"https://example.com/a/b",
		"https://en.wikipedia.org/wiki/Foo_(bar)",
		"https://example.org/paper.pdf",
		"https://news.site/x?id=1",
		"https://example.com/end",
		"https://bold.example/path",
	}
	if got := ExtractURLs(text); !reflect.DeepEqual(got, want) {
		t.Fatalf("ExtractURLs:\n got %q\nwant %q", got, want)
	}
}

func TestExtractURLsEmptyAndBare(t *testing.T) {
	if got := ExtractURLs("no links here, just http:// and text"); len(got) != 0 {
		t.Fatalf("want none, got %q", got)
	}
}

func TestUniqueURLsNormalizes(t *testing.T) {
	got := UniqueURLs([]string{
		"https://www.example.com/a/?utm_source=x",
		"http://example.com/a",
		"https://example.com/b#frag",
		"not a url at all\x7f",
	})
	if len(got) != 2 {
		t.Fatalf("want 2 unique, got %d: %v", len(got), got)
	}
	if got["example.com/a"] != "https://www.example.com/a/?utm_source=x" {
		t.Errorf("first raw URL should be kept, got %q", got["example.com/a"])
	}
}

const bookwormSample = "# Findings\n\n## I. Background\n\nLifton's *Thought Reform* (1961) described milieu control in detail across eight criteria.\n\n## II. Critics and Counter-Evidence\n\n- The APA declined to endorse the brainwashing model (1987).\n- Some scholars dispute the framing.\n\nA paragraph of caveats.\n\n## Challenges\n\nStill open.\n\n```json\nSOURCES_START\n[\n  {\"title\": \"Thought Reform and the Psychology of Totalism\", \"author\": \"Robert Jay Lifton\", \"url\": \"https://example.com/lifton\", \"date\": \"1961\"},\n  {\"title\": \"No URL book\", \"author\": \"Someone\", \"url\": \"\", \"date\": \"2001\"}\n]\nSOURCES_END\n```\n"

func TestParseDocBookwormSources(t *testing.T) {
	d := ParseDoc(bookwormSample)
	if len(d.Sources) != 2 || d.Sources[0].Author != "Robert Jay Lifton" {
		t.Fatalf("sources: %+v", d.Sources)
	}
	if strings.Contains(d.Body, "SOURCES_START") || strings.Contains(d.Body, "example.com/lifton") {
		t.Errorf("body should not include the sources block:\n%s", d.Body)
	}
	if len(d.Refs) != 0 || d.Critic != "" {
		t.Errorf("unexpected refs %v or critic %q", d.Refs, d.Critic)
	}
}

const hybridSample = `# Report

## Executive Summary

COINTELPRO ran from 1956 to 1971 [1]. The Church Committee documented it [2, 3].

### Details

More text.

## 7. References and Sources

1. Church Committee Final Report, Book III. https://example.gov/church
   (continued line) https://example.gov/church-alt
2. Senate record. https://example.gov/senate
[3] Weiner, Legacy of Ashes (2007)

### Primary documents

- FBI memo, 1968. https://vault.example/memo

## Afterword

Back to body.

---

## Critic Notes

### Groundedness Review

- Removed claim X: unsupported.
- Softened claim Y.

### Narrative vs. Evidence

No changes.
`

func TestParseDocHybridRefsAndCritic(t *testing.T) {
	d := ParseDoc(hybridSample)
	if len(d.Refs) != 4 {
		t.Fatalf("want 4 refs, got %d: %+v", len(d.Refs), d.Refs)
	}
	if d.Refs[0].Num != 1 || len(d.Refs[0].URLs) != 2 {
		t.Errorf("ref 1 should carry its continuation URL: %+v", d.Refs[0])
	}
	if d.Refs[2].Num != 3 || !strings.Contains(d.Refs[2].Text, "Legacy of Ashes") {
		t.Errorf("ref [3]: %+v", d.Refs[2])
	}
	if d.Refs[3].Num != 0 || !strings.Contains(d.Refs[3].Text, "FBI memo") {
		t.Errorf("subheading entries stay in refs: %+v", d.Refs[3])
	}
	if !strings.Contains(d.Body, "Back to body.") || strings.Contains(d.Body, "Church Committee Final Report") {
		t.Errorf("body should resume after refs and exclude them:\n%s", d.Body)
	}
	flags := CriticFlags(d.Critic)
	want := map[string]int{"Groundedness Review": 2, "Narrative vs. Evidence": 0}
	if !reflect.DeepEqual(flags, want) {
		t.Errorf("CriticFlags = %v, want %v", flags, want)
	}
}

func TestCounterEvidence(t *testing.T) {
	d := ParseDoc(bookwormSample)
	items, heads := CounterEvidence(d.Sections)
	if items != 3 {
		t.Errorf("want 2 bullets + 1 paragraph = 3 items, got %d", items)
	}
	if !reflect.DeepEqual(heads, []string{"Critics and Counter-Evidence"}) {
		t.Errorf("headings = %q (Challenges must not count)", heads)
	}
	if n, _ := CounterEvidence(nil); n != 0 {
		t.Errorf("nil sections: %d", n)
	}
}

func TestHeadingTitle(t *testing.T) {
	cases := map[string]string{
		"7. References":      "References",
		"VII. **Sources**":   "Sources",
		"2.3 Replication":    "Replication",
		"Critic Notes":       "Critic Notes",
		"1960s Surveillance": "1960s Surveillance",
	}
	for in, want := range cases {
		if got := headingTitle(in); got != want {
			t.Errorf("headingTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRefHeadingsNotFooledByBodyHeadings(t *testing.T) {
	for _, h := range []string{"Sources of Funding", "Funding Sources", "Primary evidence"} {
		if refHeadRe.MatchString(h) {
			t.Errorf("%q should not be a references heading", h)
		}
	}
	for _, h := range []string{"References", "Sources", "Key Sources", "References and Sources", "Bibliography", "Sources Consulted", "References (selected)"} {
		if !refHeadRe.MatchString(h) {
			t.Errorf("%q should be a references heading", h)
		}
	}
}

func TestCounterSentences(t *testing.T) {
	body := "## Findings\n\nThe memo exists and was sent in 1967. There is no documentary evidence it was implemented. Critics argue the program was smaller than claimed.\n\n- The effect failed to replicate in 2015.\n- Attendance was high.\n\nThe story remains unconfirmed by any agency. Nothing else here is limiting."
	n, ex := CounterSentences(body)
	if n != 4 {
		t.Fatalf("want 4 counter sentences, got %d: %q", n, ex)
	}
	if n, _ := CounterSentences("All claims confirmed by the record. The documents were released."); n != 0 {
		t.Errorf("no limiting language should count 0, got %d", n)
	}
}

func TestParseDocStripsDiveHeader(t *testing.T) {
	md := "# ## 1.2 Brief title\n\n**Sources to Find:**\n- Weiner's *Legacy of Ashes* (2007) as the brief lists it\n\n*Generated: 2026-09-25 13:06 | Backend: hybrid*\n\n---\n\n# Report\n\nThe report says COINTELPRO ran for fifteen years (1956-1971) across the country.\n"
	d := ParseDoc(md)
	if strings.Contains(d.Body, "Legacy of Ashes") || strings.Contains(d.Body, "Generated:") {
		t.Fatalf("brief header should be stripped:\n%s", d.Body)
	}
	if !strings.HasPrefix(d.Body, "# Report") {
		t.Errorf("body should start at the report: %q", d.Body[:20])
	}
	if got := stripRunHeader("no header here\n*Generated: x*"); got != "no header here\n*Generated: x*" {
		t.Errorf("no Backend field means no header: %q", got)
	}
}

func TestDocURLsSkipsCriticNotes(t *testing.T) {
	md := "Body cites https://body.example/a.\n\n## References\n\n1. Ref https://ref.example/b\n\n## Critic Notes\n\n- Removed claim citing https://critic.example/c\n"
	got := DocURLs(ParseDoc(md))
	want := []string{"https://body.example/a", "https://ref.example/b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DocURLs = %q, want %q", got, want)
	}
	if got := DocURLs(ParseDoc(bookwormSample)); !reflect.DeepEqual(got, []string{"https://example.com/lifton"}) {
		t.Errorf("sources entries with URLs only: %q", got)
	}
}
