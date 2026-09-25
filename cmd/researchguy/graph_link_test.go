package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGraphLinkReportCmd(t *testing.T) {
	_, researchDir := testSetup(t)
	runDir := filepath.Join(researchDir, "deep-research", "run1")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "report.md"), []byte("# Report\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"--type", "lead", "--title", "Forum rumor", "--path", "deep-research/run1/r0-a.md"},
		{"--type", "claim", "--title", "Farfield grants", "--path", "deep-research/run1/r1-b.md"},
		{"--type", "claim", "--title", "Unrelated", "--path", "deep-research/run2/r0-a.md"},
	} {
		if _, stderr, err := runCmdStdout(t, append([]string{"graph", "add-node"}, args...)...); err != nil {
			t.Fatalf("add-node %v: %v\n%s", args, err, stderr)
		}
	}

	stdout, stderr, err := runCmdStdout(t, "graph", "link-report", "--path", "deep-research/run1/report.md",
		"--title", "CCF funding", "--summary", "CIA money via foundations", "--json")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("not JSON: %q", stdout)
	}
	if res["created"] != true || res["linked"] != float64(2) || res["already_linked"] != float64(0) {
		t.Fatalf("result = %v (default prefix is the report's directory)", res)
	}

	stdout, _, err = runCmdStdout(t, "graph", "link-report", "--path", "./deep-research/run1/report.md", "--title", "x")
	if err != nil || !strings.Contains(stdout, "(reused): linked 0, already linked 2") {
		t.Fatalf("rerun: %q, %v", stdout, err)
	}
}

func TestGraphLinkReportCmdErrors(t *testing.T) {
	testSetup(t)
	cases := map[string][]string{
		"missing file":  {"--path", "deep-research/none/report.md", "--title", "t"},
		"absolute path": {"--path", "/etc/passwd", "--title", "t"},
		"escapes root":  {"--path", "../outside.md", "--title", "t"},
		"no title":      {"--path", "x.md"},
	}
	for name, args := range cases {
		if _, _, err := runCmdStdout(t, append([]string{"graph", "link-report"}, args...)...); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}
