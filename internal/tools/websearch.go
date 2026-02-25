package tools

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

// SearchResult holds a single search result.
type SearchResult struct {
	Title   string
	URL     string
	Snippet string
}

// WebSearch queries DuckDuckGo HTML and returns parsed results.
func WebSearch(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
	if maxResults <= 0 {
		maxResults = 10
	}

	searchURL := "https://html.duckduckgo.com/html/?q=" + url.QueryEscape(query)
	req, err := http.NewRequestWithContext(ctx, "GET", searchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Researcher/1.0)")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching search results: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("DuckDuckGo returned status %d", resp.StatusCode)
	}

	doc, err := html.Parse(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parsing HTML: %w", err)
	}

	return extractResults(doc, maxResults), nil
}

func extractResults(doc *html.Node, max int) []SearchResult {
	var results []SearchResult
	var walk func(*html.Node)

	walk = func(n *html.Node) {
		if len(results) >= max {
			return
		}

		if n.Type == html.ElementNode && n.Data == "div" && hasClass(n, "result") {
			r := parseResult(n)
			if r.URL != "" && r.Title != "" {
				results = append(results, r)
			}
		}

		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}

	walk(doc)
	return results
}

func parseResult(n *html.Node) SearchResult {
	var r SearchResult
	var walk func(*html.Node)

	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" && hasClass(n, "result__a") {
			r.Title = textContent(n)
			r.URL = extractDDGURL(getAttr(n, "href"))
		}
		if n.Type == html.ElementNode && hasClass(n, "result__snippet") {
			r.Snippet = textContent(n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}

	walk(n)
	return r
}

// extractDDGURL extracts the actual URL from DuckDuckGo's redirect URL.
func extractDDGURL(rawURL string) string {
	if rawURL == "" {
		return ""
	}

	// DDG wraps URLs like //duckduckgo.com/l/?uddg=<encoded_url>&...
	if strings.Contains(rawURL, "uddg=") {
		parsed, err := url.Parse(rawURL)
		if err == nil {
			if uddg := parsed.Query().Get("uddg"); uddg != "" {
				return uddg
			}
		}
	}

	// Sometimes it's a direct URL
	if strings.HasPrefix(rawURL, "http") {
		return rawURL
	}

	return ""
}

func hasClass(n *html.Node, class string) bool {
	for _, a := range n.Attr {
		if a.Key == "class" {
			for _, c := range strings.Fields(a.Val) {
				if c == class {
					return true
				}
			}
		}
	}
	return false
}

func getAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func textContent(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			sb.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.TrimSpace(sb.String())
}

// FormatSearchResults renders results as a numbered markdown list.
func FormatSearchResults(results []SearchResult) string {
	if len(results) == 0 {
		return "No search results found."
	}

	var sb strings.Builder
	for i, r := range results {
		sb.WriteString(fmt.Sprintf("%d. **%s**\n   URL: %s\n", i+1, r.Title, r.URL))
		if r.Snippet != "" {
			sb.WriteString(fmt.Sprintf("   %s\n", r.Snippet))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}
