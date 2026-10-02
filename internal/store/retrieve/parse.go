package retrieve

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/embed"
	"github.com/marklubin/researchguy/internal/store"
	"github.com/marklubin/researchguy/internal/store/index"
)

// Open opens the retriever store.dir and store.dsn configure, embedding
// queries with store.embed's model. Close releases it.
func Open(ctx context.Context, cfg *config.Config) (*Retriever, error) {
	if strings.TrimSpace(cfg.Store.DSN) == "" {
		return nil, fmt.Errorf("store.dsn is not set, so there is no index to search")
	}
	st, err := store.Open(cfg.Store.Dir)
	if err != nil {
		return nil, err
	}
	ix, err := index.Open(ctx, cfg.Store.DSN)
	if err != nil {
		return nil, err
	}
	emb := embed.New(cfg)
	ix.SetEmbedModel(emb.Model())
	return &Retriever{Index: ix, Store: st, Embed: emb}, nil
}

// Close closes the retriever's index.
func (r *Retriever) Close() {
	if r != nil && r.Index != nil {
		r.Index.Close()
	}
}

// ParseAge parses an age such as 90d, 2w, 24h or 1y (365 days).
func ParseAge(s string) (time.Duration, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) < 2 {
		return 0, fmt.Errorf("invalid age %q (want a number and d, w, h or y, such as 90d)", s)
	}
	n, err := strconv.Atoi(s[:len(s)-1])
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid age %q (want a number and d, w, h or y, such as 90d)", s)
	}
	unit := map[byte]time.Duration{'h': time.Hour, 'd': 24 * time.Hour, 'w': 7 * 24 * time.Hour, 'y': 365 * 24 * time.Hour}[s[len(s)-1]]
	if unit == 0 {
		return 0, fmt.Errorf("invalid age %q (want a number and d, w, h or y, such as 90d)", s)
	}
	return time.Duration(n) * unit, nil
}

// ParseBound parses a date bound: YYYY, YYYY-MM, YYYY-MM-DD, an RFC 3339
// time, or an age (90d, 1y) before now. A date names a period, so as an
// upper bound (end) it means the period's last moment: until 2020 takes in
// all of 2020.
func ParseBound(s string, now time.Time, end bool) (time.Time, error) {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	for _, f := range []struct {
		layout string
		next   func(time.Time) time.Time
	}{
		{"2006-01-02", func(t time.Time) time.Time { return t.AddDate(0, 0, 1) }},
		{"2006-01", func(t time.Time) time.Time { return t.AddDate(0, 1, 0) }},
		{"2006", func(t time.Time) time.Time { return t.AddDate(1, 0, 0) }},
	} {
		if t, err := time.Parse(f.layout, s); err == nil {
			if end {
				return f.next(t).Add(-time.Nanosecond), nil
			}
			return t, nil
		}
	}
	if d, err := ParseAge(s); err == nil {
		return now.UTC().Add(-d), nil
	}
	return time.Time{}, fmt.Errorf("invalid date %q (want YYYY, YYYY-MM, YYYY-MM-DD or an age such as 90d)", s)
}

// Args are a find request's filters as text, the way the CLI and the MCP
// tools take them.
type Args struct {
	Kinds              []string
	Since, Until, AsOf string
	DateField          string
	RunID, Domain      string
	PreferRecent       string
	MinSimilarity      float64
	Limit              int
}

// Query parses a into a Query for text, reading dates and ages relative
// to now.
func (a Args) Query(text string, now time.Time) (Query, error) {
	q := Query{Text: text, Kinds: a.Kinds, DateField: strings.TrimSpace(a.DateField), RunID: strings.TrimSpace(a.RunID),
		Domain: a.Domain, MinSimilarity: a.MinSimilarity, Limit: a.Limit}
	for _, b := range []struct {
		name, val string
		end       bool
		dst       **time.Time
	}{{"since", a.Since, false, &q.Since}, {"until", a.Until, true, &q.Until}, {"as_of", a.AsOf, true, &q.AsOf}} {
		if strings.TrimSpace(b.val) == "" {
			continue
		}
		t, err := ParseBound(b.val, now, b.end)
		if err != nil {
			return q, fmt.Errorf("%s: %w", b.name, err)
		}
		*b.dst = &t
	}
	if strings.TrimSpace(a.PreferRecent) != "" {
		d, err := ParseAge(a.PreferRecent)
		if err != nil {
			return q, fmt.Errorf("prefer_recent: %w", err)
		}
		q.PreferRecent = d
	}
	return q, nil
}
