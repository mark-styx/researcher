package fetch

import (
	"reflect"
	"testing"
)

func TestExtractURLs(t *testing.T) {
	text := `See [the study](https://example.org/a/b?x=1). Also https://en.wikipedia.org/wiki/Foo_(bar), and "https://q.example/z".
Bare http://plain.example/p; and https://x.co/ ok.`
	want := []string{
		"https://example.org/a/b?x=1",
		"https://en.wikipedia.org/wiki/Foo_(bar)",
		"https://q.example/z",
		"http://plain.example/p",
		"https://x.co/",
	}
	if got := ExtractURLs(text); !reflect.DeepEqual(got, want) {
		t.Errorf("ExtractURLs =\n%q\nwant\n%q", got, want)
	}
}

func TestDOIFromURL(t *testing.T) {
	cases := map[string]string{
		"https://doi.org/10.1177/0956797620904990":                                  "10.1177/0956797620904990",
		"https://dx.doi.org/10.1037/A0012345.":                                      "10.1037/a0012345",
		"https://journals.sagepub.com/doi/full/10.1177/0956797620904990":            "10.1177/0956797620904990",
		"https://onlinelibrary.wiley.com/doi/abs/10.1002/ejsp.2420":                 "10.1002/ejsp.2420",
		"https://www.tandfonline.com/doi/pdf/10.1080/13546783.2020.1847":            "10.1080/13546783.2020.1847",
		"https://journals.plos.org/plosone/article?id=10.1371/journal.pone.0217744": "10.1371/journal.pone.0217744",
		"https://www.nature.com/articles/s41586-020-2649-2":                         "",
		"https://example.org/10.1234/not-a-doi-path":                                "",
		"not a url": "",
	}
	for in, want := range cases {
		if got := DOIFromURL(in); got != want {
			t.Errorf("DOIFromURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCleanDOI(t *testing.T) {
	cases := map[string]string{
		"doi:10.1234/ABC":            "10.1234/abc",
		"https://doi.org/10.1/x":     "",
		"https://doi.org/10.1234/x.": "10.1234/x",
		" 10.5555/12345678 ":         "10.5555/12345678",
		"ISBN 978-0":                 "",
		"":                           "",
	}
	for in, want := range cases {
		if got := CleanDOI(in); got != want {
			t.Errorf("CleanDOI(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFetchable(t *testing.T) {
	for u, want := range map[string]bool{
		"https://example.org/paper.pdf":   true,
		"https://example.org/page":        true,
		"https://example.org/img/fig.PNG": false,
		"https://example.org/v.mp4":       false,
		"ftp://example.org/x":             false,
		"https:///nohost":                 false,
	} {
		if got := fetchable(u); got != want {
			t.Errorf("fetchable(%q) = %v", u, got)
		}
	}
}
