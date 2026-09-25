package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func newCheckServer(t *testing.T, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(200)
	})
	mux.HandleFunc("/nohead", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(405)
			return
		}
		w.WriteHeader(200)
	})
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ok", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/moved/deep/page", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/", http.StatusFound)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(200)
	})
	mux.HandleFunc("/gone", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
	mux.HandleFunc("/forbidden", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) })
	mux.HandleFunc("/ratelimited", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(429) })
	mux.HandleFunc("/broken", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) })
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(200)
	})
	mux.HandleFunc("/ua", func(w http.ResponseWriter, r *http.Request) {
		if r.UserAgent() != checkUserAgent {
			w.WriteHeader(403)
			return
		}
		w.WriteHeader(200)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestCheckURLClasses(t *testing.T) {
	var hits atomic.Int32
	srv := newCheckServer(t, &hits)
	client := NewCheckClient(100 * time.Millisecond)
	cases := map[string]string{
		"/ok":              ClassOK,
		"/nohead":          ClassOK,
		"/redirect":        ClassOK,
		"/moved/deep/page": ClassHome,
		"/ua":              ClassOK,
		"/gone":            ClassDead,
		"/forbidden":       ClassBlocked,
		"/ratelimited":     ClassBlocked,
		"/broken":          ClassError,
		"/slow":            ClassError,
	}
	for path, want := range cases {
		r := CheckURL(context.Background(), client, srv.URL+path)
		if r.Class != want {
			t.Errorf("%s: class %q (status %d, err %q), want %q", path, r.Class, r.Status, r.Err, want)
		}
	}
}

func TestClassifyDNSNotFoundIsDead(t *testing.T) {
	err := &net.OpError{Op: "dial", Err: &net.DNSError{Err: "no such host", Name: "x.invalid", IsNotFound: true}}
	if r := classify("http://x.invalid", 0, err); r.Class != ClassDead {
		t.Errorf("NXDOMAIN should be dead, got %q", r.Class)
	}
	if r := classify("http://x", 0, errors.New("connection reset")); r.Class != ClassError || r.Err == "" {
		t.Errorf("other errors should be error with message, got %+v", r)
	}
}

func TestCheckURLBadURL(t *testing.T) {
	r := CheckURL(context.Background(), NewCheckClient(time.Second), "http://bad host/")
	if r.Class != ClassError || r.Err == "" {
		t.Errorf("malformed URL: %+v", r)
	}
}

func TestCheckAllUsesCacheAndDedupes(t *testing.T) {
	var hits atomic.Int32
	srv := newCheckServer(t, &hits)
	cached := srv.URL + "/ok?cached=1"
	cache := map[string]CheckResult{cached: {URL: cached, Class: ClassDead}}
	urls := []string{cached, srv.URL + "/ok", srv.URL + "/ok", srv.URL + "/gone"}

	got := CheckAll(context.Background(), NewCheckClient(time.Second), urls, cache, 4)
	if len(got) != 3 {
		t.Fatalf("want 3 results, got %d", len(got))
	}
	if got[cached].Class != ClassDead {
		t.Errorf("cached result must not be refetched: %+v", got[cached])
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("/ok fetched %d times, want 1 (deduped, HEAD succeeds)", n)
	}
	if cache[srv.URL+"/ok"].Class != "" {
		t.Error("input cache must not be mutated")
	}
	counts := ClassCounts(urls, got)
	if counts[ClassOK] != 2 || counts[ClassDead] != 2 {
		t.Errorf("ClassCounts per occurrence = %v", counts)
	}
}

func TestCacheRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "urlcheck.json")
	empty, err := LoadCache(path)
	if err != nil || len(empty) != 0 {
		t.Fatalf("missing cache: %v, %v", empty, err)
	}
	in := map[string]CheckResult{"https://a": {URL: "https://a", Status: 200, Class: ClassOK}}
	if err := SaveCache(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := LoadCache(path)
	if err != nil || out["https://a"].Status != 200 {
		t.Fatalf("round trip: %v, %v", out, err)
	}
}

func TestRedirectedHome(t *testing.T) {
	cases := []struct {
		raw, final string
		want       bool
	}{
		{"https://a.example/article/1", "https://a.example/", true},
		{"https://a.example/article/1", "https://a.example/?ref=x", false},
		{"https://a.example/article/1", "https://a.example/article/1/", false},
		{"https://a.example/", "https://a.example/", false},
		{"https://a.example/x", "::bad", false},
	}
	for _, c := range cases {
		if got := redirectedHome(c.raw, c.final); got != c.want {
			t.Errorf("redirectedHome(%q, %q) = %v, want %v", c.raw, c.final, got, c.want)
		}
	}
}
