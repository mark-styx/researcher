package critique

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSplitRewriteOutput(t *testing.T) {
	revised, changes := SplitRewriteOutput("answer text\n" + ChangesMarker + "\n- change one")
	if revised != "answer text" || changes != "- change one" {
		t.Errorf("got %q / %q", revised, changes)
	}
	revised, changes = SplitRewriteOutput("just the answer, no marker")
	if revised != "just the answer, no marker" || changes != "" {
		t.Errorf("no marker: %q / %q", revised, changes)
	}
}

func TestParseFlags(t *testing.T) {
	cases := []struct {
		raw  string
		want []string
	}{
		{"", nil},
		{NoUnsupportedClaims, nil},
		{"  " + NoUnsupportedClaims + "\n", nil},
		{"- a\n- b", []string{"a", "b"}},
		{"* one\n  continued here\n2) two\n3. three", []string{"one continued here", "two", "three"}},
		{"Intro line\n- only item", []string{"only item"}},
		{"The whole text reads as narrative.", []string{"The whole text reads as narrative."}},
	}
	for _, c := range cases {
		got := ParseFlags(c.raw, NoUnsupportedClaims)
		if strings.Join(got, "|") != strings.Join(c.want, "|") || got == nil {
			t.Errorf("ParseFlags(%q) = %#v, want %#v", c.raw, got, c.want)
		}
	}
	// A long answer that merely mentions the sentinel is still parsed.
	long := "- claim A: missing\n(Otherwise " + NoUnsupportedClaims + ")"
	if got := ParseFlags(long, NoUnsupportedClaims); len(got) != 1 {
		t.Errorf("sentinel inside a list: %#v", got)
	}
}

func TestRunFlagsWithoutRewriting(t *testing.T) {
	var systems, users []string
	complete := func(_ context.Context, system, user string) (string, error) {
		systems = append(systems, system)
		users = append(users, user)
		if strings.Contains(system, "groundedness critic") {
			return "- \"founded in 1950\": evidence gives 1951\n- \"$2M\": no figure in evidence", nil
		}
		return NoNarrativeClaims, nil
	}
	ev := []Evidence{{Label: "research/a.md", Content: "Founded 1951."}, {Label: "empty.md", Content: " "}}
	r, err := Run(context.Background(), complete, "It was founded in 1950 with $2M.", ev)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Groundedness) != 2 || len(r.Narrative) != 0 || r.Flags != 2 {
		t.Fatalf("result = %+v", r)
	}
	if !strings.Contains(systems[0], "Do not rewrite the text") || !strings.Contains(systems[0], "never call a claim false") {
		t.Errorf("groundedness critic should run in flag mode: %s", systems[0])
	}
	for _, u := range users {
		if !strings.Contains(u, "[research/a.md]\nFounded 1951.") || strings.Contains(u, "[empty.md]") {
			t.Errorf("evidence rendering: %q", u)
		}
	}
	md := r.Markdown()
	for _, want := range []string{"## Groundedness", "- \"founded in 1950\": evidence gives 1951", "## Narrative vs. evidence", NoNarrativeClaims, "is not the same as false"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q:\n%s", want, md)
		}
	}
}

func TestRunErrors(t *testing.T) {
	ok := func(context.Context, string, string) (string, error) { return NoNarrativeClaims, nil }
	if _, err := Run(context.Background(), ok, "  ", nil); err == nil {
		t.Error("empty text should fail")
	}
	calls := 0
	failSecond := func(context.Context, string, string) (string, error) {
		calls++
		if calls == 2 {
			return "", errors.New("rate limited")
		}
		return NoUnsupportedClaims, nil
	}
	if _, err := Run(context.Background(), failSecond, "text", nil); err == nil || !strings.Contains(err.Error(), "narrative critic") {
		t.Errorf("narrative failure: %v", err)
	}
	failFirst := func(context.Context, string, string) (string, error) { return "", errors.New("down") }
	if _, err := Run(context.Background(), failFirst, "text", nil); err == nil || !strings.Contains(err.Error(), "groundedness critic") {
		t.Errorf("groundedness failure: %v", err)
	}
}

func TestLoadEvidence(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	a := write("a.md", "0123456789")
	b := write("b.md", "ééééé") // 10 bytes
	c := write("c.md", "never read")

	l, err := LoadEvidence([]string{a, b}, 0)
	if err != nil || len(l.Evidence) != 2 || l.Truncated || l.Chars != 20 || l.Evidence[0].Label != a {
		t.Fatalf("uncapped = %+v, %v", l, err)
	}
	l, err = LoadEvidence([]string{a, b, c}, 15)
	if err != nil || !l.Truncated || len(l.Evidence) != 2 || len(l.Skipped) != 1 || l.Skipped[0] != c {
		t.Fatalf("capped = %+v, %v", l, err)
	}
	if got := l.Evidence[1].Content; !utf8.ValidString(got) || len(got) > 5 {
		t.Errorf("cut evidence = %q; want at most 5 bytes on a rune boundary", got)
	}
	if _, err := LoadEvidence(nil, 0); err == nil {
		t.Error("no files should fail")
	}
	if _, err := LoadEvidence([]string{filepath.Join(dir, "missing.md")}, 0); err == nil {
		t.Error("missing file should fail")
	}
}

func TestRewritePromptAndNotes(t *testing.T) {
	ev := []Evidence{{Label: "ollama/m1 | shard: s1", Content: "evidence one"}}
	p := BuildGroundednessRewritePrompt("the question", "the draft", ev)
	for _, want := range []string{"Original request:\nthe question", "Draft answer:\nthe draft", "[ollama/m1 | shard: s1]\nevidence one", "clearly marked as uncertain"} {
		if !strings.Contains(p, want) {
			t.Errorf("rewrite prompt missing %q", want)
		}
	}
	if !strings.Contains(GroundednessRewriteSystemPrompt(), ChangesMarker) {
		t.Error("rewrite system prompt must name the changes marker")
	}
	if got := AppendNotes("body", "", " "); got != "body" {
		t.Errorf("no notes: %q", got)
	}
	got := AppendNotes("body", "- g1", "")
	if !strings.Contains(got, "## Critic Notes") || !strings.Contains(got, "### Groundedness Review\n\n- g1") || strings.Contains(got, "Narrative") {
		t.Errorf("groundedness only: %q", got)
	}
	got = AppendNotes("body", "", "- n1")
	if !strings.Contains(got, "### Narrative vs. Evidence\n\n- n1") || strings.Contains(got, "Groundedness Review") {
		t.Errorf("narrative only: %q", got)
	}
}
