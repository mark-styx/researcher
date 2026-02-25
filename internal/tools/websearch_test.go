package tools

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func TestExtractDDGURL(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "DDG redirect URL",
			input: "//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fpage&rut=abc",
			want:  "https://example.com/page",
		},
		{
			name:  "direct http URL",
			input: "https://example.com/direct",
			want:  "https://example.com/direct",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "no uddg param",
			input: "//duckduckgo.com/l/?foo=bar",
			want:  "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractDDGURL(tc.input)
			if got != tc.want {
				t.Errorf("extractDDGURL(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestExtractResults(t *testing.T) {
	// Build DDG-style HTML with result divs
	htmlStr := `<html><body>
		<div class="result">
			<a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2F1">First Result</a>
			<span class="result__snippet">Snippet one</span>
		</div>
		<div class="result">
			<a class="result__a" href="https://example.com/2">Second Result</a>
			<span class="result__snippet">Snippet two</span>
		</div>
		<div class="result">
			<a class="result__a" href="https://example.com/3">Third Result</a>
			<span class="result__snippet">Snippet three</span>
		</div>
	</body></html>`

	doc, err := html.Parse(strings.NewReader(htmlStr))
	if err != nil {
		t.Fatal(err)
	}

	// Get all results
	results := extractResults(doc, 10)
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	if results[0].Title != "First Result" {
		t.Errorf("result[0].Title = %q, want %q", results[0].Title, "First Result")
	}
	if results[0].URL != "https://example.com/1" {
		t.Errorf("result[0].URL = %q, want %q", results[0].URL, "https://example.com/1")
	}
	if results[0].Snippet != "Snippet one" {
		t.Errorf("result[0].Snippet = %q, want %q", results[0].Snippet, "Snippet one")
	}

	// Verify max cap
	capped := extractResults(doc, 2)
	if len(capped) != 2 {
		t.Errorf("expected max 2 results, got %d", len(capped))
	}
}

func TestFormatSearchResults(t *testing.T) {
	t.Run("empty results", func(t *testing.T) {
		got := FormatSearchResults(nil)
		if got != "No search results found." {
			t.Errorf("got %q, want %q", got, "No search results found.")
		}
	})

	t.Run("single result with snippet", func(t *testing.T) {
		results := []SearchResult{
			{Title: "Test", URL: "https://example.com", Snippet: "A snippet"},
		}
		got := FormatSearchResults(results)
		if !strings.Contains(got, "**Test**") {
			t.Error("missing title in output")
		}
		if !strings.Contains(got, "https://example.com") {
			t.Error("missing URL in output")
		}
		if !strings.Contains(got, "A snippet") {
			t.Error("missing snippet in output")
		}
	})

	t.Run("result without snippet", func(t *testing.T) {
		results := []SearchResult{
			{Title: "NoSnip", URL: "https://example.com"},
		}
		got := FormatSearchResults(results)
		if !strings.Contains(got, "**NoSnip**") {
			t.Error("missing title in output")
		}
		// Should NOT have an extra indented line for snippet
		lines := strings.Split(strings.TrimSpace(got), "\n")
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed != "" && !strings.HasPrefix(trimmed, "1.") && !strings.HasPrefix(trimmed, "URL:") {
				t.Errorf("unexpected line: %q", line)
			}
		}
	})
}

func TestHasClass(t *testing.T) {
	tests := []struct {
		name  string
		attrs []html.Attribute
		class string
		want  bool
	}{
		{
			name:  "matching class",
			attrs: []html.Attribute{{Key: "class", Val: "result"}},
			class: "result",
			want:  true,
		},
		{
			name:  "multi-class match",
			attrs: []html.Attribute{{Key: "class", Val: "foo result bar"}},
			class: "result",
			want:  true,
		},
		{
			name:  "no match",
			attrs: []html.Attribute{{Key: "class", Val: "other"}},
			class: "result",
			want:  false,
		},
		{
			name:  "no class attr",
			attrs: []html.Attribute{{Key: "id", Val: "result"}},
			class: "result",
			want:  false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			node := &html.Node{
				Type: html.ElementNode,
				Data: "div",
				Attr: tc.attrs,
			}
			got := hasClass(node, tc.class)
			if got != tc.want {
				t.Errorf("hasClass = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTextContent(t *testing.T) {
	htmlStr := `<div>Hello <span>beautiful <b>world</b></span></div>`
	doc, err := html.Parse(strings.NewReader(htmlStr))
	if err != nil {
		t.Fatal(err)
	}

	got := textContent(doc)
	if got != "Hello beautiful world" {
		t.Errorf("textContent = %q, want %q", got, "Hello beautiful world")
	}
}
