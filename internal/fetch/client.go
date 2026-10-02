// Package fetch gets the documents behind a run's evidence: the pages
// workers opened, the URLs drafts and reports cite, and the top results of
// each search. It stores the raw bytes and the extracted text in the store
// and records every attempt, failed or not, in the run's fetches.jsonl.
// See "Fetch" in docs/research-store-design.md.
package fetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// DefaultUserAgent reads as a browser, since many publishers refuse
// anything else, and still names researchguy.
const DefaultUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36 researchguy/1.0"

// Client is an HTTP client for fetching documents politely: per-host
// spacing and concurrency, retries with backoff on transient failures, a
// size cap, and no requests to private or local addresses (redirects
// included) unless AllowPrivate is set.
type Client struct {
	UserAgent string
	Timeout   time.Duration // per request
	MaxBytes  int64
	Retries   int           // retries after the first try
	Backoff   time.Duration // wait before the first retry; doubles
	// HostInterval spaces requests to one host; HostConcurrency caps how
	// many run at once.
	HostInterval    time.Duration
	HostConcurrency int
	AllowPrivate    bool

	once  sync.Once
	http  *http.Client
	hosts hostLimiter
}

// NewClient returns a client with the defaults the fetch stage uses.
// RESEARCHGUY_ALLOW_PRIVATE_URLS=true allows private addresses, as it does
// for the web_fetch tool.
func NewClient(timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	return &Client{
		UserAgent:       DefaultUserAgent,
		Timeout:         timeout,
		MaxBytes:        25 << 20,
		Retries:         2,
		Backoff:         time.Second,
		HostInterval:    250 * time.Millisecond,
		HostConcurrency: 2,
		AllowPrivate:    strings.EqualFold(os.Getenv("RESEARCHGUY_ALLOW_PRIVATE_URLS"), "true"),
	}
}

// Response is a completed request. Status is set for any HTTP response,
// including errors; Body only for 2xx.
type Response struct {
	FinalURL     string
	Status       int
	ContentType  string
	LastModified string
	Body         []byte
	Truncated    bool // the body was cut at MaxBytes
	Attempts     int
}

func (c *Client) init() {
	c.once.Do(func() {
		dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
		if !c.AllowPrivate {
			// Checked on the address actually dialed, so a redirect or a
			// DNS answer that changed since a check can't reach the LAN.
			dialer.Control = func(network, address string, _ syscall.RawConn) error {
				host, _, err := net.SplitHostPort(address)
				if err != nil {
					return err
				}
				if ip := net.ParseIP(host); ip == nil || privateIP(ip) {
					return fmt.Errorf("refusing private or local address %s", host)
				}
				return nil
			}
		}
		c.http = &http.Client{
			Transport: &http.Transport{
				DialContext:           dialer.DialContext,
				ForceAttemptHTTP2:     true,
				MaxIdleConns:          64,
				IdleConnTimeout:       60 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: c.Timeout,
			},
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return errors.New("stopped after 10 redirects")
				}
				return nil
			},
		}
		c.hosts = hostLimiter{interval: c.HostInterval, conc: max(c.HostConcurrency, 1), hosts: map[string]*hostState{}}
	})
}

func privateIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast()
}

// Get fetches raw, retrying network errors, 429 and 5xx. An HTTP error
// status is a Response, not an error; err is for requests that got no
// response.
func (c *Client) Get(ctx context.Context, raw string) (*Response, error) {
	c.init()
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return nil, fmt.Errorf("not an http(s) URL: %q", raw)
	}
	var resp *Response
	wait := c.Backoff
	attempt := 1
	for ; ; attempt++ {
		var retryAfter time.Duration
		resp, retryAfter, err = c.try(ctx, u)
		if resp != nil {
			resp.Attempts = attempt
		}
		if attempt > c.Retries || ctx.Err() != nil || !transient(resp, err) {
			break
		}
		d := max(wait, min(retryAfter, 10*time.Second))
		select {
		case <-time.After(d):
		case <-ctx.Done():
		}
		wait *= 2
	}
	if err != nil {
		return nil, &AttemptError{Err: err, Attempts: attempt}
	}
	return resp, nil
}

// AttemptError is a request that got no response, after its retries.
type AttemptError struct {
	Err      error
	Attempts int
}

func (e *AttemptError) Error() string { return e.Err.Error() }
func (e *AttemptError) Unwrap() error { return e.Err }

func transient(resp *Response, err error) bool {
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return true
		}
		return errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) || strings.Contains(err.Error(), "connection reset")
	}
	switch resp.Status {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

func (c *Client) try(ctx context.Context, u *url.URL) (*Response, time.Duration, error) {
	release, err := c.hosts.acquire(ctx, strings.ToLower(u.Hostname()))
	if err != nil {
		return nil, 0, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/pdf;q=0.9,text/plain;q=0.8,*/*;q=0.5")
	req.Header.Set("Accept-Language", "en-US,en;q=0.8")
	hr, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer hr.Body.Close()
	resp := &Response{
		FinalURL:     hr.Request.URL.String(),
		Status:       hr.StatusCode,
		ContentType:  hr.Header.Get("Content-Type"),
		LastModified: hr.Header.Get("Last-Modified"),
	}
	if hr.StatusCode < 200 || hr.StatusCode > 299 {
		io.Copy(io.Discard, io.LimitReader(hr.Body, 64<<10))
		return resp, retryAfter(hr.Header.Get("Retry-After")), nil
	}
	body, err := io.ReadAll(io.LimitReader(hr.Body, c.MaxBytes+1))
	if err != nil {
		return nil, 0, fmt.Errorf("reading body: %w", err)
	}
	if int64(len(body)) > c.MaxBytes {
		body, resp.Truncated = body[:c.MaxBytes], true
	}
	resp.Body = body
	return resp, 0, nil
}

func retryAfter(h string) time.Duration {
	if n, err := strconv.Atoi(strings.TrimSpace(h)); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	return 0
}

// hostLimiter spaces and caps requests per host.
type hostLimiter struct {
	interval time.Duration
	conc     int
	mu       sync.Mutex
	hosts    map[string]*hostState
}

type hostState struct {
	slots chan struct{}
	next  time.Time
}

func (h *hostLimiter) acquire(ctx context.Context, host string) (func(), error) {
	h.mu.Lock()
	st, ok := h.hosts[host]
	if !ok {
		st = &hostState{slots: make(chan struct{}, h.conc)}
		h.hosts[host] = st
	}
	h.mu.Unlock()
	select {
	case st.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	h.mu.Lock()
	now := time.Now()
	start := now
	if st.next.After(now) {
		start = st.next
	}
	st.next = start.Add(h.interval)
	h.mu.Unlock()
	if d := time.Until(start); d > 0 {
		select {
		case <-time.After(d):
		case <-ctx.Done():
			<-st.slots
			return nil, ctx.Err()
		}
	}
	return func() { <-st.slots }, nil
}
