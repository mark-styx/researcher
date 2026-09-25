package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runCmdStdout runs the CLI and returns stdout and stderr separately, so
// tests can assert --json output is the only thing on stdout.
func runCmdStdout(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()
	os.Stdout, os.Stderr = outW, errW

	root := buildRoot()
	root.SetOut(outW)
	root.SetErr(errW)
	root.SetArgs(args)
	err = root.Execute()

	outW.Close()
	errW.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	var ob, eb bytes.Buffer
	ob.ReadFrom(outR)
	eb.ReadFrom(errR)
	return ob.String(), eb.String(), err
}

func TestDiveCmd_JSONAndOut(t *testing.T) {
	testSetupWithOllama(t, "Deep dive content here")
	out := filepath.Join(t.TempDir(), "sub", "x.md")

	stdout, stderr, err := runCmdStdout(t, "dive", "quantum computing", "--json", "--out", out, "--no-research")
	if err != nil {
		t.Fatalf("dive: %v\nstderr: %s", err, stderr)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not JSON: %q", stdout)
	}
	if got["saved_to"] != out || !strings.Contains(fmt.Sprint(got["report"]), "Deep dive content here") || got["backend"] != "ollama" {
		t.Fatalf("got %v", got)
	}
	data, err := os.ReadFile(out)
	if err != nil || !strings.Contains(string(data), "Deep dive content here") {
		t.Fatalf("report not written to --out: %v", err)
	}
}

func TestResearchCmds_JSONKeys(t *testing.T) {
	for _, tc := range []struct {
		args []string
		key  string
	}{
		{[]string{"ask", "q", "--json", "--no-save", "--no-research"}, "answer"},
		{[]string{"review", "t", "--json", "--no-research", "--out", "OUT"}, "review"},
		{[]string{"compare", "a", "b", "--json", "--no-research", "--out", "OUT"}, "comparison"},
	} {
		t.Run(tc.args[0], func(t *testing.T) {
			testSetupWithOllama(t, "body text")
			args := append([]string{}, tc.args...)
			for i, a := range args {
				if a == "OUT" {
					args[i] = filepath.Join(t.TempDir(), "o.md")
				}
			}
			stdout, stderr, err := runCmdStdout(t, args...)
			if err != nil {
				t.Fatalf("%v\nstderr: %s", err, stderr)
			}
			var got map[string]any
			if err := json.Unmarshal([]byte(stdout), &got); err != nil {
				t.Fatalf("stdout is not JSON: %q", stdout)
			}
			if !strings.Contains(fmt.Sprint(got[tc.key]), "body text") {
				t.Fatalf("missing %q: %v", tc.key, got)
			}
		})
	}
}

// writeGrepaiJSON rewrites the test config's grepai binary to a script that
// records its args and prints results.
func writeGrepaiJSON(t *testing.T, configDir, researchDir, extra, results string) string {
	t.Helper()
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	script := filepath.Join(dir, "fake-grepai")
	body := "#!/bin/sh\necho \"$@\" >> " + argsFile + "\ncase \"$*\" in *\"--project book\"*) echo '[]'; exit 0;; esac\ncat <<'JSONEOF'\n" + results + "\nJSONEOF\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := fmt.Sprintf("research_dir: %s\ndefault_backend: ollama\nollama:\n  host: http://127.0.0.1:0\n  model: test\ngrepai:\n  binary: %s\n%s", researchDir, script, extra)
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	return argsFile
}

func TestSearchCmd_JSONProjects(t *testing.T) {
	configDir, researchDir := testSetup(t)
	if err := os.MkdirAll(filepath.Join(researchDir, "cat"), 0o755); err != nil {
		t.Fatal(err)
	}
	argsFile := writeGrepaiJSON(t, configDir, researchDir, "  workspace: ws\n  project: research\n",
		`[{"file_path":"ws/research/cat/a.md","start_line":1,"end_line":2,"score":0.5,"content":"x"}]`)

	stdout, stderr, err := runCmdStdout(t, "search", "q", "--json", "--project", "research", "--project", "book")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	var hits []map[string]any
	if err := json.Unmarshal([]byte(stdout), &hits); err != nil {
		t.Fatalf("not JSON: %q", stdout)
	}
	if len(hits) != 1 || hits[0]["project"] != "research" || hits[0]["path"] != filepath.Join(researchDir, "cat", "a.md") {
		t.Fatalf("hits = %v", hits)
	}
	args, _ := os.ReadFile(argsFile)
	if !strings.Contains(string(args), "--json --workspace ws --project research\n") || !strings.Contains(string(args), "--json --workspace ws --project book\n") {
		t.Fatalf("want one grepai call per project, got:\n%s", args)
	}

	if _, _, err := runCmdStdout(t, "search", "q", "--max-age", "30d"); err == nil {
		t.Fatal("--max-age without --json should error")
	}
}

func TestContextCmd(t *testing.T) {
	configDir, researchDir := testSetup(t)
	if err := os.MkdirAll(filepath.Join(researchDir, "cat"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(researchDir, "cat", "a.md"), []byte("stored research"), 0o644); err != nil {
		t.Fatal(err)
	}
	argsFile := writeGrepaiJSON(t, configDir, researchDir, "",
		`[{"file_path":"cat/a.md","start_line":1,"end_line":1,"score":0.9,"content":"stored research"}]`)

	stdout, stderr, err := runCmdStdout(t, "context", "topic", "--json", "--max-age", "none")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("not JSON: %q", stdout)
	}
	if got["count"] != float64(1) || !strings.Contains(fmt.Sprint(got["context"]), "stored research") {
		t.Fatalf("got %v", got)
	}
	if args, _ := os.ReadFile(argsFile); !strings.Contains(string(args), "search topic") {
		t.Fatalf("args = %s", args)
	}

	// Text mode lists sources then the context.
	stdout, _, err = runCmdStdout(t, "context", "topic", "--max-age", "none")
	if err != nil || !strings.Contains(stdout, "cat/a.md") || !strings.Contains(stdout, "--- Source: cat/a.md ---") {
		t.Fatalf("text output: %q, %v", stdout, err)
	}

	if _, _, err := runCmdStdout(t, "context", "topic", "--max-age", "5x"); err == nil {
		t.Fatal("bad max-age should error")
	}
}
