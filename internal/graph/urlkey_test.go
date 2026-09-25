package graph

import "testing"

func TestNormalizeURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://en.wikipedia.org/wiki/Project_MKUltra", "en.wikipedia.org/wiki/Project_MKUltra"},
		{"http://en.wikipedia.org/wiki/Project_MKUltra", "en.wikipedia.org/wiki/Project_MKUltra"},
		{"HTTPS://EN.Wikipedia.ORG/wiki/Project_MKUltra#History", "en.wikipedia.org/wiki/Project_MKUltra"},
		{"https://www.cia.gov/readingroom/", "cia.gov/readingroom"},
		{"https://cia.gov:443/readingroom", "cia.gov/readingroom"},
		{"http://example.com:80/", "example.com"},
		{"https://example.com:8080/x", "example.com:8080/x"},
		{"https://example.com/a?utm_source=x&b=2&a=1&fbclid=z&gclid=q", "example.com/a?a=1&b=2"},
		{"https://example.com/a?utm_medium=email", "example.com/a"},
		{"  https://example.com/Path/Case  ", "example.com/Path/Case"},
		{"https://example.com/search?q=a+b", "example.com/search?q=a+b"},
		{"https://example.com/%7Euser/", "example.com/~user"},
		{"https://example.com/a%2Fb", "example.com/a%2Fb"},
		{"https://example.com/caf%C3%A9", "example.com/caf%C3%A9"},
	}
	for _, tc := range cases {
		got, err := NormalizeURL(tc.in)
		if err != nil {
			t.Errorf("NormalizeURL(%q) error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("NormalizeURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeURLRejects(t *testing.T) {
	for _, in := range []string{"", "   ", "not a url", "ftp://example.com/x", "mailto:a@b.c", "https://", "/relative/path", "https://exa mple.com/"} {
		if got, err := NormalizeURL(in); err == nil {
			t.Errorf("NormalizeURL(%q) = %q, want error", in, got)
		}
	}
}

func TestKeys(t *testing.T) {
	k, err := URLKey("http://www.example.com/x/")
	if err != nil || k != "url:example.com/x" {
		t.Fatalf("URLKey = %q, %v", k, err)
	}
	if BookKey("the_poisoned_well") != "book:the_poisoned_well" {
		t.Fatal("BookKey")
	}
}
