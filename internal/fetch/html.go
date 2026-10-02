package fetch

import (
	"bytes"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/marklubin/researchguy/internal/store"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	"golang.org/x/net/html/charset"
)

// Page is what was read out of an HTML page.
type Page struct {
	Text      string
	Title     string
	DOI       string
	Published *store.Published
	// Refresh is a meta refresh target, resolved against the page URL:
	// redirect stubs (linkinghub.elsevier.com and the like) use one.
	Refresh string
}

// dropTags never hold the text a reader came for.
var dropTags = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Noscript: true, atom.Template: true,
	atom.Svg: true, atom.Iframe: true, atom.Form: true, atom.Button: true,
	atom.Select: true, atom.Input: true, atom.Textarea: true, atom.Nav: true,
	atom.Footer: true, atom.Aside: true, atom.Object: true, atom.Embed: true,
	atom.Canvas: true, atom.Dialog: true, atom.Menu: true,
}

var dropRoles = map[string]bool{
	"navigation": true, "banner": true, "contentinfo": true, "complementary": true,
	"search": true, "menu": true, "menubar": true, "dialog": true, "alert": true,
}

var (
	unlikely = regexp.MustCompile(`(?i)(^|[-_ ])(comments?|share|sharing|social|related|promo|advert|ads?|cookie|consent|newsletter|subscribe|sidebar|breadcrumbs?|menu|masthead|popup|modal|signup|outbrain|taboola|disqus|skip-link|site-header|site-footer|toolbar)([-_ ]|$)`)
	likely   = regexp.MustCompile(`(?i)(article|body|content|main|post|entry|story|abstract|text)`)
)

// blockTags end the paragraph before and after them.
var blockTags = map[atom.Atom]bool{
	atom.P: true, atom.Div: true, atom.Section: true, atom.Article: true, atom.Main: true,
	atom.H1: true, atom.H2: true, atom.H3: true, atom.H4: true, atom.H5: true, atom.H6: true,
	atom.Li: true, atom.Ul: true, atom.Ol: true, atom.Dl: true, atom.Dt: true, atom.Dd: true,
	atom.Blockquote: true, atom.Pre: true, atom.Table: true, atom.Tr: true, atom.Figure: true,
	atom.Figcaption: true, atom.Header: true, atom.Address: true, atom.Hr: true, atom.Details: true,
	atom.Summary: true, atom.Caption: true, atom.Tbody: true, atom.Thead: true,
}

// ParseHTML reads the main text and the publication metadata out of an HTML
// page. contentType picks the charset when the page doesn't declare one.
func ParseHTML(body []byte, contentType string, pageURL string, now time.Time) Page {
	r, err := charset.NewReader(bytes.NewReader(body), contentType)
	if err != nil {
		r = bytes.NewReader(body)
	}
	doc, err := html.Parse(r)
	if err != nil {
		return Page{}
	}
	m := readMeta(doc)
	var p Page
	p.Title = firstNonEmpty(m.get("citation_title"), m.get("og:title"), m.get("twitter:title"), m.title, m.h1)
	p.DOI = firstNonEmpty(CleanDOI(m.get("citation_doi")), CleanDOI(m.get("prism.doi")), CleanDOI(m.get("bepress_citation_doi")), dcDOI(m.all("dc.identifier")))
	for _, k := range metaDateKeys {
		if k == "datepublished" {
			// JSON-LD sits at the same trust level as itemprop.
			if pub := jsonLDDate(m.jsonLD, now); pub != nil {
				p.Published = pub
				break
			}
		}
		if pub := published(m.get(k), k, false, now); pub != nil {
			p.Published = pub
			break
		}
	}
	if p.Published == nil {
		p.Published = published(m.pubTime, "time-pubdate", false, now)
	}
	if p.Published == nil {
		p.Published = urlPathDate(pageURL, now)
	}
	if m.refresh != "" {
		if base, err := url.Parse(pageURL); err == nil {
			if ref, err := base.Parse(m.refresh); err == nil && (ref.Scheme == "http" || ref.Scheme == "https") {
				p.Refresh = ref.String()
			}
		}
	}
	prune(doc)
	p.Text = render(mainContent(doc))
	return p
}

type meta struct {
	tags    map[string][]string
	title   string
	h1      string
	jsonLD  []string
	pubTime string
	refresh string
}

func (m meta) get(k string) string {
	for _, v := range m.tags[k] {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

func (m meta) all(k string) []string { return m.tags[k] }

func readMeta(doc *html.Node) meta {
	m := meta{tags: map[string][]string{}}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.DataAtom {
			case atom.Meta:
				content := attr(n, "content")
				for _, a := range []string{"name", "property", "itemprop"} {
					if k := strings.ToLower(strings.TrimSpace(attr(n, a))); k != "" {
						m.tags[k] = append(m.tags[k], content)
					}
				}
				if strings.EqualFold(attr(n, "http-equiv"), "refresh") {
					if _, target, ok := strings.Cut(content, "="); ok {
						m.refresh = strings.Trim(strings.TrimSpace(target), `'"`)
					}
				}
			case atom.Title:
				if m.title == "" {
					m.title = collapse(textOf(n))
				}
			case atom.H1:
				if m.h1 == "" {
					m.h1 = collapse(textOf(n))
				}
			case atom.Script:
				if strings.EqualFold(strings.TrimSpace(attr(n, "type")), "application/ld+json") {
					m.jsonLD = append(m.jsonLD, textOf(n))
				}
			case atom.Time:
				if m.pubTime == "" && (hasAttr(n, "pubdate") || strings.EqualFold(attr(n, "itemprop"), "datePublished")) {
					m.pubTime = attr(n, "datetime")
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return m
}

func dcDOI(vals []string) string {
	for _, v := range vals {
		if d := CleanDOI(v); d != "" {
			return d
		}
	}
	return ""
}

// prune removes what isn't content: scripts, navigation, hidden elements,
// and blocks whose class or id marks them as sharing widgets, comments,
// ads and the like.
func prune(n *html.Node) {
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		if c.Type == html.CommentNode || (c.Type == html.ElementNode && drop(c)) {
			n.RemoveChild(c)
		} else {
			prune(c)
		}
		c = next
	}
}

func drop(n *html.Node) bool {
	switch n.DataAtom {
	case atom.Html, atom.Body, atom.Article, atom.Main:
		return false
	case atom.Header:
		return !insideContent(n)
	}
	if dropTags[n.DataAtom] || hasAttr(n, "hidden") || strings.EqualFold(attr(n, "aria-hidden"), "true") {
		return true
	}
	if dropRoles[strings.ToLower(attr(n, "role"))] {
		return true
	}
	style := strings.ReplaceAll(strings.ToLower(attr(n, "style")), " ", "")
	if strings.Contains(style, "display:none") || strings.Contains(style, "visibility:hidden") {
		return true
	}
	cls := attr(n, "class") + " " + attr(n, "id")
	return unlikely.MatchString(cls) && !likely.MatchString(cls)
}

func insideContent(n *html.Node) bool {
	for p := n.Parent; p != nil; p = p.Parent {
		if p.DataAtom == atom.Article || p.DataAtom == atom.Main {
			return true
		}
	}
	return false
}

// mainContent picks the element holding the page's text: the longest
// <article>, else <main>, else the element whose paragraphs score highest,
// else <body>.
func mainContent(doc *html.Node) *html.Node {
	body := find(doc, func(n *html.Node) bool { return n.DataAtom == atom.Body })
	if body == nil {
		body = doc
	}
	const enough = 500
	var best *html.Node
	bestLen := 0
	each(body, func(n *html.Node) {
		if n.DataAtom == atom.Article {
			if l := textLen(n); l > bestLen {
				best, bestLen = n, l
			}
		}
	})
	if bestLen >= enough {
		return best
	}
	if m := find(body, func(n *html.Node) bool {
		return n.DataAtom == atom.Main || strings.EqualFold(attr(n, "role"), "main")
	}); m != nil && textLen(m) >= enough {
		return m
	}
	if c := scoreCandidates(body); c != nil && textLen(c) >= enough/2 {
		return c
	}
	return body
}

// scoreCandidates is a small version of the readability heuristic: each
// paragraph of 25+ characters scores its parent fully and its grandparent
// by half, by length and comma count, discounted by link density.
func scoreCandidates(body *html.Node) *html.Node {
	scores := map[*html.Node]float64{}
	each(body, func(n *html.Node) {
		if n.DataAtom != atom.P && n.DataAtom != atom.Pre && n.DataAtom != atom.Blockquote && n.DataAtom != atom.Td {
			return
		}
		t := collapse(textOf(n))
		l := utf8.RuneCountInString(t)
		if l < 25 {
			return
		}
		s := 1 + float64(strings.Count(t, ",")) + min(float64(l)/100, 3)
		if p := n.Parent; p != nil {
			scores[p] += s
			if g := p.Parent; g != nil {
				scores[g] += s / 2
			}
		}
	})
	var best *html.Node
	bestScore := 0.0
	for n, s := range scores {
		s *= 1 - linkDensity(n)
		if s > bestScore || (s == bestScore && best != nil && textLen(n) > textLen(best)) {
			best, bestScore = n, s
		}
	}
	return best
}

func linkDensity(n *html.Node) float64 {
	total := textLen(n)
	if total == 0 {
		return 0
	}
	links := 0
	each(n, func(c *html.Node) {
		if c.DataAtom == atom.A {
			links += utf8.RuneCountInString(collapse(textOf(c)))
		}
	})
	return float64(links) / float64(total)
}

// render writes an element's text with paragraphs separated by blank lines:
// block elements end paragraphs, <br> ends a line, table cells are joined
// by tabs and <pre> keeps its whitespace.
func render(root *html.Node) string {
	var paras []string
	var cur strings.Builder
	flush := func() {
		if t := strings.TrimSpace(collapseLines(cur.String())); t != "" {
			paras = append(paras, t)
		}
		cur.Reset()
	}
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			// Line breaks in HTML source are spaces; only <br> and table
			// rows break lines.
			cur.WriteString(strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(n.Data))
			return
		}
		if n.Type != html.ElementNode {
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				walk(c)
			}
			return
		}
		switch n.DataAtom {
		case atom.Br:
			cur.WriteString("\n")
			return
		case atom.Td, atom.Th:
			cur.WriteString("\t")
		case atom.Img:
			if alt := strings.TrimSpace(attr(n, "alt")); alt != "" && n.Parent != nil && n.Parent.DataAtom == atom.Figure {
				cur.WriteString(" " + alt + " ")
			}
			return
		case atom.Pre:
			flush()
			if t := strings.Trim(textOf(n), "\n"); strings.TrimSpace(t) != "" {
				paras = append(paras, t)
			}
			return
		case atom.Tr:
			// One table row is one line of the table's paragraph.
			cur.WriteString("\n")
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				walk(c)
			}
			cur.WriteString("\n")
			return
		}
		// A table's row groups stay in the table's paragraph.
		block := blockTags[n.DataAtom] && n.DataAtom != atom.Tbody && n.DataAtom != atom.Thead
		if block {
			flush()
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if block {
			flush()
		}
	}
	walk(root)
	flush()
	return strings.Join(paras, "\n\n")
}

var (
	spaceRun  = regexp.MustCompile(`[ \t\r\f\v\x{00a0}\x{2009}\x{200b}]+`)
	tabSpaces = regexp.MustCompile(` ?\t ?`)
)

// collapseLines collapses runs of spaces within each line and drops empty
// lines, keeping the line breaks <br> and table rows made.
func collapseLines(s string) string {
	lines := strings.Split(s, "\n")
	out := lines[:0]
	for _, l := range lines {
		cells := strings.Split(l, "\t")
		var kept []string
		for _, c := range cells {
			if c = strings.TrimSpace(spaceRun.ReplaceAllString(c, " ")); c != "" {
				kept = append(kept, c)
			}
		}
		if len(kept) > 0 {
			out = append(out, tabSpaces.ReplaceAllString(strings.Join(kept, "\t"), "\t"))
		}
	}
	return strings.Join(out, "\n")
}

func collapse(s string) string {
	return strings.TrimSpace(spaceRun.ReplaceAllString(strings.ReplaceAll(s, "\n", " "), " "))
}

func textOf(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

func textLen(n *html.Node) int { return utf8.RuneCountInString(collapse(textOf(n))) }

func each(n *html.Node, f func(*html.Node)) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode {
			f(c)
		}
		each(c, f)
	}
}

func find(n *html.Node, match func(*html.Node) bool) *html.Node {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && match(c) {
			return c
		}
		if f := find(c, match); f != nil {
			return f
		}
	}
	return nil
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

func hasAttr(n *html.Node, key string) bool {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return true
		}
	}
	return false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}
