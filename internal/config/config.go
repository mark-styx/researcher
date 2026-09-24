package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	ResearchDir    string          `yaml:"research_dir"`
	DefaultBackend string          `yaml:"default_backend"`
	Claude         ClaudeConfig    `yaml:"claude"`
	Ollama         OllamaConfig    `yaml:"ollama"`
	Hybrid         HybridConfig    `yaml:"hybrid"`
	Tools          ToolsConfig     `yaml:"tools"`
	Scheduler      SchedulerConfig `yaml:"scheduler"`
	Grepai         GrepaiConfig    `yaml:"grepai"`
	Ask            AskConfig       `yaml:"ask"`
	Graph          GraphConfig     `yaml:"graph"`
}

type GraphConfig struct {
	Rollup GraphRollupConfig `yaml:"rollup"`
}

// GraphRollupConfig gates the background pass that re-summarizes graph nodes
// whose linked file has changed since they were last summarized. Off by
// default until it's had real use (see docs/node-rollup-design.md).
type GraphRollupConfig struct {
	Enabled     bool `yaml:"enabled"`
	MaxPerCycle int  `yaml:"max_per_cycle"`
}

type AskConfig struct {
	MaxAge string `yaml:"max_age"`
}

type ClaudeConfig struct {
	Binary       string  `yaml:"binary"`
	Model        string  `yaml:"model"`
	MaxTokens    int     `yaml:"max_tokens"`     // Deprecated: Claude CLI no longer supports --max-tokens.
	MaxBudgetUSD float64 `yaml:"max_budget_usd"` // Optional max spend per call (--max-budget-usd).
	MaxTurns     int     `yaml:"max_turns"`      // Max agentic turns for tool-using calls (--max-turns).
}

type OllamaConfig struct {
	Host          string `yaml:"host"`
	Model         string `yaml:"model"`
	FallbackModel string `yaml:"fallback_model"`
}

type HybridConfig struct {
	WorkerBackend      string   `yaml:"worker_backend"`
	WorkerModels       []string `yaml:"worker_models"`
	AggregatorBackend  string   `yaml:"aggregator_backend"`
	AggregatorModel    string   `yaml:"aggregator_model"`
	VerifierBackend    string   `yaml:"verifier_backend"`
	VerifierModel      string   `yaml:"verifier_model"`
	EnableVerification bool     `yaml:"enable_verification"`
	MaxParallel        int      `yaml:"max_parallel"`
}

type SchedulerConfig struct {
	PollInterval  string `yaml:"poll_interval"`
	MaxConcurrent int    `yaml:"max_concurrent"`
	LogFile       string `yaml:"log_file"`
	PIDFile       string `yaml:"pid_file"`
}

type ToolsConfig struct {
	Enabled       bool `yaml:"enabled"`
	MaxIterations int  `yaml:"max_iterations"`
	MaxResults    int  `yaml:"max_results"`
}

type GrepaiConfig struct {
	AutoIndex bool   `yaml:"auto_index"`
	Binary    string `yaml:"binary"`
	Workspace string `yaml:"workspace"`
	Project   string `yaml:"project"`
}

const DefaultYAML = `# Research database location
research_dir: ~/sentinel/research

# Default LLM backend ("claude", "ollama", or "hybrid")
default_backend: claude

# Claude CLI configuration
claude:
  binary: claude
  model: opus
  max_tokens: 16000
  max_turns: 50

# Ollama configuration
ollama:
  host: http://localhost:11434
  model: qwen3-coder-next
  fallback_model: nemotron

# Hybrid configuration (small local workers + aggregator model)
hybrid:
  worker_backend: ollama
  worker_models:
    - qwen3-coder-next
    - nemotron
  aggregator_backend: claude
  aggregator_model: opus
  verifier_backend: claude
  verifier_model: sonnet
  enable_verification: true
  max_parallel: 2

# Tool use (web search, web fetch)
tools:
  enabled: true
  max_iterations: 20
  max_results: 10

# Scheduler
scheduler:
  poll_interval: 60s
  max_concurrent: 1
  log_file: ~/.researchguy/scheduler.log
  pid_file: ~/.researchguy/scheduler.pid

# grepai
# If this research_dir is registered as a project inside a grepai workspace
# (see "grepai workspace list"), set workspace/project so search hits the
# workspace's shared index instead of a standalone local one. Leave both
# blank for a plain, non-workspace grepai project.
grepai:
  auto_index: true
  binary: grepai
  workspace: ""
  project: ""

# ask command
ask:
  max_age: 90d

# Knowledge graph rollup (off by default): periodically re-summarizes nodes
# whose linked file changed since it was last summarized. Proposals are
# written to a pending_summary field for review, not applied automatically —
# see "researchguy graph approve".
graph:
  rollup:
    enabled: false
    max_per_cycle: 5
`

func Dir() string {
	if dir := os.Getenv("RESEARCHGUY_CONFIG_DIR"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".researchguy")
}

// LegacyDir returns the pre-rename config directory (~/.researcher), so
// callers can detect and surface data left behind by the old binary name
// without moving it automatically.
func LegacyDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".researcher")
}

func FilePath() string {
	return filepath.Join(Dir(), "config.yaml")
}

func ExpandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	return path
}

func Load() (*Config, error) {
	path := FilePath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return defaults(), nil
		}
		return nil, fmt.Errorf("reading config: %w", err)
	}

	cfg := defaults()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	return cfg, nil
}

func defaults() *Config {
	return &Config{
		ResearchDir:    "~/sentinel/research",
		DefaultBackend: "claude",
		Claude: ClaudeConfig{
			Binary:    "claude",
			Model:     "opus",
			MaxTokens: 16000,
			MaxTurns:  50,
		},
		Ollama: OllamaConfig{
			Host:          "http://localhost:11434",
			Model:         "qwen3-coder-next",
			FallbackModel: "nemotron",
		},
		Hybrid: HybridConfig{
			WorkerBackend:      "ollama",
			WorkerModels:       []string{"qwen3-coder-next", "nemotron"},
			AggregatorBackend:  "claude",
			AggregatorModel:    "opus",
			VerifierBackend:    "claude",
			VerifierModel:      "sonnet",
			EnableVerification: true,
			MaxParallel:        2,
		},
		Tools: ToolsConfig{
			Enabled:       true,
			MaxIterations: 20,
			MaxResults:    10,
		},
		Scheduler: SchedulerConfig{
			PollInterval:  "60s",
			MaxConcurrent: 1,
			LogFile:       "~/.researchguy/scheduler.log",
			PIDFile:       "~/.researchguy/scheduler.pid",
		},
		Grepai: GrepaiConfig{
			AutoIndex: true,
			Binary:    "grepai",
		},
		Ask: AskConfig{
			MaxAge: "90d",
		},
		Graph: GraphConfig{
			Rollup: GraphRollupConfig{
				Enabled:     false,
				MaxPerCycle: 5,
			},
		},
	}
}
