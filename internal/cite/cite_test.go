package cite

import (
	"testing"
)

func TestParse_MarkersGroupsAndOffsets(t *testing.T) {
	report := "Intro [E3]. Felines nap \"for most of every single day\" [P:12; S:7][E4] and more.\n" +
		"Ünïcode “the market fell sharply on Monday”, the paper says [P:99]. Not a marker [X:1] or [P:abc]."
	got := Parse(report)
	want := []Marker{
		{Ord: 1, Marker: "E3", Kind: KindCapture, ID: "3", Offset: 6, Group: 1},
		{Ord: 2, Marker: "P:12", Kind: KindPassage, ID: "12", Offset: 55, Group: 2, Quote: "for most of every single day"},
		{Ord: 3, Marker: "S:7", Kind: KindSource, ID: "7", Offset: 55, Group: 2, Quote: "for most of every single day"},
		{Ord: 4, Marker: "E4", Kind: KindCapture, ID: "4", Offset: 66, Group: 2, Quote: "for most of every single day"},
		{Ord: 5, Marker: "P:99", Kind: KindPassage, ID: "99", Offset: 141, Group: 3, Quote: "the market fell sharply on Monday"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d markers: %+v", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("marker %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	runes := []rune(report)
	for _, m := range got {
		if runes[m.Offset] != '[' {
			t.Errorf("offset %d of %s is %q, not the bracket", m.Offset, m.Marker, runes[m.Offset])
		}
	}
}

func TestParse_QuoteRules(t *testing.T) {
	tests := []struct {
		name, report, quote string
	}{
		{"short scare quote", `The "settled" science [P:1]`, ""},
		{"too far before", `"one two three four" and then` + " a great deal of further prose that keeps going well past the point where a reader would still tie it to the quote [P:1]", ""},
		{"previous line", "\"one two three four\"\nNext line [P:1]", ""},
		{"consumed by the earlier citation", `"one two three four" [E1] then [P:1]`, ""},
		{"unterminated", `an "open quote with no end [P:1]`, ""},
		{"close attribution", `"one two three four," she wrote [P:1]`, "one two three four,"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ms := Parse(tc.report)
			if len(ms) == 0 {
				t.Fatal("no markers")
			}
			if got := ms[len(ms)-1].Quote; got != tc.quote {
				t.Errorf("quote = %q, want %q", got, tc.quote)
			}
		})
	}
}

func TestParse_None(t *testing.T) {
	if got := Parse("no citations here [1] [E] [P:]"); len(got) != 0 {
		t.Errorf("got %+v", got)
	}
}

func TestFound(t *testing.T) {
	text := "The study found that Cats sleep 12–16 hours a day, mostly at dawn; they don't hunt at noon."
	tests := []struct {
		quote string
		want  bool
	}{
		{"cats sleep 12-16 hours a day", true},
		{"Cats sleep 12–16 hours a day, mostly at dawn", true},
		{"they don’t hunt at noon", true},
		{"Cats sleep ... mostly at dawn", true},
		{"Cats sleep [...] at dawn", true},
		{"mostly at dawn ... Cats sleep", false},
		{"cats sleep 20 hours", false},
		{"ats sleep 12", false},
		{"...", false},
		{"", false},
	}
	for _, tc := range tests {
		if got := Found(tc.quote, text); got != tc.want {
			t.Errorf("Found(%q) = %v, want %v", tc.quote, got, tc.want)
		}
	}
}
