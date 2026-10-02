package fetch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testClient() *Client {
	c := NewClient(5 * time.Second)
	c.AllowPrivate = true
	c.Backoff = 10 * time.Millisecond
	c.HostInterval = 0
	return c
}

func TestClient_RetriesTransientStatus(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if !strings.Contains(r.UserAgent(), "researchguy") {
			t.Errorf("User-Agent = %q", r.UserAgent())
		}
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Last-Modified", "Tue, 15 Nov 1994 08:12:31 GMT")
		w.Write([]byte("<p>ok</p>"))
	}))
	defer srv.Close()
	resp, err := testClient().Get(context.Background(), srv.URL+"/page")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != 200 || resp.Attempts != 3 || string(resp.Body) != "<p>ok</p>" || resp.LastModified == "" {
		t.Errorf("resp = %+v", resp)
	}
}

func TestClient_DoesNotRetryForbidden(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte("go away"))
	}))
	defer srv.Close()
	resp, err := testClient().Get(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != 403 || resp.Attempts != 1 || calls.Load() != 1 || resp.Body != nil {
		t.Errorf("resp = %+v after %d calls", resp, calls.Load())
	}
}

func TestClient_FollowsRedirectsAndRecordsFinalURL(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/old" {
			http.Redirect(w, r, srv.URL+"/new", http.StatusMovedPermanently)
			return
		}
		w.Write([]byte("new page"))
	}))
	defer srv.Close()
	resp, err := testClient().Get(context.Background(), srv.URL+"/old")
	if err != nil || resp.FinalURL != srv.URL+"/new" {
		t.Fatalf("resp = %+v, %v", resp, err)
	}
}

func TestClient_RefusesPrivateAddresses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("request reached the server")
	}))
	defer srv.Close()
	c := testClient()
	c.AllowPrivate = false
	c.Retries = 0
	_, err := c.Get(context.Background(), srv.URL)
	if err == nil || !strings.Contains(err.Error(), "private or local") {
		t.Fatalf("err = %v, want a refusal", err)
	}
	var ae *AttemptError
	if !errors.As(err, &ae) || ae.Attempts != 1 {
		t.Errorf("err = %#v, want an AttemptError after 1 attempt", err)
	}
}

func TestClient_RejectsNonHTTP(t *testing.T) {
	if _, err := testClient().Get(context.Background(), "file:///etc/passwd"); err == nil {
		t.Error("file URL fetched")
	}
}

func TestClient_TruncatesAtMaxBytes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.Repeat("x", 1000)))
	}))
	defer srv.Close()
	c := testClient()
	c.MaxBytes = 100
	resp, err := c.Get(context.Background(), srv.URL)
	if err != nil || len(resp.Body) != 100 || !resp.Truncated {
		t.Fatalf("resp = %d bytes, truncated=%v, %v", len(resp.Body), resp.Truncated, err)
	}
}

func TestClient_SpacesRequestsPerHost(t *testing.T) {
	var mu sync.Mutex
	var times []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		times = append(times, time.Now())
		mu.Unlock()
	}))
	defer srv.Close()
	c := testClient()
	c.HostInterval = 80 * time.Millisecond
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Get(context.Background(), srv.URL)
		}()
	}
	wg.Wait()
	if len(times) != 3 {
		t.Fatalf("%d requests", len(times))
	}
	first, last := times[0], times[0]
	for _, tm := range times {
		if tm.Before(first) {
			first = tm
		}
		if tm.After(last) {
			last = tm
		}
	}
	if gap := last.Sub(first); gap < 150*time.Millisecond {
		t.Errorf("3 requests to one host spanned %v, want >= 2 intervals", gap)
	}
}

func TestClient_StopsOnCanceledContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	c := testClient()
	c.Backoff = time.Hour
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	resp, _ := c.Get(ctx, srv.URL)
	if time.Since(start) > 2*time.Second {
		t.Error("Get waited out its backoff after the context ended")
	}
	if resp != nil && resp.Attempts > 1 {
		t.Errorf("retried after cancel: %+v", resp)
	}
}
