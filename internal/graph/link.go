package graph

import (
	"fmt"
	"strings"
)

// LinkReportResult reports what LinkReport created.
type LinkReportResult struct {
	ReportID      string `json:"report_id"`
	Created       bool   `json:"created"`
	Linked        int    `json:"linked"`
	AlreadyLinked int    `json:"already_linked"`
}

// LinkReport creates the report node for reportPath, or reuses the one
// already there, and adds a references edge from it to every other node
// whose path starts with prefix. Existing edges are kept, so a rerun adds
// nothing.
func (s *Store) LinkReport(title, summary, reportPath, prefix string) (LinkReportResult, error) {
	var res LinkReportResult
	if strings.TrimSpace(reportPath) == "" {
		return res, fmt.Errorf("report path is required")
	}
	if strings.TrimSpace(prefix) == "" {
		return res, fmt.Errorf("prefix is required (an empty prefix would link every node)")
	}

	nodes, err := s.ListNodes("")
	if err != nil {
		return res, fmt.Errorf("listing nodes: %w", err)
	}
	var report *Node
	for _, n := range nodes {
		if n.Type == "report" && n.Path == reportPath {
			report = n
			break
		}
	}
	if report == nil {
		report = &Node{Type: "report", Title: title, Path: reportPath, Summary: summary}
		if err := s.CreateNode(report); err != nil {
			return res, fmt.Errorf("creating report node: %w", err)
		}
		res.Created = true
	}
	res.ReportID = report.ID

	existing, err := s.ListEdges(EdgeFilter{FromID: report.ID, Type: "references"})
	if err != nil {
		return res, fmt.Errorf("listing report edges: %w", err)
	}
	linked := make(map[string]bool, len(existing))
	for _, e := range existing {
		linked[e.ToID] = true
	}
	for _, n := range nodes {
		if n.ID == report.ID || !strings.HasPrefix(n.Path, prefix) {
			continue
		}
		if linked[n.ID] {
			res.AlreadyLinked++
			continue
		}
		if err := s.CreateEdge(&Edge{FromID: report.ID, ToID: n.ID, Type: "references"}); err != nil {
			return res, fmt.Errorf("linking %s: %w", n.ID, err)
		}
		linked[n.ID] = true
		res.Linked++
	}
	return res, nil
}
