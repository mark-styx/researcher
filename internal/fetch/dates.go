package fetch

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/marklubin/researchguy/internal/store"
)

// metaDateKeys are the meta tags (name, property or itemprop, lowercased)
// that state a publication date, most trusted first. Scholarly citation
// tags come first: publishers set them for Google Scholar and they name the
// version of record.
var metaDateKeys = []string{
	"citation_publication_date",
	"citation_date",
	"citation_online_date",
	"article:published_time",
	"og:article:published_time",
	"dc.date.issued",
	"dcterms.issued",
	"prism.publicationdate",
	"dc.date",
	"dcterms.date",
	"datepublished",
	"pubdate",
	"publishdate",
	"publish-date",
	"parsely-pub-date",
	"sailthru.date",
	"date",
}

var (
	isoDay   = regexp.MustCompile(`^(\d{4})[-/.](\d{1,2})[-/.](\d{1,2})(?:$|[T\s])`)
	isoMonth = regexp.MustCompile(`^(\d{4})[-/](\d{1,2})$`)
	isoYear  = regexp.MustCompile(`^(\d{4})$`)
	// A date in a URL path: /2024/03/15/ or /2024/03/.
	urlDay   = regexp.MustCompile(`/((?:19|20)\d{2})/(\d{2})/(\d{2})(?:/|$)`)
	urlMonth = regexp.MustCompile(`/((?:19|20)\d{2})/(\d{2})(?:/|$)`)
)

var dayLayouts = []string{
	"January 2, 2006", "Jan 2, 2006", "2 January 2006", "2 Jan 2006",
	"2006 January 2", "2006 Jan 2", "Monday, January 2, 2006",
	time.RFC1123, time.RFC1123Z, time.RFC850, time.ANSIC,
	"Mon, 2 Jan 2006 15:04:05 -0700", "Mon, 2 Jan 2006 15:04:05 MST",
}

var monthLayouts = []string{"January 2006", "Jan 2006", "2006 January", "2006 Jan"}

// parseDate reads a date as publishers write it and returns it as YYYY,
// YYYY-MM or YYYY-MM-DD with its precision. The date is taken as written:
// a timestamp's time zone isn't applied, since the calendar date the
// source shows is what's being recorded.
func parseDate(s string, now time.Time) (string, string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", false
	}
	if m := isoDay.FindStringSubmatch(s); m != nil {
		return checkDate(atoi(m[1]), atoi(m[2]), atoi(m[3]), "day", now)
	}
	if m := isoMonth.FindStringSubmatch(s); m != nil {
		return checkDate(atoi(m[1]), atoi(m[2]), 1, "month", now)
	}
	if m := isoYear.FindStringSubmatch(s); m != nil {
		return checkDate(atoi(m[1]), 1, 1, "year", now)
	}
	for _, l := range dayLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return checkDate(t.Year(), int(t.Month()), t.Day(), "day", now)
		}
	}
	for _, l := range monthLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return checkDate(t.Year(), int(t.Month()), 1, "month", now)
		}
	}
	return "", "", false
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

// checkDate rejects impossible dates and ones in the future (two days of
// slack for time zones), and formats the rest to their precision.
func checkDate(y, m, d int, precision string, now time.Time) (string, string, bool) {
	if y < 1000 || m < 1 || m > 12 || d < 1 {
		return "", "", false
	}
	t := time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
	if t.Day() != d || t.After(now.Add(48*time.Hour)) {
		return "", "", false
	}
	switch precision {
	case "year":
		return fmt.Sprintf("%04d", y), precision, true
	case "month":
		return fmt.Sprintf("%04d-%02d", y, m), precision, true
	}
	return t.Format("2006-01-02"), precision, true
}

func published(raw, from string, weak bool, now time.Time) *store.Published {
	date, precision, ok := parseDate(raw, now)
	if !ok {
		return nil
	}
	return &store.Published{Date: date, Precision: precision, From: from, Weak: weak}
}

// jsonLDDate finds the first datePublished in JSON-LD blocks, searching
// nested objects and @graph arrays.
func jsonLDDate(blocks []string, now time.Time) *store.Published {
	for _, b := range blocks {
		var v any
		if json.Unmarshal([]byte(strings.TrimSpace(b)), &v) != nil {
			continue
		}
		if s := findKey(v, "datePublished", 0); s != "" {
			if p := published(s, "json-ld", false, now); p != nil {
				return p
			}
		}
	}
	return nil
}

func findKey(v any, key string, depth int) string {
	if depth > 8 {
		return ""
	}
	switch x := v.(type) {
	case map[string]any:
		if s, ok := x[key].(string); ok && s != "" {
			return s
		}
		for _, k := range []string{"@graph", "mainEntity", "mainEntityOfPage"} {
			if s := findKey(x[k], key, depth+1); s != "" {
				return s
			}
		}
	case []any:
		for _, e := range x {
			if s := findKey(e, key, depth+1); s != "" {
				return s
			}
		}
	}
	return ""
}

// urlPathDate reads a /YYYY/MM/DD/ or /YYYY/MM/ date from a URL path.
func urlPathDate(raw string, now time.Time) *store.Published {
	u, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	if m := urlDay.FindStringSubmatch(u.Path); m != nil {
		if d, p, ok := checkDate(atoi(m[1]), atoi(m[2]), atoi(m[3]), "day", now); ok {
			return &store.Published{Date: d, Precision: p, From: "url-path"}
		}
	}
	if m := urlMonth.FindStringSubmatch(u.Path); m != nil {
		if d, p, ok := checkDate(atoi(m[1]), atoi(m[2]), 1, "month", now); ok {
			return &store.Published{Date: d, Precision: p, From: "url-path"}
		}
	}
	return nil
}
