package quote

import "testing"

func TestLocate(t *testing.T) {
	text := "The fuller name, Okapi BM25, includes the name of the first system to use it, which was the Okapi " +
		"information retrieval system, implemented at London’s City University[1] in the 1980s and 1990s. " +
		"BM25F[5][2] (or the BM25 model with Extension to Multiple Weighted Fields[6]) is a modification of BM25. " +
		"Ünïcode café prices rose 12–16% in 2024."
	tests := []struct {
		name, quote, want string
		ok                bool
	}{
		{"exact", "the first system to use it", "the first system to use it", true},
		{"case and spacing", "OKAPI   bm25,  includes", "Okapi BM25, includes", true},
		{"dropped footnote", "London's City University in the 1980s", "London’s City University[1] in the 1980s", true},
		{"ellipsis", "BM25F ... is a modification of BM25", "BM25F[5][2] (or the BM25 model with Extension to Multiple Weighted Fields[6]) is a modification of BM25", true},
		{"bracketed ellipsis", "The fuller name, […] includes the name", "The fuller name, Okapi BM25, includes the name", true},
		{"unicode offsets", "café prices rose 12-16%", "café prices rose 12–16", true},
		{"inserted word", "in the 1980s and the 1990s", "", false},
		{"fragments out of order", "is a modification ... BM25F", "", false},
		{"partial word", "kapi BM25", "", false},
		{"only punctuation", "... — !", "", false},
		{"empty", "", "", false},
	}
	runes := []rune(text)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			start, end, ok := Locate(tc.quote, text)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if !ok {
				return
			}
			if got := string(runes[start:end]); got != tc.want {
				t.Errorf("span = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLocate_LaterOccurrenceCompletesTheQuote(t *testing.T) {
	text := "rates rose sharply. Later, rates rose in March and fell in April."
	start, end, ok := Locate("rates rose ... fell in April", text)
	// The first "rates rose" starts the span: the rest follows it within
	// MaxSkip.
	if !ok || string([]rune(text)[start:end]) != "rates rose sharply. Later, rates rose in March and fell in April" {
		t.Errorf("span = %q, %v", string([]rune(text)[start:end]), ok)
	}
	far := "rates rose. " + longGap() + " Then rates rose and fell in April."
	start, end, ok = Locate("rates rose ... fell in April", far)
	if !ok || string([]rune(far)[start:end]) != "rates rose and fell in April" {
		t.Errorf("a later first fragment: span = %q, %v", string([]rune(far)[start:end]), ok)
	}
	if _, _, ok := Locate("rates rose ... fell in April", "rates rose. "+longGap()+" fell in April"); ok {
		t.Error("an ellipsis skipping more than MaxSkip still matched")
	}
}

func longGap() string {
	b := make([]byte, 0, MaxSkip*2)
	for len(b) < MaxSkip*2 {
		b = append(b, "words "...)
	}
	return string(b)
}

func TestFound(t *testing.T) {
	if !Found("system to use it", "the first system to use it, which") || Found("system to use them", "the first system to use it") {
		t.Error("Found disagrees with Locate")
	}
}
