package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/marklubin/researchguy/internal/graph"
)

func TestSentences(t *testing.T) {
	text := `The U.S. Senate formed the committee in 1975. J. Edgar Hoover ran the FBI, e.g. during COINTELPRO. "It was real," Church said. Was it legal? Most scholars say no! Final line without stop`
	want := []string{
		"The U.S. Senate formed the committee in 1975.",
		"J. Edgar Hoover ran the FBI, e.g. during COINTELPRO.",
		`"It was real," Church said.`,
		"Was it legal?",
		"Most scholars say no!",
		"Final line without stop",
	}
	if got := Sentences(text); !reflect.DeepEqual(got, want) {
		t.Fatalf("Sentences:\n got %q\nwant %q", got, want)
	}
	if got := Sentences("  "); len(got) != 0 {
		t.Errorf("blank: %q", got)
	}
	if got := Sentences("Version 2.5 shipped. It worked."); len(got) != 2 {
		t.Errorf("decimal point must not split: %q", got)
	}
}

func TestProseBlocksSkipsNonProse(t *testing.T) {
	body := "# Title\n\nFirst paragraph\ncontinues here.\n\n| a | b |\n|---|---|\n\n```\ncode line\n```\n\n- item one\n- item two\n\n> quoted line\n"
	want := []string{"First paragraph continues here.", "item one", "item two", "quoted line"}
	if got := proseBlocks(body); !reflect.DeepEqual(got, want) {
		t.Fatalf("proseBlocks = %q, want %q", got, want)
	}
}

func TestExtractClaimsBookworm(t *testing.T) {
	d := ParseDoc(bookwormSample)
	claims := ExtractClaims("A", "t1", d)
	if len(claims) != 1 {
		t.Fatalf("want 1 attributed claim of 60+ chars, got %d: %+v", len(claims), claims)
	}
	c := claims[0]
	if !strings.HasPrefix(c.Text, "Lifton's Thought Reform (1961)") {
		t.Errorf("emphasis should be stripped: %q", c.Text)
	}
	if len(c.Cites) == 0 || !strings.Contains(c.Cites[0], "https://example.com/lifton") {
		t.Errorf("paren-year claim should resolve to the Lifton source: %q", c.Cites)
	}
}

func TestExtractClaimsHybridRefs(t *testing.T) {
	md := strings.Replace(hybridSample, "COINTELPRO ran from 1956 to 1971 [1].",
		"COINTELPRO ran from 1956 to 1971 under FBI direction across many field offices [1].", 1)
	md = strings.Replace(md, "The Church Committee documented it [2, 3].",
		"The Church Committee documented the program in exhaustive detail in its reports [2, 3]. Nobody cited this unattributed sentence at all, which is long enough.", 1)
	claims := ExtractClaims("B", "t1", ParseDoc(md))
	if len(claims) != 2 {
		t.Fatalf("want 2 claims (unattributed skipped), got %d: %+v", len(claims), claims)
	}
	if len(claims[0].Cites) != 1 || !strings.HasPrefix(claims[0].Cites[0], "[1] Church Committee Final Report") {
		t.Errorf("[1] should resolve to ref 1: %q", claims[0].Cites)
	}
	if len(claims[1].Cites) != 2 || !strings.HasPrefix(claims[1].Cites[1], "[3] Weiner") {
		t.Errorf("[2, 3] should resolve to refs 2 and 3: %q", claims[1].Cites)
	}
}

func TestExtractClaimsInlineLink(t *testing.T) {
	d := ParseDoc("The RAND report described the firehose of falsehood model in 2016 in detail, see [RAND](https://rand.example/rr198).")
	claims := ExtractClaims("C", "t1", d)
	if len(claims) != 1 || !reflect.DeepEqual(claims[0].Cites, []string{"https://rand.example/rr198"}) {
		t.Fatalf("inline link claim: %+v", claims)
	}
	if strings.Contains(claims[0].Text, "](") {
		t.Errorf("markdown link syntax should be flattened: %q", claims[0].Text)
	}
}

func TestExtractClaimsUnresolvedAttribution(t *testing.T) {
	d := ParseDoc("An obscure pamphlet (Nobody, 1999) claimed the program extended well beyond its charter.")
	claims := ExtractClaims("A", "t2", d)
	if len(claims) != 1 || len(claims[0].Cites) != 0 {
		t.Fatalf("attributed but unmatched claim should have no cites: %+v", claims)
	}
}

func TestMatchSourcesPrefersNameAndYear(t *testing.T) {
	d := Doc{}
	d.Sources = append(d.Sources,
		srcEntry("Legacy of Ashes", "Tim Weiner", "2007", "https://w.example"),
		srcEntry("Merchants of Doubt", "Naomi Oreskes", "2010", "https://o.example"),
	)
	got := matchSources("Oreskes and Conway traced the tobacco playbook (2010).", d, 2)
	if len(got) != 1 || !strings.Contains(got[0], "o.example") {
		t.Fatalf("matchSources = %q", got)
	}
	if got := matchSources("Nothing relevant here (1999).", d, 2); len(got) != 0 {
		t.Errorf("no overlap should match nothing: %q", got)
	}
}

func TestExpandCiteNums(t *testing.T) {
	cases := map[string][]int{
		"1":     {1},
		"2, 3":  {2, 3},
		"4-6":   {4, 5, 6},
		"1,3–4": {1, 3, 4},
		"9-2":   nil,
		"x":     nil,
	}
	for in, want := range cases {
		if got := expandCiteNums(in); !reflect.DeepEqual(got, want) {
			t.Errorf("expandCiteNums(%q) = %v, want %v", in, got, want)
		}
	}
}

func makeClaims(arm, topic string, n int) []Claim {
	var out []Claim
	for i := 0; i < n; i++ {
		out = append(out, Claim{Arm: arm, Topic: topic, Text: arm + topic + string(rune('a'+i))})
	}
	return out
}

func TestSampleClaimsSpreadsAcrossTopics(t *testing.T) {
	var all []Claim
	all = append(all, makeClaims("A", "t1", 10)...)
	all = append(all, makeClaims("A", "t2", 10)...)
	all = append(all, makeClaims("A", "t3", 2)...)
	all = append(all, makeClaims("B", "t1", 3)...)

	got := SampleClaims(append([]Claim(nil), all...), 7, 42)
	perTopic := map[string]int{}
	perArm := map[string]int{}
	for _, c := range got {
		perArm[c.Arm]++
		if c.Arm == "A" {
			perTopic[c.Topic]++
		}
	}
	if perArm["A"] != 7 || perArm["B"] != 3 {
		t.Errorf("per arm = %v, want A 7 and B all 3", perArm)
	}
	if perTopic["t3"] != 2 || perTopic["t1"] < 2 || perTopic["t2"] < 2 {
		t.Errorf("A should be spread round-robin across topics: %v", perTopic)
	}

	again := SampleClaims(append([]Claim(nil), all...), 7, 42)
	if !reflect.DeepEqual(got, again) {
		t.Error("same seed should give the same sample")
	}
}

func TestBlindAssignsIDsDeterministically(t *testing.T) {
	claims := append(makeClaims("A", "t1", 3), makeClaims("B", "t1", 3)...)
	a := Blind(claims, 7)
	b := Blind(claims, 7)
	if !reflect.DeepEqual(a, b) {
		t.Error("Blind should be deterministic for a seed")
	}
	if a[0].ID != "C01" || a[5].ID != "C06" {
		t.Errorf("IDs: %s..%s", a[0].ID, a[5].ID)
	}
	if claims[0].ID != "" {
		t.Error("Blind must not mutate its input")
	}
}

func TestReviewSheetRoundTripHidesArms(t *testing.T) {
	claims := Blind([]Claim{
		{Arm: "A", Topic: "t1", Text: "Claim one.", Cites: []string{"https://a.example"}},
		{Arm: "B", Topic: "t2", Text: "Claim two."},
		{Arm: "C", Topic: "t1", Text: "Claim three."},
	}, 1)
	var buf bytes.Buffer
	if err := WriteReviewSheet(&buf, claims); err != nil {
		t.Fatal(err)
	}
	sheet := buf.String()
	for _, leak := range []string{"arm:", "Arm:", "arm A", "arm B", "(A)", "\"A\""} {
		if strings.Contains(sheet, leak) {
			t.Errorf("sheet leaks arm via %q", leak)
		}
	}
	if !strings.Contains(sheet, "(no matching source entry)") {
		t.Error("uncited claims should say so")
	}

	filled := strings.Replace(sheet, "\nVerdict:\n", "\nVerdict: supported\n", 1)
	filled = strings.Replace(filled, "\nVerdict:\n", "\nVerdict: Unsupported\n", 1)
	filled = strings.Replace(filled, "\nVerdict:\n", "\nVerdict: maybe\n", 1)
	v, err := Verdicts(strings.NewReader(filled))
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 2 || v[claims[0].ID] != "supported" || v[claims[1].ID] != "unsupported" {
		t.Errorf("verdicts = %v (invalid values ignored, case folded)", v)
	}
}

func srcEntry(title, author, date, url string) graph.SourceEntry {
	return graph.SourceEntry{Title: title, Author: author, Date: date, URL: url}
}
