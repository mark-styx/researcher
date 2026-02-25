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
	Tools          ToolsConfig     `yaml:"tools"`
	Scheduler      SchedulerConfig `yaml:"scheduler"`
	Grepai         GrepaiConfig    `yaml:"grepai"`
}

type ClaudeConfig struct {
	Binary    string `yaml:"binary"`
	Model     string `yaml:"model"`
	MaxTokens int    `yaml:"max_tokens"`
}

type OllamaConfig struct {
	Host          string `yaml:"host"`
	Model         string `yaml:"model"`
	FallbackModel string `yaml:"fallback_model"`
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
}

const DefaultYAML = `# Research database location
research_dir: ~/sentinel/research

# Default LLM backend ("claude" or "ollama")
default_backend: claude

# Claude CLI configuration
claude:
  binary: claude
  model: opus
  max_tokens: 16000

# Ollama configuration
ollama:
  host: http://localhost:11434
  model: qwen3-coder-next
  fallback_model: nemotron

# Tool use (web search, web fetch)
tools:
  enabled: true
  max_iterations: 20
  max_results: 10

# Scheduler
scheduler:
  poll_interval: 60s
  max_concurrent: 1
  log_file: ~/.researcher/scheduler.log
  pid_file: ~/.researcher/scheduler.pid

# grepai
grepai:
  auto_index: true
  binary: grepai
`

func Dir() string {
	if dir := os.Getenv("RESEARCHER_CONFIG_DIR"); dir != "" {
		return dir
	}
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
		},
		Ollama: OllamaConfig{
			Host:          "http://localhost:11434",
			Model:         "qwen3-coder-next",
			FallbackModel: "nemotron",
		},
		Tools: ToolsConfig{
			Enabled:       true,
			MaxIterations: 20,
			MaxResults:    10,
		},
		Scheduler: SchedulerConfig{
			PollInterval:  "60s",
			MaxConcurrent: 1,
			LogFile:       "~/.researcher/scheduler.log",
			PIDFile:       "~/.researcher/scheduler.pid",
		},
		Grepai: GrepaiConfig{
			AutoIndex: true,
			Binary:    "grepai",
		},
	}
}
