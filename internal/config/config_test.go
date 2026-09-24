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
	if cfg.Hybrid.WorkerBackend != "ollama" {
		t.Errorf("Hybrid.WorkerBackend = %q, want %q", cfg.Hybrid.WorkerBackend, "ollama")
	}
	if cfg.Hybrid.AggregatorBackend != "claude" {
		t.Errorf("Hybrid.AggregatorBackend = %q, want %q", cfg.Hybrid.AggregatorBackend, "claude")
	}
	if !cfg.Hybrid.EnableVerification {
		t.Error("Hybrid.EnableVerification should be true")
	}
	if cfg.Tools.MaxResults != 10 {
		t.Errorf("Tools.MaxResults = %d, want 10", cfg.Tools.MaxResults)
	}
	if cfg.Tools.MaxIterations != 20 {
		t.Errorf("Tools.MaxIterations = %d, want 20", cfg.Tools.MaxIterations)
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
	if cfg.Hybrid.MaxParallel != 2 {
		t.Errorf("Hybrid.MaxParallel = %d, want 2", cfg.Hybrid.MaxParallel)
	}
	if cfg.Hybrid.VerifierModel != "sonnet" {
		t.Errorf("Hybrid.VerifierModel = %q, want %q", cfg.Hybrid.VerifierModel, "sonnet")
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
