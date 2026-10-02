package fetch

import "testing"

func TestParseDate(t *testing.T) {
	cases := []struct {
		in, date, prec string
		ok             bool
	}{
		{"2025-03-14", "2025-03-14", "day", true},
		{"2025/3/4", "2025-03-04", "day", true},
		{"2025-03-14T23:30:00-08:00", "2025-03-14", "day", true},
		{"2025-03-14 10:00", "2025-03-14", "day", true},
		{"2025-03", "2025-03", "month", true},
		{"1998", "1998", "year", true},
		{"March 3, 2021", "2021-03-03", "day", true},
		{"3 Mar 2021", "2021-03-03", "day", true},
		{"2019 May 1", "2019-05-01", "day", true},
		{"2019 May", "2019-05", "month", true},
		{"January 2020", "2020-01", "month", true},
		{"Tue, 15 Nov 1994 08:12:31 GMT", "1994-11-15", "day", true},
		{"2025-02-30", "", "", false},
		{"2025-13-01", "", "", false},
		{"2027-01-01", "", "", false}, // after testNow
		{"0999", "", "", false},
		{"yesterday", "", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		d, p, ok := parseDate(c.in, testNow)
		if d != c.date || p != c.prec || ok != c.ok {
			t.Errorf("parseDate(%q) = %q, %q, %v; want %q, %q, %v", c.in, d, p, ok, c.date, c.prec, c.ok)
		}
	}
}

func TestPublished_WeakFlag(t *testing.T) {
	p := published("Tue, 15 Nov 1994 08:12:31 GMT", "last-modified", true, testNow)
	if p == nil || !p.Weak || p.From != "last-modified" {
		t.Errorf("published = %+v", p)
	}
}

func TestURLPathDate(t *testing.T) {
	for u, want := range map[string]string{
		"https://n.example/2024/03/15/story": "2024-03-15",
		"https://n.example/2024/03/story":    "2024-03",
		"https://n.example/2024/13/01/story": "",
		"https://n.example/id/123456/789":    "",
		"https://n.example/story":            "",
	} {
		p := urlPathDate(u, testNow)
		got := ""
		if p != nil {
			got = p.Date
		}
		if got != want {
			t.Errorf("urlPathDate(%q) = %q, want %q", u, got, want)
		}
	}
}
