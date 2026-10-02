package llm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/tools"
)

func TestClaude_Name(t *testing.T) {
	c := &Claude{}
	if got := c.Name(); got != "claude" {
		t.Errorf("Name() = %q, want %q", got, "claude")
	}
}

func TestClaude_Complete(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-claude")
	err := os.WriteFile(script, []byte("#!/bin/sh\necho 'test response'\n"), 0755)
	if err != nil {
		t.Fatalf("writing fake script: %v", err)
	}

	c := &Claude{Binary: script, Model: "test", MaxTokens: 100}
	got, err := c.Complete(context.Background(), Request{UserPrompt: "hello"})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if got != "test response" {
		t.Errorf("Complete() = %q, want %q", got, "test response")
	}
}

func TestClaude_Complete_WithSystemPrompt(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-claude")
	// Script that echoes all args so we can verify system prompt is passed
	err := os.WriteFile(script, []byte("#!/bin/sh\necho 'with system'\n"), 0755)
	if err != nil {
		t.Fatalf("writing fake script: %v", err)
	}

	c := &Claude{Binary: script, Model: "test", MaxTokens: 100}
	got, err := c.Complete(context.Background(), Request{
		SystemPrompt: "You are a helper",
		UserPrompt:   "hello",
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if got != "with system" {
		t.Errorf("Complete() = %q, want %q", got, "with system")
	}
}

func TestClaude_Complete_WithTools(t *testing.T) {
	dir := t.TempDir()
	// Script that prints args to stderr and response to stdout
	script := filepath.Join(dir, "fake-claude")
	err := os.WriteFile(script, []byte("#!/bin/sh\necho 'tool response'\n"), 0755)
	if err != nil {
		t.Fatalf("writing fake script: %v", err)
	}

	c := &Claude{Binary: script, Model: "test", MaxTokens: 100}
	got, err := c.Complete(context.Background(), Request{
		UserPrompt: "search for something",
		Tools: []tools.Tool{
			{Name: "web_search", Description: "Search the web"},
			{Name: "web_fetch", Description: "Fetch a URL"},
		},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if got != "tool response" {
		t.Errorf("Complete() = %q, want %q", got, "tool response")
	}
}

func TestClaude_Complete_WithMaxBudget(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-claude")
	// Script that echoes args so we can verify --max-budget-usd is passed
	err := os.WriteFile(script, []byte("#!/bin/sh\necho \"$@\"\n"), 0755)
	if err != nil {
		t.Fatalf("writing fake script: %v", err)
	}

	c := &Claude{Binary: script, Model: "test", MaxBudgetUSD: 1.50}
	got, err := c.Complete(context.Background(), Request{
		UserPrompt: "test",
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if !strings.Contains(got, "--max-budget-usd") {
		t.Errorf("output %q should contain --max-budget-usd", got)
	}
	if !strings.Contains(got, "1.50") {
		t.Errorf("output %q should contain budget value 1.50", got)
	}
}

func TestClaude_Complete_BinaryNotFound(t *testing.T) {
	c := &Claude{Binary: "/nonexistent/binary", Model: "test", MaxTokens: 100}
	_, err := c.Complete(context.Background(), Request{UserPrompt: "hello"})
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
}

// fakeClaudeRecorder writes a fake claude binary that records its argv (one
// per line), stdin, working directory and system prompt file into out.
func fakeClaudeRecorder(t *testing.T) (binary, out string) {
	t.Helper()
	dir := t.TempDir()
	out = filepath.Join(dir, "out")
	if err := os.Mkdir(out, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
out="` + out + `"
: > "$out/args"
prev=""
for a in "$@"; do
  printf '%s\n' "$a" >> "$out/args"
  if [ "$prev" = "--system-prompt-file" ]; then cp "$a" "$out/system"; fi
  prev="$a"
done
cat > "$out/stdin"
pwd -P > "$out/cwd"
ls -A > "$out/cwd-listing"
echo 'recorded response'
`
	binary = filepath.Join(dir, "fake-claude")
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return binary, out
}

func readRecorded(t *testing.T, out, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(out, name))
	if err != nil {
		t.Fatalf("reading recorded %s: %v", name, err)
	}
	return string(data)
}

func TestClaude_Complete_PromptOnStdinNotArgv(t *testing.T) {
	bin, out := fakeClaudeRecorder(t)
	// Past macOS's 1 MiB ARG_MAX: this failed with "argument list too long"
	// when the prompt was passed as the -p argument.
	prompt := strings.Repeat("evidence line\n", 150_000)
	c := &Claude{Binary: bin, Model: "opus"}
	got, err := c.Complete(context.Background(), Request{UserPrompt: prompt})
	if err != nil {
		t.Fatalf("Complete() with a %d-byte prompt: %v", len(prompt), err)
	}
	if got != "recorded response" {
		t.Errorf("Complete() = %q", got)
	}
	if stdin := readRecorded(t, out, "stdin"); stdin != prompt {
		t.Errorf("stdin had %d bytes, want the %d-byte prompt", len(stdin), len(prompt))
	}
	if args := readRecorded(t, out, "args"); strings.Contains(args, "evidence line") {
		t.Error("prompt text leaked into argv")
	}
}

func TestClaude_Complete_SystemPromptInFile(t *testing.T) {
	bin, out := fakeClaudeRecorder(t)
	system := "You are a helper.\n" + strings.Repeat("context ", 50_000)
	c := &Claude{Binary: bin, Model: "opus"}
	if _, err := c.Complete(context.Background(), Request{SystemPrompt: system, UserPrompt: "q"}); err != nil {
		t.Fatal(err)
	}
	if got := readRecorded(t, out, "system"); got != system {
		t.Errorf("system prompt file had %d bytes, want %d", len(got), len(system))
	}
	args := strings.Split(strings.TrimSpace(readRecorded(t, out, "args")), "\n")
	if argIndex(args, "--system-prompt") >= 0 {
		t.Error("--system-prompt passed inline; it should go through --system-prompt-file")
	}
	if strings.Contains(strings.Join(args, "\n"), "context context") {
		t.Error("system prompt text leaked into argv")
	}
}

func TestClaude_Complete_RunsInEmptyTempDirThatIsRemoved(t *testing.T) {
	bin, out := fakeClaudeRecorder(t)
	c := &Claude{Binary: bin, Model: "opus"}
	if _, err := c.Complete(context.Background(), Request{SystemPrompt: "s", UserPrompt: "q"}); err != nil {
		t.Fatal(err)
	}
	cwd := strings.TrimSpace(readRecorded(t, out, "cwd"))
	wd, _ := os.Getwd()
	if cwd == wd || !strings.Contains(filepath.Base(cwd), "researchguy-claude-") {
		t.Errorf("claude ran in %q, want a researchguy-claude- temp dir", cwd)
	}
	// Only the system prompt file researchguy itself wrote.
	if listing := strings.TrimSpace(readRecorded(t, out, "cwd-listing")); listing != "system-prompt.md" {
		t.Errorf("work dir contained %q, want only system-prompt.md", listing)
	}
	if _, err := os.Stat(cwd); !os.IsNotExist(err) {
		t.Errorf("work dir %s still exists after the call (stat err %v)", cwd, err)
	}
}

func TestClaude_Args_Isolation(t *testing.T) {
	tests := []struct {
		name      string
		ignore    bool
		tools     []tools.Tool
		wantTools string
		wantSafe  bool
		wantTurns bool
	}{
		{name: "no tools means no tools", ignore: true, wantTools: "", wantSafe: true},
		{name: "web tools only", ignore: true, tools: []tools.Tool{{Name: "web_search"}, {Name: "web_fetch"}, {Name: "unmapped"}}, wantTools: "WebSearch,WebFetch", wantSafe: true, wantTurns: true},
		{name: "user config kept when asked", ignore: false, wantTools: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &Claude{Model: "opus", IgnoreUserConfig: tc.ignore}
			args, err := c.args(t.TempDir(), Request{Tools: tc.tools})
			if err != nil {
				t.Fatal(err)
			}
			i := argIndex(args, "--tools")
			if i < 0 || i+1 >= len(args) || args[i+1] != tc.wantTools {
				t.Errorf("--tools value wrong in %q, want %q", args, tc.wantTools)
			}
			if got := argIndex(args, "--safe-mode") >= 0 && argIndex(args, "--strict-mcp-config") >= 0; got != tc.wantSafe {
				t.Errorf("safe mode flags present = %v, want %v: %q", got, tc.wantSafe, args)
			}
			if got := argIndex(args, "--max-turns") >= 0; got != tc.wantTurns {
				t.Errorf("--max-turns present = %v, want %v", got, tc.wantTurns)
			}
			if argIndex(args, "--no-session-persistence") < 0 {
				t.Errorf("missing --no-session-persistence: %q", args)
			}
			if args[0] != "-p" || argIndex(args, "--system-prompt-file") >= 0 {
				t.Errorf("args = %q, want -p first and no system prompt file for an empty system prompt", args)
			}
		})
	}
}

func TestClaude_Complete_FailureIncludesStderr(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-claude")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'prompt is too long' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := &Claude{Binary: script, Model: "opus"}
	_, err := c.Complete(context.Background(), Request{UserPrompt: "q"})
	if err == nil || !strings.Contains(err.Error(), "prompt is too long") {
		t.Errorf("err = %v, want the CLI's stderr included", err)
	}
}

func TestNewProvider_ClaudeCarriesIgnoreUserConfig(t *testing.T) {
	cfg := &config.Config{DefaultBackend: "claude", Claude: config.ClaudeConfig{Binary: "claude", Model: "opus", IgnoreUserConfig: true}}
	p, err := NewProvider(cfg, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if c, ok := p.(*Claude); !ok || !c.IgnoreUserConfig {
		t.Errorf("provider = %#v, want *Claude with IgnoreUserConfig", p)
	}
}

func argIndex(args []string, flag string) int {
	for i, a := range args {
		if a == flag {
			return i
		}
	}
	return -1
}

// The CLI reports API errors such as "Prompt is too long" on stdout with an
// empty stderr; the error has to carry them or a failed aggregation says
// nothing about why.
func TestClaude_Complete_FailureIncludesStdout(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-claude")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ncat > /dev/null\necho 'Prompt is too long'\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := &Claude{Binary: script, Model: "opus"}
	_, err := c.Complete(context.Background(), Request{UserPrompt: "q"})
	if err == nil || !strings.Contains(err.Error(), "Prompt is too long") {
		t.Errorf("err = %v, want the CLI's stdout included", err)
	}
}
