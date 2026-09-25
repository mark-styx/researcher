package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"
)

// URL check classes.
const (
	ClassOK      = "ok"      // final response 2xx/3xx
	ClassBlocked = "blocked" // 401/403/429/999: exists but refuses automated clients
	ClassDead    = "dead"    // 404/410 or the host does not resolve
	ClassError   = "error"   // anything else: 5xx, timeouts, TLS, resets
)

// CheckResult is the outcome of fetching one URL.
type CheckResult struct {
	URL    string `json:"url"`
	Status int    `json:"status,omitempty"`
	Class  string `json:"class"`
	Err    string `json:"err,omitempty"`
}

// Browser-like UA: the question is whether a reader following the citation
// gets the page, and many sites reject unknown clients outright.
const checkUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36"

// CheckURL tries HEAD, then falls back to GET when HEAD errors or returns
// 4xx/5xx (many servers mishandle HEAD).
func CheckURL(ctx context.Context, client *http.Client, raw string) CheckResult {
	status, err := fetchStatus(ctx, client, http.MethodHead, raw)
	if err != nil || status >= 400 {
		status, err = fetchStatus(ctx, client, http.MethodGet, raw)
	}
	return classify(raw, status, err)
}

func fetchStatus(ctx context.Context, client *http.Client, method, raw string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, method, raw, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", checkUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/pdf,*/*;q=0.8")
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.CopyN(io.Discard, resp.Body, 64<<10)
	return resp.StatusCode, nil
}

func classify(raw string, status int, err error) CheckResult {
	r := CheckResult{URL: raw, Status: status}
	if err != nil {
		r.Err = err.Error()
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
			r.Class = ClassDead
		} else {
			r.Class = ClassError
		}
		return r
	}
	switch {
	case status >= 200 && status < 400:
		r.Class = ClassOK
	case status == 401 || status == 403 || status == 429 || status == 999:
		r.Class = ClassBlocked
	case status == 404 || status == 410:
		r.Class = ClassDead
	default:
		r.Class = ClassError
	}
	return r
}

// CheckAll checks every URL not already in cache, parallel workers at a
// time, and returns the merged cache.
func CheckAll(ctx context.Context, client *http.Client, urls []string, cache map[string]CheckResult, parallel int) map[string]CheckResult {
	out := make(map[string]CheckResult, len(cache)+len(urls))
	for k, v := range cache {
		out[k] = v
	}
	var todo []string
	seen := make(map[string]bool)
	for _, u := range urls {
		if _, ok := out[u]; ok || seen[u] {
			continue
		}
		seen[u] = true
		todo = append(todo, u)
	}
	sort.Strings(todo)
	if parallel < 1 {
		parallel = 1
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, parallel)
	for _, u := range todo {
		wg.Add(1)
		go func(u string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			r := CheckURL(ctx, client, u)
			mu.Lock()
			out[u] = r
			mu.Unlock()
		}(u)
	}
	wg.Wait()
	return out
}

// NewCheckClient returns a client with a per-request timeout that follows
// up to 10 redirects.
func NewCheckClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout}
}

// LoadCache reads a URL check cache; a missing file is an empty cache.
func LoadCache(path string) (map[string]CheckResult, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]CheckResult{}, nil
	}
	if err != nil {
		return nil, err
	}
	var m map[string]CheckResult
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// SaveCache writes the cache as indented JSON.
func SaveCache(path string, m map[string]CheckResult) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// ClassCounts tallies results by class for the given URLs.
func ClassCounts(urls []string, results map[string]CheckResult) map[string]int {
	out := map[string]int{ClassOK: 0, ClassBlocked: 0, ClassDead: 0, ClassError: 0}
	for _, u := range urls {
		if r, ok := results[u]; ok {
			out[r.Class]++
		}
	}
	return out
}
