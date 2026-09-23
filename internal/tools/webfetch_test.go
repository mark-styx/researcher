package tools

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebFetch_HTML(t *testing.T) {
	t.Setenv("RESEARCHGUY_ALLOW_PRIVATE_URLS", "true")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body><p>Hello</p></body></html>`)
	}))
	defer srv.Close()

	content, err := WebFetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(content, "Hello") {
		t.Errorf("content = %q, expected to contain 'Hello'", content)
	}
}

func TestWebFetch_PlainText(t *testing.T) {
	t.Setenv("RESEARCHGUY_ALLOW_PRIVATE_URLS", "true")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "raw text content")
	}))
	defer srv.Close()

	content, err := WebFetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content != "raw text content" {
		t.Errorf("content = %q, want %q", content, "raw text content")
	}
}

func TestWebFetch_HTTPError(t *testing.T) {
	t.Setenv("RESEARCHGUY_ALLOW_PRIVATE_URLS", "true")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := WebFetch(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected error for 404")
	}
	if !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("error = %q, expected to contain 'HTTP 404'", err.Error())
	}
}

func TestWebFetch_SkipsTags(t *testing.T) {
	t.Setenv("RESEARCHGUY_ALLOW_PRIVATE_URLS", "true")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body>
			<script>var x = 1;</script>
			<style>.foo{color:red}</style>
			<nav><a href="/">Home</a></nav>
			<p>Visible content here</p>
		</body></html>`)
	}))
	defer srv.Close()

	content, err := WebFetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(content, "Visible content here") {
		t.Errorf("missing visible content in: %q", content)
	}
	if strings.Contains(content, "var x = 1") {
		t.Error("script content should be excluded")
	}
	if strings.Contains(content, "color:red") {
		t.Error("style content should be excluded")
	}
	if strings.Contains(content, "Home") {
		t.Error("nav content should be excluded")
	}
}

func TestExtractText(t *testing.T) {
	input := `<html><body>
		<p>First paragraph</p>
		<div>A <b>bold</b> word</div>
		<script>ignore me</script>
		<style>.x{}</style>
		<p>Last paragraph</p>
	</body></html>`

	got, err := extractText(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(got, "First paragraph") {
		t.Error("missing 'First paragraph'")
	}
	if !strings.Contains(got, "bold") {
		t.Error("missing 'bold'")
	}
	if !strings.Contains(got, "Last paragraph") {
		t.Error("missing 'Last paragraph'")
	}
	if strings.Contains(got, "ignore me") {
		t.Error("script content should be excluded")
	}

	// Verify no excessive blank lines (max 1 consecutive)
	if strings.Contains(got, "\n\n\n") {
		t.Error("excessive blank lines not collapsed")
	}
}

func TestWebFetch_RejectsLocalhostByDefault(t *testing.T) {
	_, err := WebFetch(context.Background(), "http://127.0.0.1:8080")
	if err == nil {
		t.Fatal("expected localhost/private URL rejection")
	}
	if !strings.Contains(err.Error(), "private or local network") && !strings.Contains(err.Error(), "localhost") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWebFetch_RejectsUnsupportedScheme(t *testing.T) {
	_, err := WebFetch(context.Background(), "file:///etc/passwd")
	if err == nil {
		t.Fatal("expected scheme rejection")
	}
	if !strings.Contains(err.Error(), "only http/https allowed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestStripTags(t *testing.T) {
	input := "<p>hello</p> <b>world</b>"
	got := stripTags(input)
	if !strings.Contains(got, "hello") {
		t.Error("missing 'hello'")
	}
	if !strings.Contains(got, "world") {
		t.Error("missing 'world'")
	}
	if strings.Contains(got, "<") || strings.Contains(got, ">") {
		t.Errorf("tags not stripped: %q", got)
	}
}

func TestTruncate(t *testing.T) {
	t.Run("short string unchanged", func(t *testing.T) {
		got := truncate("hello", 100)
		if got != "hello" {
			t.Errorf("got %q, want %q", got, "hello")
		}
	})

	t.Run("long string truncated", func(t *testing.T) {
		long := strings.Repeat("a", 200)
		got := truncate(long, 50)
		if len(got) > 70 { // 50 + marker
			t.Errorf("truncated length = %d, expected ~60-65", len(got))
		}
		if !strings.Contains(got, "[truncated]") {
			t.Error("missing truncation marker")
		}
		if !strings.HasPrefix(got, strings.Repeat("a", 50)) {
			t.Error("truncated content doesn't start with expected prefix")
		}
	})
}
