package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestExpandPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("cannot get home dir: %v", err)
	}

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "tilde prefix",
			input: "~/foo",
			want:  filepath.Join(home, "foo"),
		},
		{
			name:  "absolute path unchanged",
			input: "/usr/local/bin",
			want:  "/usr/local/bin",
		},
		{
			name:  "relative path unchanged",
			input: "relative/path",
			want:  "relative/path",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ExpandPath(tc.input)
			if got != tc.want {
				t.Errorf("ExpandPath(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestDefaultYAML_Parses(t *testing.T) {
	var cfg Config
	if err := yaml.Unmarshal([]byte(DefaultYAML), &cfg); err != nil {
		t.Fatalf("failed to unmarshal DefaultYAML: %v", err)
	}

	if !cfg.Tools.Enabled {
		t.Error("expected Tools.Enabled = true")
	}
	if cfg.Claude.Model != "opus" {
		t.Errorf("Claude.Model = %q, want %q", cfg.Claude.Model, "opus")
	}
	if cfg.DefaultBackend != "claude" {
		t.Errorf("DefaultBackend = %q, want %q", cfg.DefaultBackend, "claude")
	}
	if cfg.Ollama.Host != "http://localhost:11434" {
		t.Errorf("Ollama.Host = %q, want %q", cfg.Ollama.Host, "http://localhost:11434")
	}
	if cfg.Ollama.Model != "glm-4.7-flash" {
		t.Errorf("Ollama.Model = %q, want %q", cfg.Ollama.Model, "glm-4.7-flash")
	}
	if cfg.Ollama.UtilityModel != "qwen3.5:9b" {
		t.Errorf("Ollama.UtilityModel = %q, want %q", cfg.Ollama.UtilityModel, "qwen3.5:9b")
	}
	if cfg.Ollama.NumCtx != 32768 || cfg.Ollama.NumPredict != 4096 || cfg.Ollama.KeepAlive != "0s" {
		t.Errorf("Ollama resource controls = %+v", cfg.Ollama)
	}
	if cfg.Hybrid.WorkerBackend != "ollama" {
		t.Errorf("Hybrid.WorkerBackend = %q, want %q", cfg.Hybrid.WorkerBackend, "ollama")
	}
	if cfg.Hybrid.AggregatorBackend != "ollama" {
		t.Errorf("Hybrid.AggregatorBackend = %q, want %q", cfg.Hybrid.AggregatorBackend, "ollama")
	}
	if cfg.Hybrid.AggregatorModel != "qwen3.8:27b-q4_K_M" {
		t.Errorf("Hybrid.AggregatorModel = %q, want %q", cfg.Hybrid.AggregatorModel, "qwen3.8:27b-q4_K_M")
	}
	if cfg.Hybrid.EnableVerification {
		t.Error("Hybrid.EnableVerification should default to false")
	}
	if cfg.Tools.MaxResults != 6 {
		t.Errorf("Tools.MaxResults = %d, want 6", cfg.Tools.MaxResults)
	}
	if cfg.Tools.MaxIterations != 6 {
		t.Errorf("Tools.MaxIterations = %d, want 6", cfg.Tools.MaxIterations)
	}
	if cfg.Graph.Rollup.Enabled {
		t.Error("expected Graph.Rollup.Enabled = false by default")
	}
	if cfg.Graph.Rollup.MaxPerCycle != 5 {
		t.Errorf("Graph.Rollup.MaxPerCycle = %d, want 5", cfg.Graph.Rollup.MaxPerCycle)
	}
}

func TestDir_EnvOverride(t *testing.T) {
	t.Setenv("RESEARCHGUY_CONFIG_DIR", "/tmp/test-config")
	if got := Dir(); got != "/tmp/test-config" {
		t.Errorf("Dir() = %q, want %q", got, "/tmp/test-config")
	}
}

func TestDir_DefaultFallback(t *testing.T) {
	t.Setenv("RESEARCHGUY_CONFIG_DIR", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("cannot get home dir: %v", err)
	}
	want := filepath.Join(home, ".researchguy")
	if got := Dir(); got != want {
		t.Errorf("Dir() = %q, want %q", got, want)
	}
}

func TestLegacyDir(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("cannot get home dir: %v", err)
	}
	want := filepath.Join(home, ".researcher")
	if got := LegacyDir(); got != want {
		t.Errorf("LegacyDir() = %q, want %q", got, want)
	}
}

func TestFilePath(t *testing.T) {
	t.Setenv("RESEARCHGUY_CONFIG_DIR", "/tmp/test-config")
	want := "/tmp/test-config/config.yaml"
	if got := FilePath(); got != want {
		t.Errorf("FilePath() = %q, want %q", got, want)
	}
}

func TestLoad_FileNotFound(t *testing.T) {
	// Point at an empty temp dir — no config.yaml exists
	t.Setenv("RESEARCHGUY_CONFIG_DIR", t.TempDir())

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	// Should return defaults
	if cfg.DefaultBackend != "claude" {
		t.Errorf("DefaultBackend = %q, want %q", cfg.DefaultBackend, "claude")
	}
	if cfg.Claude.Model != "opus" {
		t.Errorf("Claude.Model = %q, want %q", cfg.Claude.Model, "opus")
	}
}

func TestLoad_ValidFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RESEARCHGUY_CONFIG_DIR", dir)

	yaml := `research_dir: /custom/research
default_backend: ollama
claude:
  binary: /usr/bin/claude
  model: sonnet
  max_tokens: 8000
ollama:
  host: http://myhost:11434
  model: llama3
  fallback_model: mistral
  utility_model: small-model
  num_ctx: 24576
  num_predict: 2048
  keep_alive: 3m
hybrid:
  worker_backend: ollama
  worker_models: [qwen3, mistral]
  aggregator_backend: claude
  aggregator_model: sonnet
  verifier_backend: claude
  verifier_model: haiku
  enable_verification: true
  max_parallel: 3
`
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0644); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.DefaultBackend != "ollama" {
		t.Errorf("DefaultBackend = %q, want %q", cfg.DefaultBackend, "ollama")
	}
	if cfg.ResearchDir != "/custom/research" {
		t.Errorf("ResearchDir = %q, want %q", cfg.ResearchDir, "/custom/research")
	}
	if cfg.Claude.Binary != "/usr/bin/claude" {
		t.Errorf("Claude.Binary = %q, want %q", cfg.Claude.Binary, "/usr/bin/claude")
	}
	if cfg.Claude.Model != "sonnet" {
		t.Errorf("Claude.Model = %q, want %q", cfg.Claude.Model, "sonnet")
	}
	if cfg.Claude.MaxTokens != 8000 {
		t.Errorf("Claude.MaxTokens = %d, want 8000", cfg.Claude.MaxTokens)
	}
	if cfg.Ollama.Host != "http://myhost:11434" {
		t.Errorf("Ollama.Host = %q, want %q", cfg.Ollama.Host, "http://myhost:11434")
	}
	if cfg.Ollama.Model != "llama3" {
		t.Errorf("Ollama.Model = %q, want %q", cfg.Ollama.Model, "llama3")
	}
	if cfg.Ollama.FallbackModel != "mistral" {
		t.Errorf("Ollama.FallbackModel = %q, want %q", cfg.Ollama.FallbackModel, "mistral")
	}
	if cfg.Ollama.UtilityModel != "small-model" || cfg.Ollama.NumCtx != 24576 || cfg.Ollama.NumPredict != 2048 || cfg.Ollama.KeepAlive != "3m" {
		t.Errorf("Ollama resource controls = %+v", cfg.Ollama)
	}
	if cfg.Hybrid.AggregatorModel != "sonnet" {
		t.Errorf("Hybrid.AggregatorModel = %q, want %q", cfg.Hybrid.AggregatorModel, "sonnet")
	}
	if cfg.Hybrid.MaxParallel != 3 {
		t.Errorf("Hybrid.MaxParallel = %d, want 3", cfg.Hybrid.MaxParallel)
	}
	if cfg.Hybrid.VerifierModel != "haiku" {
		t.Errorf("Hybrid.VerifierModel = %q, want %q", cfg.Hybrid.VerifierModel, "haiku")
	}
}

func TestLoad_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RESEARCHGUY_CONFIG_DIR", dir)

	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("{{invalid yaml::"), 0644); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
	if !strings.Contains(err.Error(), "parsing config") {
		t.Errorf("error = %q, want to contain 'parsing config'", err.Error())
	}
}

func TestDefaults(t *testing.T) {
	cfg := defaults()

	if cfg.ResearchDir != "~/sentinel/research" {
		t.Errorf("ResearchDir = %q, want %q", cfg.ResearchDir, "~/sentinel/research")
	}
	if cfg.DefaultBackend != "claude" {
		t.Errorf("DefaultBackend = %q, want %q", cfg.DefaultBackend, "claude")
	}
	if cfg.Claude.Binary != "claude" {
		t.Errorf("Claude.Binary = %q, want %q", cfg.Claude.Binary, "claude")
	}
	if cfg.Claude.MaxTokens != 16000 {
		t.Errorf("Claude.MaxTokens = %d, want 16000", cfg.Claude.MaxTokens)
	}
	if cfg.Ollama.Host != "http://localhost:11434" {
		t.Errorf("Ollama.Host = %q, want %q", cfg.Ollama.Host, "http://localhost:11434")
	}
	if cfg.Ollama.Model != "glm-4.7-flash" || cfg.Ollama.FallbackModel != "" || cfg.Ollama.UtilityModel != "qwen3.5:9b" {
		t.Errorf("Ollama model defaults = %+v", cfg.Ollama)
	}
	if cfg.Ollama.NumCtx != 32768 || cfg.Ollama.NumPredict != 4096 || cfg.Ollama.KeepAlive != "0s" {
		t.Errorf("Ollama resource defaults = %+v", cfg.Ollama)
	}
	if cfg.Hybrid.MaxParallel != 1 {
		t.Errorf("Hybrid.MaxParallel = %d, want 1", cfg.Hybrid.MaxParallel)
	}
	if cfg.Hybrid.AggregatorBackend != "ollama" || cfg.Hybrid.AggregatorModel != "qwen3.8:27b-q4_K_M" {
		t.Errorf("Hybrid aggregator defaults = %+v", cfg.Hybrid)
	}
	if cfg.Hybrid.VerifierBackend != "ollama" || cfg.Hybrid.VerifierModel != "qwen3.5:9b" || cfg.Hybrid.EnableVerification {
		t.Errorf("Hybrid verifier defaults = %+v", cfg.Hybrid)
	}
	if cfg.Scheduler.PollInterval != "60s" {
		t.Errorf("Scheduler.PollInterval = %q, want %q", cfg.Scheduler.PollInterval, "60s")
	}
	if cfg.Scheduler.MaxConcurrent != 1 {
		t.Errorf("Scheduler.MaxConcurrent = %d, want 1", cfg.Scheduler.MaxConcurrent)
	}
	if !cfg.Tools.Enabled {
		t.Error("Tools.Enabled should be true")
	}
	if !cfg.Grepai.AutoIndex {
		t.Error("Grepai.AutoIndex should be true")
	}
	if cfg.Grepai.Binary != "grepai" {
		t.Errorf("Grepai.Binary = %q, want %q", cfg.Grepai.Binary, "grepai")
	}
	if cfg.Grepai.Workspace != "" {
		t.Errorf("Grepai.Workspace = %q, want empty default", cfg.Grepai.Workspace)
	}
	if cfg.Grepai.Project != "" {
		t.Errorf("Grepai.Project = %q, want empty default", cfg.Grepai.Project)
	}
}

func TestLoad_GrepaiWorkspace(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RESEARCHGUY_CONFIG_DIR", dir)

	yaml := `grepai:
  auto_index: true
  binary: grepai
  workspace: sentinel-personal
  project: research
`
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0644); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.Grepai.Workspace != "sentinel-personal" {
		t.Errorf("Grepai.Workspace = %q, want %q", cfg.Grepai.Workspace, "sentinel-personal")
	}
	if cfg.Grepai.Project != "research" {
		t.Errorf("Grepai.Project = %q, want %q", cfg.Grepai.Project, "research")
	}
}

func TestGrepaiProjectList(t *testing.T) {
	cases := []struct {
		name string
		g    GrepaiConfig
		want []string
	}{
		{"none", GrepaiConfig{}, nil},
		{"single project", GrepaiConfig{Project: "research"}, []string{"research"}},
		{"projects win", GrepaiConfig{Project: "research", Projects: []string{"a", "b"}}, []string{"a", "b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.g.ProjectList()
			if strings.Join(got, ",") != strings.Join(tc.want, ",") || len(got) != len(tc.want) {
				t.Fatalf("ProjectList() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLoad_ProjectsAndReadRoots(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RESEARCHGUY_CONFIG_DIR", dir)
	yaml := `grepai:
  workspace: sentinel-personal
  projects: [research, manipulation]
read_roots:
  - ~/sentinel/books
`
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Grepai.ProjectList(); len(got) != 2 || got[1] != "manipulation" {
		t.Fatalf("projects = %v", got)
	}
	if len(cfg.ReadRoots) != 1 || cfg.ReadRoots[0] != "~/sentinel/books" {
		t.Fatalf("read_roots = %v", cfg.ReadRoots)
	}
	if cfg.Grepai.WorkspaceFile != "~/.grepai/workspace.yaml" {
		t.Fatalf("workspace_file default lost: %q", cfg.Grepai.WorkspaceFile)
	}
}

func TestDefaultYAML_NewFields(t *testing.T) {
	var cfg Config
	if err := yaml.Unmarshal([]byte(DefaultYAML), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Grepai.WorkspaceFile != "~/.grepai/workspace.yaml" || len(cfg.ReadRoots) != 0 || len(cfg.Grepai.Projects) != 0 {
		t.Fatalf("grepai = %+v, read_roots = %v", cfg.Grepai, cfg.ReadRoots)
	}
}

func TestCodexConfig_DefaultsAndYAML(t *testing.T) {
	cfg := defaults()
	if cfg.Codex.Binary != "codex" || cfg.Codex.Model != "" || cfg.Codex.ReasoningEffort != "" || !cfg.Codex.IgnoreUserConfig {
		t.Errorf("Codex defaults = %+v", cfg.Codex)
	}

	var parsed Config
	if err := yaml.Unmarshal([]byte(DefaultYAML), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Codex != cfg.Codex {
		t.Errorf("DefaultYAML codex = %+v, want defaults() %+v", parsed.Codex, cfg.Codex)
	}
}

func TestLoad_CodexHybridWorkers(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RESEARCHGUY_CONFIG_DIR", dir)
	data := `default_backend: hybrid
codex:
  model: gpt-test
  reasoning_effort: high
  ignore_user_config: false
hybrid:
  worker_backend: codex
  worker_models: [gpt-test]
  aggregator_backend: claude
  aggregator_model: opus
  max_parallel: 5
`
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Codex.Binary != "codex" {
		t.Errorf("Codex.Binary = %q, want the default kept when unset", cfg.Codex.Binary)
	}
	if cfg.Codex.Model != "gpt-test" || cfg.Codex.ReasoningEffort != "high" || cfg.Codex.IgnoreUserConfig {
		t.Errorf("Codex = %+v", cfg.Codex)
	}
	if cfg.Hybrid.WorkerBackend != "codex" || cfg.Hybrid.AggregatorBackend != "claude" || cfg.Hybrid.MaxParallel != 5 {
		t.Errorf("Hybrid = %+v", cfg.Hybrid)
	}
}

func TestHybridMaxEvidenceChars_DefaultsAndOverride(t *testing.T) {
	// 0 lets the hybrid backend size the cap to the aggregator's backend.
	if got := defaults().Hybrid.MaxEvidenceChars; got != 0 {
		t.Errorf("defaults() Hybrid.MaxEvidenceChars = %d, want 0", got)
	}
	var parsed Config
	if err := yaml.Unmarshal([]byte(DefaultYAML), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Hybrid.MaxEvidenceChars != 0 {
		t.Errorf("DefaultYAML max_evidence_chars = %d, want 0", parsed.Hybrid.MaxEvidenceChars)
	}

	dir := t.TempDir()
	t.Setenv("RESEARCHGUY_CONFIG_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("hybrid:\n  max_evidence_chars: 400000\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Hybrid.MaxEvidenceChars != 400_000 {
		t.Errorf("loaded max_evidence_chars = %d, want 400000", cfg.Hybrid.MaxEvidenceChars)
	}
}

func TestStoreDir_BlankResolvesUnderConfigDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RESEARCHGUY_CONFIG_DIR", dir)

	// No config file at all.
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "store"); cfg.Store.Dir != want {
		t.Errorf("store dir with no config file = %q, want %q", cfg.Store.Dir, want)
	}

	// The default YAML leaves it blank too.
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(DefaultYAML), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "store"); cfg.Store.Dir != want {
		t.Errorf("store dir from DefaultYAML = %q, want %q", cfg.Store.Dir, want)
	}
}

func TestStoreDir_ExplicitIsExpanded(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RESEARCHGUY_CONFIG_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("store:\n  dir: ~/shared-store\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	if want := filepath.Join(home, "shared-store"); cfg.Store.Dir != want {
		t.Errorf("store dir = %q, want %q", cfg.Store.Dir, want)
	}
}

func TestClaudeIgnoreUserConfig_DefaultsOnAndCanBeDisabled(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RESEARCHGUY_CONFIG_DIR", dir)
	// A claude block that doesn't mention the key keeps the default.
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("claude:\n  model: opus\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Claude.IgnoreUserConfig {
		t.Error("claude.ignore_user_config should default to true")
	}

	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("claude:\n  ignore_user_config: false\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Claude.IgnoreUserConfig {
		t.Error("claude.ignore_user_config: false should be honored")
	}
}
