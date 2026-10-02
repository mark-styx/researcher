package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	ResearchDir    string          `yaml:"research_dir"`
	DefaultBackend string          `yaml:"default_backend"`
	Claude         ClaudeConfig    `yaml:"claude"`
	Ollama         OllamaConfig    `yaml:"ollama"`
	Codex          CodexConfig     `yaml:"codex"`
	Hybrid         HybridConfig    `yaml:"hybrid"`
	Tools          ToolsConfig     `yaml:"tools"`
	Scheduler      SchedulerConfig `yaml:"scheduler"`
	Grepai         GrepaiConfig    `yaml:"grepai"`
	Ask            AskConfig       `yaml:"ask"`
	Graph          GraphConfig     `yaml:"graph"`
	Store          StoreConfig     `yaml:"store"`
	// ReadRoots are extra directories researchguy_read may open besides
	// research_dir (for example book repos searched through grepai.projects).
	ReadRoots []string `yaml:"read_roots,omitempty"`
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

// StoreConfig locates the on-disk research store, where every task writes
// a run record and the evidence it collected (internal/store). A blank Dir
// resolves to <config dir>/store when the config is loaded. DSN is the
// Postgres index derived from it (internal/store/index); blank means no
// index, and runs are only written to disk.
type StoreConfig struct {
	Dir   string           `yaml:"dir"`
	DSN   string           `yaml:"dsn"`
	Fetch StoreFetchConfig `yaml:"fetch"`
	Embed StoreEmbedConfig `yaml:"embed"`
}

// StoreFetchConfig drives the fetch stage (internal/fetch): after a run
// ends, the pages its workers opened, the URLs its drafts and report cite
// and each search's top results are fetched into the store.
type StoreFetchConfig struct {
	Enabled bool `yaml:"enabled"`
	// TopResults is how many of each search's top-ranked results are
	// fetched, besides opened and cited pages.
	TopResults  int    `yaml:"top_results"`
	Concurrency int    `yaml:"concurrency"`
	Timeout     string `yaml:"timeout"` // per request
	// Budget caps one run's fetch stage; what it doesn't reach is fetched
	// by `researchguy store fetch` or the daemon. "0" is no limit.
	Budget string `yaml:"budget"`
	// OpenAlex looks DOIs up for metadata, abstracts and open-access
	// copies when the publisher's page gives too little text.
	OpenAlex  bool   `yaml:"openalex"`
	UserAgent string `yaml:"user_agent,omitempty"`
}

// TimeoutDuration is Timeout parsed, 20s when blank or invalid.
func (f StoreFetchConfig) TimeoutDuration() time.Duration {
	return durationOr(f.Timeout, 20*time.Second)
}

// BudgetDuration is Budget parsed, 3m when blank or invalid, 0 (no limit)
// for "0".
func (f StoreFetchConfig) BudgetDuration() time.Duration {
	if strings.TrimSpace(f.Budget) == "0" {
		return 0
	}
	return durationOr(f.Budget, 3*time.Minute)
}

// StoreEmbedConfig is the embedding model passages are indexed with.
// Embeddings need the index (store.dsn) and Ollama.
type StoreEmbedConfig struct {
	Model string `yaml:"model"`
	// Host is the Ollama server; blank uses ollama.host.
	Host string `yaml:"host"`
	// Budget caps embedding after a run; the rest is embedded by
	// `researchguy store embed` or the daemon.
	Budget string `yaml:"budget"`
}

// BudgetDuration is Budget parsed, 2m when blank or invalid.
func (e StoreEmbedConfig) BudgetDuration() time.Duration {
	return durationOr(e.Budget, 2*time.Minute)
}

func durationOr(s string, def time.Duration) time.Duration {
	d, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil || d <= 0 {
		return def
	}
	return d
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
	// IgnoreUserConfig runs the CLI with --safe-mode and --strict-mcp-config,
	// so ~/.claude CLAUDE.md files, memory, hooks and MCP servers stay out of
	// research calls. Auth is unaffected.
	IgnoreUserConfig bool `yaml:"ignore_user_config"`
}

type OllamaConfig struct {
	Host          string `yaml:"host"`
	Model         string `yaml:"model"`
	FallbackModel string `yaml:"fallback_model"`
	UtilityModel  string `yaml:"utility_model"`
	NumCtx        int    `yaml:"num_ctx"`
	NumPredict    int    `yaml:"num_predict"`
	KeepAlive     string `yaml:"keep_alive"`
}

// CodexConfig drives the Codex CLI backend ("codex exec"). Blank model or
// reasoning effort leaves Codex's own default in place.
type CodexConfig struct {
	Binary          string `yaml:"binary"`
	Model           string `yaml:"model"`
	ReasoningEffort string `yaml:"reasoning_effort"`
	// IgnoreUserConfig skips ~/.codex/config.toml so worker runs don't start
	// the user's MCP servers. Auth still comes from CODEX_HOME.
	IgnoreUserConfig bool `yaml:"ignore_user_config"`
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
	// MaxEvidenceChars caps how much of the raw tool-result ledger one
	// aggregator or critic prompt receives, split evenly across shards. The
	// full ledger is saved to the run record either way. 0 sizes it to the
	// backend: ~2M chars for claude (Opus has a 1M-token window), 80k
	// otherwise. Set it for a smaller-window claude model.
	MaxEvidenceChars int `yaml:"max_evidence_chars"`
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
	// Projects searches several workspace projects at once. When set it
	// replaces Project; Project alone still works as a one-element list.
	Projects []string `yaml:"projects,omitempty"`
	// WorkspaceFile is grepai's workspace registry, used to map workspace
	// hit paths ("<workspace>/<project>/<rel>") back to files on disk.
	WorkspaceFile string `yaml:"workspace_file,omitempty"`
}

// ProjectList returns the workspace projects to search: Projects if set,
// otherwise Project as a one-element list, otherwise nil.
func (g GrepaiConfig) ProjectList() []string {
	if len(g.Projects) > 0 {
		return g.Projects
	}
	if g.Project != "" {
		return []string{g.Project}
	}
	return nil
}

const DefaultYAML = `# Research database location
research_dir: ~/sentinel/research

# Default LLM backend ("claude", "ollama", "codex", or "hybrid")
default_backend: claude

# Claude CLI configuration
claude:
  binary: claude
  model: opus
  max_tokens: 16000
  max_turns: 50
  # Run with --safe-mode and no MCP servers, so your CLAUDE.md files, memory,
  # hooks and MCP servers stay out of research calls. Auth is unaffected.
  ignore_user_config: true

# Ollama configuration
ollama:
  host: http://localhost:11434
  model: glm-4.7-flash
  fallback_model: ""
  utility_model: qwen3.5:9b
  num_ctx: 32768
  num_predict: 4096
  keep_alive: 0s

# Codex CLI configuration ("codex exec"). Blank model/reasoning_effort use
# Codex's defaults. ignore_user_config skips ~/.codex/config.toml (and its
# MCP servers); auth still comes from CODEX_HOME.
codex:
  binary: codex
  model: ""
  reasoning_effort: ""
  ignore_user_config: true

# Hybrid configuration (small local workers + aggregator model).
# worker_backend may be "codex" to run the shards through Codex instead.
hybrid:
  worker_backend: ollama
  worker_models:
    - glm-4.7-flash
  aggregator_backend: ollama
  aggregator_model: qwen3.8:27b-q4_K_M
  verifier_backend: ollama
  verifier_model: qwen3.5:9b
  enable_verification: false
  max_parallel: 1
  # Cap on raw tool results one aggregator or critic prompt receives, split
  # evenly across shards. The full ledger is saved to the run record either
  # way. 0 sizes it to the backend: ~2M chars for claude, 80k otherwise.
  max_evidence_chars: 0

# Tool use (web search, web fetch)
tools:
  enabled: true
  max_iterations: 6
  max_results: 6

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
# blank for a plain, non-workspace grepai project. "projects" searches
# several workspace projects at once (it replaces "project" when set).
# workspace_file is grepai's registry, used to map hits back to real paths.
grepai:
  auto_index: true
  binary: grepai
  workspace: ""
  project: ""
  projects: []
  workspace_file: ~/.grepai/workspace.yaml

# Extra directories researchguy_read may open besides research_dir, e.g. the
# roots of other grepai projects listed above.
read_roots: []

# ask command ("none" disables the freshness filter)
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

# Research store. Every task writes a run record and the raw evidence it
# collected under <dir>/runs/<run_id>/. Blank means <config dir>/store.
# Point several config dirs at one store to keep all research together.
# dsn is the Postgres index built from the store, for example
# postgres://localhost:5432/researchguy (create it with
# ` + "`researchguy store init`" + `). Blank means no index.
store:
  dir: ""
  dsn: ""
  # After a run ends, fetch the pages its workers opened, the URLs its
  # drafts and report cite, and each search's top_results results, storing
  # their raw bytes and text. budget caps one run's fetch ("0" is no limit);
  # the rest waits for ` + "`researchguy store fetch`" + ` or the daemon. openalex
  # looks DOIs up for abstracts and open-access copies of blocked papers.
  fetch:
    enabled: true
    top_results: 3
    concurrency: 8
    timeout: 20s
    budget: 3m
    openalex: true
  # Passage embeddings for the index (needs dsn and Ollama). Blank host
  # uses ollama.host.
  embed:
    model: nomic-embed-text
    host: ""
    budget: 2m
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
			cfg := defaults()
			resolveStoreDir(cfg)
			return cfg, nil
		}
		return nil, fmt.Errorf("reading config: %w", err)
	}

	cfg := defaults()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	resolveStoreDir(cfg)
	return cfg, nil
}

// resolveStoreDir fills a blank store.dir with <config dir>/store and
// expands a leading ~/.
func resolveStoreDir(cfg *Config) {
	if strings.TrimSpace(cfg.Store.Dir) == "" {
		cfg.Store.Dir = filepath.Join(Dir(), "store")
		return
	}
	cfg.Store.Dir = ExpandPath(cfg.Store.Dir)
}

func defaults() *Config {
	return &Config{
		ResearchDir:    "~/sentinel/research",
		DefaultBackend: "claude",
		Claude: ClaudeConfig{
			Binary:           "claude",
			Model:            "opus",
			MaxTokens:        16000,
			MaxTurns:         50,
			IgnoreUserConfig: true,
		},
		Ollama: OllamaConfig{
			Host:          "http://localhost:11434",
			Model:         "glm-4.7-flash",
			FallbackModel: "",
			UtilityModel:  "qwen3.5:9b",
			NumCtx:        32768,
			NumPredict:    4096,
			KeepAlive:     "0s",
		},
		Codex: CodexConfig{
			Binary:           "codex",
			IgnoreUserConfig: true,
		},
		Hybrid: HybridConfig{
			WorkerBackend:      "ollama",
			WorkerModels:       []string{"glm-4.7-flash"},
			AggregatorBackend:  "ollama",
			AggregatorModel:    "qwen3.8:27b-q4_K_M",
			VerifierBackend:    "ollama",
			VerifierModel:      "qwen3.5:9b",
			EnableVerification: false,
			MaxParallel:        1,
			MaxEvidenceChars:   0,
		},
		Tools: ToolsConfig{
			Enabled:       true,
			MaxIterations: 6,
			MaxResults:    6,
		},
		Scheduler: SchedulerConfig{
			PollInterval:  "60s",
			MaxConcurrent: 1,
			LogFile:       "~/.researchguy/scheduler.log",
			PIDFile:       "~/.researchguy/scheduler.pid",
		},
		Grepai: GrepaiConfig{
			AutoIndex:     true,
			Binary:        "grepai",
			WorkspaceFile: "~/.grepai/workspace.yaml",
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
		Store: StoreConfig{
			Fetch: StoreFetchConfig{
				Enabled:     true,
				TopResults:  3,
				Concurrency: 8,
				Timeout:     "20s",
				Budget:      "3m",
				OpenAlex:    true,
			},
			Embed: StoreEmbedConfig{
				Model:  "nomic-embed-text",
				Budget: "2m",
			},
		},
	}
}
