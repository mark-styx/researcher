package graph

import (
	"strings"
	"testing"
)

func TestLinkReport(t *testing.T) {
	s := newTestStore(t)
	mk := func(typ, title, path string) *Node {
		t.Helper()
		n := &Node{Type: typ, Title: title, Path: path}
		if err := s.CreateNode(n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	lead := mk("lead", "Forum rumor", "deep-research/run1/r0-a.md")
	claim := mk("claim", "Funding came via Farfield", "deep-research/run1/r1-b.md")
	mk("claim", "Other run", "deep-research/run2/r0-a.md")
	mk("entity", "Thin node", "")

	res, err := s.LinkReport("CCF funding", "CIA funded the CCF", "deep-research/run1/report.md", "deep-research/run1/")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Created || res.Linked != 2 || res.AlreadyLinked != 0 {
		t.Fatalf("first run = %+v", res)
	}
	report, err := s.GetNode(res.ReportID)
	if err != nil || report.Type != "report" || report.Title != "CCF funding" || report.Summary != "CIA funded the CCF" {
		t.Fatalf("report node = %+v, %v", report, err)
	}
	edges, _ := s.ListEdges(EdgeFilter{FromID: res.ReportID, Type: "references"})
	got := map[string]bool{}
	for _, e := range edges {
		got[e.ToID] = true
	}
	if len(edges) != 2 || !got[lead.ID] || !got[claim.ID] {
		t.Fatalf("edges = %v, want lead and claim from run1 only", got)
	}

	// A rerun (resume, retry) reuses the node and adds nothing.
	again, err := s.LinkReport("ignored", "ignored", "deep-research/run1/report.md", "deep-research/run1/")
	if err != nil {
		t.Fatal(err)
	}
	if again.Created || again.ReportID != res.ReportID || again.Linked != 0 || again.AlreadyLinked != 2 {
		t.Fatalf("rerun = %+v", again)
	}

	// A node registered after the first link gets picked up.
	late := mk("source", "Late source", "deep-research/run1/r1-c.md")
	third, _ := s.LinkReport("", "", "deep-research/run1/report.md", "deep-research/run1/")
	if third.Linked != 1 || third.AlreadyLinked != 2 {
		t.Fatalf("third = %+v", third)
	}
	if edges, _ := s.ListEdges(EdgeFilter{FromID: res.ReportID, ToID: late.ID}); len(edges) != 1 {
		t.Error("late node not linked")
	}
}

func TestLinkReportRequiresPathAndPrefix(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.LinkReport("t", "", "", "x/"); err == nil || !strings.Contains(err.Error(), "report path") {
		t.Errorf("empty path: %v", err)
	}
	if _, err := s.LinkReport("t", "", "x/report.md", " "); err == nil || !strings.Contains(err.Error(), "prefix") {
		t.Errorf("empty prefix: %v", err)
	}
	if _, err := s.LinkReport("", "", "x/report.md", "x/"); err == nil {
		t.Error("a new report node without a title should fail")
	}
}
