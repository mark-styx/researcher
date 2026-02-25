package tools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"golang.org/x/net/html"
)

const (
	maxFetchBytes = 100 * 1024 // 100KB download limit
	maxTextBytes  = 50 * 1024  // 50KB text output limit
	fetchTimeout  = 30 * time.Second
)

// WebFetch retrieves a URL and extracts its text content.
func WebFetch(ctx context.Context, targetURL string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", targetURL, nil)
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Researcher/1.0)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching URL: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d for %s", resp.StatusCode, targetURL)
	}

	// Limit read size
	limited := io.LimitReader(resp.Body, maxFetchBytes)
	body, err := io.ReadAll(limited)
	if err != nil {
		return "", fmt.Errorf("reading response: %w", err)
	}

	contentType := resp.Header.Get("Content-Type")

	// Plain text: return as-is
	if strings.Contains(contentType, "text/plain") {
		return truncate(string(body), maxTextBytes), nil
	}

	// HTML: extract text
	text, err := extractText(string(body))
	if err != nil {
		// Fallback: strip tags
		text = stripTags(string(body))
	}

	return truncate(text, maxTextBytes), nil
}

// extractText walks an HTML document and extracts visible text.
func extractText(raw string) (string, error) {
	doc, err := html.Parse(strings.NewReader(raw))
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	var walk func(*html.Node)

	// Tags to skip entirely
	skipTags := map[string]bool{
		"script": true, "style": true, "nav": true,
		"header": true, "footer": true, "noscript": true,
		"svg": true, "iframe": true,
	}

	// Block elements that should have newlines
	blockTags := map[string]bool{
		"p": true, "div": true, "br": true, "h1": true, "h2": true,
		"h3": true, "h4": true, "h5": true, "h6": true, "li": true,
		"tr": true, "blockquote": true, "pre": true, "section": true,
		"article": true, "main": true,
	}

	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && skipTags[n.Data] {
			return
		}

		if n.Type == html.ElementNode && blockTags[n.Data] {
			sb.WriteString("\n")
		}

		if n.Type == html.TextNode {
			text := strings.TrimSpace(n.Data)
			if text != "" {
				sb.WriteString(text)
				sb.WriteString(" ")
			}
		}

		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}

		if n.Type == html.ElementNode && blockTags[n.Data] {
			sb.WriteString("\n")
		}
	}

	walk(doc)

	// Clean up excessive whitespace
	text := sb.String()
	lines := strings.Split(text, "\n")
	var cleaned []string
	prevEmpty := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			if !prevEmpty {
				cleaned = append(cleaned, "")
				prevEmpty = true
			}
			continue
		}
		cleaned = append(cleaned, line)
		prevEmpty = false
	}

	return strings.TrimSpace(strings.Join(cleaned, "\n")), nil
}

// stripTags is a simple fallback that removes HTML tags.
func stripTags(s string) string {
	var sb strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
			sb.WriteRune(' ')
		case !inTag:
			sb.WriteRune(r)
		}
	}
	return strings.TrimSpace(sb.String())
}

func truncate(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	return s[:maxBytes] + "\n\n[truncated]"
}
