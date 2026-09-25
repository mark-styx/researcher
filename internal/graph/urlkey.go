package graph

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// trackingParams are query parameters dropped during URL normalization
// because they identify a click, not a document.
var trackingParams = map[string]bool{
	"fbclid": true, "gclid": true, "dclid": true, "msclkid": true,
	"mc_cid": true, "mc_eid": true, "igshid": true, "_ga": true,
}

// NormalizeURL reduces an http(s) URL to a stable identity string so the
// same document cited in different books maps to one source node. It drops
// the scheme (http and https are the same document), a leading "www.",
// default ports, the fragment, tracking parameters (utm_* and click IDs),
// and a trailing slash; lowercases the host; and sorts query parameters.
// Path case is kept: many servers treat it as significant.
func NormalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("empty URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parsing URL %q: %w", raw, err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("unsupported URL %q: want http or https", raw)
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || strings.ContainsAny(host, " \t") {
		return "", fmt.Errorf("URL %q has no valid host", raw)
	}
	host = strings.TrimPrefix(host, "www.")
	if port := u.Port(); port != "" && port != "80" && port != "443" {
		host += ":" + port
	}

	path := strings.TrimRight(u.EscapedPath(), "/")
	// Undo needless escaping (%7E -> ~), but leave escaped separators
	// (%2F, %3F, %23) alone since decoding them changes the path.
	upper := strings.ToUpper(path)
	if !strings.Contains(upper, "%2F") && !strings.Contains(upper, "%3F") && !strings.Contains(upper, "%23") {
		if unescaped, err := url.PathUnescape(path); err == nil {
			path = (&url.URL{Path: unescaped}).EscapedPath()
		}
	}

	q := u.Query()
	for k := range q {
		if strings.HasPrefix(strings.ToLower(k), "utm_") || trackingParams[strings.ToLower(k)] {
			q.Del(k)
		}
	}
	out := host + path
	if len(q) > 0 {
		keys := make([]string, 0, len(q))
		for k := range q {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var parts []string
		for _, k := range keys {
			vals := q[k]
			sort.Strings(vals)
			for _, v := range vals {
				parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
			}
		}
		out += "?" + strings.Join(parts, "&")
	}
	return out, nil
}

// URLKey is the node_keys key for a source URL.
func URLKey(raw string) (string, error) {
	n, err := NormalizeURL(raw)
	if err != nil {
		return "", err
	}
	return "url:" + n, nil
}

// BookKey is the node_keys key for a book's report node.
func BookKey(slug string) string {
	return "book:" + slug
}
