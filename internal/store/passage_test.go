package store

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// checkPassages verifies the invariants every split must keep: offsets
// index the text, Text is that span, ords count up, spans don't overlap,
// and no passage starts or ends in whitespace.
func checkPassages(t *testing.T, text string, ps []Passage) {
	t.Helper()
	runes := []rune(text)
	prev := 0
	for i, p := range ps {
		if p.Ord != i {
			t.Errorf("passage %d has ord %d", i, p.Ord)
		}
		if p.Start < prev || p.End <= p.Start || p.End > len(runes) {
			t.Fatalf("passage %d span [%d,%d) out of order (prev end %d, len %d)", i, p.Start, p.End, prev, len(runes))
		}
		if got := string(runes[p.Start:p.End]); got != p.Text {
			t.Errorf("passage %d text doesn't match its offsets", i)
		}
		if strings.TrimSpace(p.Text) != p.Text {
			t.Errorf("passage %d has surrounding whitespace: %q", i, p.Text)
		}
		prev = p.End
	}
}

func TestPassages_GroupsParagraphsUpToTarget(t *testing.T) {
	para := strings.Repeat("word ", 96) // 479 chars once trimmed
	text := strings.Join([]string{para, para, para, para, para}, "\n\n")
	ps := Passages(text)
	checkPassages(t, text, ps)
	if len(ps) != 2 {
		t.Fatalf("got %d passages, want 2 (three paragraphs, then two)", len(ps))
	}
	for _, p := range ps {
		if n := utf8.RuneCountInString(p.Text); n > PassageTarget {
			t.Errorf("passage of %d chars exceeds the target", n)
		}
	}
	if !strings.Contains(ps[0].Text, "\n\n") {
		t.Error("merged paragraphs lost their blank line")
	}
}

func TestPassages_SplitsLongParagraphAtSentences(t *testing.T) {
	sentence := "This sentence is about forty-five characters. "
	text := strings.TrimSpace(strings.Repeat(sentence, 120)) // ~5.5k chars, one paragraph
	ps := Passages(text)
	checkPassages(t, text, ps)
	if len(ps) < 3 {
		t.Fatalf("got %d passages from a 5.5k paragraph", len(ps))
	}
	for i, p := range ps[:len(ps)-1] {
		if !strings.HasSuffix(p.Text, ".") {
			t.Errorf("passage %d doesn't end at a sentence: ...%q", i, p.Text[len(p.Text)-20:])
		}
		if n := utf8.RuneCountInString(p.Text); n > passageMax {
			t.Errorf("passage %d has %d chars", i, n)
		}
	}
}

func TestPassages_HardCutsTextWithoutSpaces(t *testing.T) {
	text := strings.Repeat("x", 4500)
	ps := Passages(text)
	checkPassages(t, text, ps)
	total := 0
	for _, p := range ps {
		total += len(p.Text)
	}
	if total != 4500 {
		t.Errorf("passages cover %d of 4500 chars", total)
	}
}

func TestPassages_RuneOffsets(t *testing.T) {
	text := "Ünïcödé first paragraph — ok.\n\n   \n日本語の段落。"
	ps := Passages(text)
	checkPassages(t, text, ps)
	if len(ps) != 1 {
		t.Fatalf("got %d passages, want the two small paragraphs merged", len(ps))
	}
	if ps[0].Start != 0 || ps[0].End != utf8.RuneCountInString(text) {
		t.Errorf("span = [%d,%d), want rune offsets [0,%d)", ps[0].Start, ps[0].End, utf8.RuneCountInString(text))
	}
}

func TestPassages_ShortTailJoinsPrevious(t *testing.T) {
	text := strings.Repeat("a", 1400) + "\n\n" + strings.Repeat("b", 400) + "\n\n" + "tail."
	ps := Passages(text)
	checkPassages(t, text, ps)
	if last := ps[len(ps)-1]; strings.TrimSpace(last.Text) == "tail." {
		t.Errorf("short tail left on its own: %d passages", len(ps))
	}
}

func TestPassages_EmptyAndBlank(t *testing.T) {
	for _, text := range []string{"", "   ", "\n\n\n"} {
		if ps := Passages(text); len(ps) != 0 {
			t.Errorf("Passages(%q) = %d passages", text, len(ps))
		}
	}
}

func TestPassages_Deterministic(t *testing.T) {
	text := strings.Repeat("Some text here. More text.\n\n", 300)
	a, b := Passages(text), Passages(text)
	if len(a) != len(b) {
		t.Fatal("different passage counts")
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("passage %d differs", i)
		}
	}
}
