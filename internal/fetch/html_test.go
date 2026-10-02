package fetch

import (
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

const articlePage = `<!doctype html>
<html><head>
<title>Feed study | Example Journal</title>
<meta property="og:title" content="OG title">
<meta name="citation_title" content="Exposure to the feed and stated opinion">
<meta name="citation_publication_date" content="2025/03/14">
<meta property="article:published_time" content="2025-04-01T09:00:00Z">
<meta name="citation_doi" content="doi:10.1234/EJ.2025.77">
<script>var tracking = "do not keep";</script>
<style>.x{color:red}</style>
</head><body>
<header class="site-header"><a href="/">Home</a> <a href="/about">About</a></header>
<nav><ul><li><a href="/a">Section A</a></li><li><a href="/b">Section B</a></li></ul></nav>
<div class="share-buttons"><a href="#">Share on X</a></div>
<article>
  <header><h1>Exposure to the feed</h1><p class="byline">By A. Author</p></header>
  <p>Exposure to the feed shifted stated opinion by 0.1 SD, a small effect, in a sample of 1,200 adults.</p>
  <p>The effect   faded after
  two weeks, which the authors attribute to novelty.<br>A second line.</p>
  <table><tr><th>Group</th><th>Effect</th></tr><tr><td>Treated</td><td>0.1</td></tr></table>
  <ul><li>First point about the result, long enough to count.</li><li>Second point.</li></ul>
  <aside>Related: other stories</aside>
  <div style="display: none">hidden text</div>
  <p>` + "Padding sentence to make the article long enough to be picked as main content. " + `</p>
  <p>More padding so that the article clears the five hundred character threshold easily, with commas, clauses, and words.</p>
  <p>Even more padding text, because the threshold is measured in characters of visible text in the article element.</p>
</article>
<footer>Copyright Example Journal</footer>
<!-- a comment -->
</body></html>`

func TestParseHTML_ArticleTextAndMetadata(t *testing.T) {
	p := ParseHTML([]byte(articlePage), "text/html; charset=utf-8", "https://example.org/articles/77", testNow)
	if p.Title != "Exposure to the feed and stated opinion" {
		t.Errorf("Title = %q, want citation_title", p.Title)
	}
	if p.DOI != "10.1234/ej.2025.77" {
		t.Errorf("DOI = %q", p.DOI)
	}
	if p.Published == nil || p.Published.Date != "2025-03-14" || p.Published.Precision != "day" || p.Published.From != "citation_publication_date" || p.Published.Weak {
		t.Errorf("Published = %+v, want citation_publication_date 2025-03-14", p.Published)
	}
	for _, want := range []string{
		"Exposure to the feed shifted stated opinion by 0.1 SD",
		"The effect faded after two weeks, which the authors attribute to novelty.\nA second line.",
		"Group\tEffect\nTreated\t0.1",
		"First point about the result",
		"By A. Author",
	} {
		if !strings.Contains(p.Text, want) {
			t.Errorf("text missing %q:\n%s", want, p.Text)
		}
	}
	for _, unwanted := range []string{"tracking", "Section A", "Share on X", "Related: other stories", "hidden text", "Copyright", "color:red", "a comment", "Home"} {
		if strings.Contains(p.Text, unwanted) {
			t.Errorf("text kept %q", unwanted)
		}
	}
	if !strings.Contains(p.Text, "\n\n") {
		t.Error("paragraphs not separated by blank lines")
	}
}

func TestParseHTML_ScoresParagraphsWithoutArticle(t *testing.T) {
	body := `<html><body>
<div id="menu"><a href="/">Home</a><a href="/x">Link</a><a href="/y">Another link here</a></div>
<div class="wrapper"><div class="story">` +
		strings.Repeat(`<p>This is a paragraph of real content, with commas, clauses, and enough words to score well.</p>`, 8) +
		`</div></div>
<div class="links"><p><a href="/1">A paragraph that is entirely a link and should lose to the story text.</a></p></div>
</body></html>`
	p := ParseHTML([]byte(body), "text/html", "https://example.org/x", testNow)
	if !strings.Contains(p.Text, "This is a paragraph of real content") {
		t.Fatalf("story text missing:\n%s", p.Text)
	}
	if strings.Contains(p.Text, "entirely a link") || strings.Contains(p.Text, "Another link here") {
		t.Errorf("link block kept:\n%s", p.Text)
	}
}

func TestParseHTML_DateSources(t *testing.T) {
	cases := []struct {
		name, head, url  string
		date, prec, from string
		weak             bool
	}{
		{"json-ld in a graph", `<script type="application/ld+json">{"@context":"https://schema.org","@graph":[{"@type":"WebPage"},{"@type":"NewsArticle","datePublished":"2024-11-05T14:00:00-05:00"}]}</script>`, "https://n.example/a", "2024-11-05", "day", "json-ld", false},
		{"article:published_time", `<meta property="article:published_time" content="2023-07-01T00:00:00Z">`, "https://n.example/a", "2023-07-01", "day", "article:published_time", false},
		{"year only", `<meta name="citation_date" content="1995">`, "https://n.example/a", "1995", "year", "citation_date", false},
		{"month", `<meta name="citation_publication_date" content="2019/05">`, "https://n.example/a", "2019-05", "month", "citation_publication_date", false},
		{"textual", `<meta name="dc.date" content="March 3, 2021">`, "https://n.example/a", "2021-03-03", "day", "dc.date", false},
		{"time pubdate", `</head><body><time pubdate datetime="2022-02-02">Feb 2</time>`, "https://n.example/a", "2022-02-02", "day", "time-pubdate", false},
		{"url path", ``, "https://n.example/2020/06/30/story", "2020-06-30", "day", "url-path", false},
		{"future date ignored, url used", `<meta name="citation_date" content="2099-01-01">`, "https://n.example/2020/06/story", "2020-06", "month", "url-path", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := ParseHTML([]byte("<html><head>"+c.head+"</head><body><p>x</p></body></html>"), "text/html", c.url, testNow)
			if p.Published == nil {
				t.Fatalf("no date")
			}
			got := *p.Published
			if got.Date != c.date || got.Precision != c.prec || got.From != c.from || got.Weak != c.weak {
				t.Errorf("Published = %+v, want %s %s from %s", got, c.date, c.prec, c.from)
			}
		})
	}
	p := ParseHTML([]byte(`<html><body><p>no date anywhere</p></body></html>`), "text/html", "https://n.example/page", testNow)
	if p.Published != nil {
		t.Errorf("undated page got %+v; an unknown date stays unknown", p.Published)
	}
}

func TestParseHTML_RefreshAndCharset(t *testing.T) {
	p := ParseHTML([]byte(`<html><head><meta http-equiv="refresh" content="0; URL='/retrieve/pii/S123?x=1'"></head><body>Redirecting</body></html>`), "text/html", "https://linkinghub.example/retrieve", testNow)
	if p.Refresh != "https://linkinghub.example/retrieve/pii/S123?x=1" {
		t.Errorf("Refresh = %q", p.Refresh)
	}
	latin1 := []byte("<html><head><meta charset=\"iso-8859-1\"></head><body><p>Caf\xe9 au lait</p></body></html>")
	if p := ParseHTML(latin1, "text/html", "https://x.example", testNow); !strings.Contains(p.Text, "Café au lait") {
		t.Errorf("latin-1 text = %q", p.Text)
	}
}

func TestParseHTML_TitleFallbacks(t *testing.T) {
	p := ParseHTML([]byte(`<html><head><title> Plain   title </title></head><body><h1>Heading</h1></body></html>`), "text/html", "https://x.example", testNow)
	if p.Title != "Plain title" {
		t.Errorf("Title = %q", p.Title)
	}
	p = ParseHTML([]byte(`<html><body><h1>Only heading</h1><p>text</p></body></html>`), "text/html", "https://x.example", testNow)
	if p.Title != "Only heading" {
		t.Errorf("Title = %q", p.Title)
	}
}
