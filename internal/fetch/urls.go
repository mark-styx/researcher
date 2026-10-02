package fetch

import (
	"net/url"
	"path"
	"regexp"
	"strings"
)

var urlRe = regexp.MustCompile(`https?://[^\s<>"'` + "`" + `\]\[{}|\\^]+`)

// ExtractURLs returns every http(s) URL in text, in order, with trailing
// punctuation and unbalanced closing parentheses trimmed, so a markdown
// link's ")" isn't part of the URL but /wiki/Foo_(bar) keeps its own.
func ExtractURLs(text string) []string {
	var out []string
	for _, m := range urlRe.FindAllString(text, -1) {
		u := trimURL(m)
		if len(u) > len("https://") {
			out = append(out, u)
		}
	}
	return out
}

func trimURL(u string) string {
	for {
		trimmed := strings.TrimRight(u, ".,;:!?*_'\"")
		if strings.HasSuffix(trimmed, ")") && strings.Count(trimmed, "(") < strings.Count(trimmed, ")") {
			trimmed = trimmed[:len(trimmed)-1]
		}
		if trimmed == u {
			return u
		}
		u = trimmed
	}
}

var doiRe = regexp.MustCompile(`(?i)\b(10\.\d{4,9}/[-._;()/:a-z0-9<>]*[a-z0-9)])`)

// DOIFromURL returns the DOI a URL names, lowercased, or "": a doi.org link,
// a publisher path with /doi/<doi>, or a query value that is a DOI.
func DOIFromURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	p, _ := url.PathUnescape(u.EscapedPath())
	if host == "doi.org" || host == "dx.doi.org" {
		return CleanDOI(strings.Trim(p, "/"))
	}
	if i := strings.Index(strings.ToLower(p), "/doi/"); i >= 0 {
		if m := doiRe.FindString(p[i:]); m != "" {
			return CleanDOI(m)
		}
	}
	for _, vs := range u.Query() {
		for _, v := range vs {
			if m := doiRe.FindString(v); m != "" && strings.HasPrefix(strings.TrimSpace(v), m) {
				return CleanDOI(m)
			}
		}
	}
	return ""
}

// CleanDOI normalizes a DOI written with a doi: or doi.org prefix, and
// returns "" for anything that isn't one.
func CleanDOI(s string) string {
	s = strings.TrimSpace(s)
	low := strings.ToLower(s)
	for _, p := range []string{"https://doi.org/", "http://doi.org/", "https://dx.doi.org/", "http://dx.doi.org/", "doi:", "doi "} {
		if strings.HasPrefix(low, p) {
			s = strings.TrimSpace(s[len(p):])
			low = strings.ToLower(s)
		}
	}
	m := doiRe.FindString(s)
	if m == "" || !strings.HasPrefix(s, m) {
		return ""
	}
	return strings.ToLower(strings.TrimRight(m, ".,;"))
}

// skipExt are file types that aren't documents to read.
var skipExt = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true, ".svg": true, ".ico": true,
	".mp4": true, ".mov": true, ".webm": true, ".mp3": true, ".wav": true, ".m4a": true,
	".zip": true, ".gz": true, ".tgz": true, ".dmg": true, ".exe": true, ".pkg": true,
	".css": true, ".js": true, ".woff": true, ".woff2": true, ".ttf": true,
}

// fetchable reports whether a URL is worth fetching as a document.
func fetchable(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return false
	}
	return !skipExt[strings.ToLower(path.Ext(u.Path))]
}
