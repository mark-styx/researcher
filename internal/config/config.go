package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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
	Goose          GooseConfig     `yaml:"goose"`
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
	Dir    string            `yaml:"dir"`
	DSN    string            `yaml:"dsn"`
	Fetch  StoreFetchConfig  `yaml:"fetch"`
	Embed  StoreEmbedConfig  `yaml:"embed"`
	Claims StoreClaimsConfig `yaml:"claims"`
	// VolatileMaxAge is how old a volatile claim's date can be before
	// retrieval flags it possibly outdated.
	VolatileMaxAge string `yaml:"volatile_max_age"`
}

// VolatileMaxAgeDuration is VolatileMaxAge parsed (Go durations, or
// days, weeks and years such as 30d), 30 days when blank or invalid.
func (s StoreConfig) VolatileMaxAgeDuration() time.Duration {
	return ageOr(s.VolatileMaxAge, 30*24*time.Hour)
}

// StoreClaimsConfig drives claims (internal/claims): the daemon extracts
// checkable claims from fetched documents with a local model, documents
// reports cite first, and has a stronger model label how each new claim
// relates to its nearest claims.
type StoreClaimsConfig struct {
	Enabled bool `yaml:"enabled"`
	// Model extracts claims through Ollama; blank uses
	// ollama.utility_model. Host blank uses ollama.host.
	Model string `yaml:"model"`
	Host  string `yaml:"host"`
	// Budget caps one daemon pass of extraction; `researchguy store
	// extract` runs until done unless given one.
	Budget string `yaml:"budget"`
	// A document is extracted in chunks of up to ChunkChars characters,
	// at most MaxChunks of them. A chunk the model fails on is tried again
	// on later passes, up to MaxAttempts.
	ChunkChars  int             `yaml:"chunk_chars"`
	MaxChunks   int             `yaml:"max_chunks"`
	MaxAttempts int             `yaml:"max_attempts"`
	Link        StoreLinkConfig `yaml:"link"`
}

// BudgetDuration is Budget parsed, 10m when blank or invalid.
func (c StoreClaimsConfig) BudgetDuration() time.Duration {
	return durationOr(c.Budget, 10*time.Minute)
}

// StoreLinkConfig drives claim linking: each claim's nearest claims from
// other origins are labeled same, supports, contradicts, refines,
// supersedes or unrelated by Backend (claude or ollama) and Model.
type StoreLinkConfig struct {
	Enabled bool   `yaml:"enabled"`
	Backend string `yaml:"backend"`
	Model   string `yaml:"model"`
	// Neighbors is how many nearest claims each claim is compared with,
	// MinSimilarity how close (cosine, 0-1) they must be.
	Neighbors     int     `yaml:"neighbors"`
	MinSimilarity float64 `yaml:"min_similarity"`
	// BatchSize pairs go in one model call, at most MaxPairsPerDay pairs a
	// day.
	BatchSize      int `yaml:"batch_size"`
	MaxPairsPerDay int `yaml:"max_pairs_per_day"`
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

// ageOr parses s as a Go duration or a number of days, weeks or years
// (30d, 2w, 1y), def when blank or invalid.
func ageOr(s string, def time.Duration) time.Duration {
	s = strings.TrimSpace(s)
	if len(s) > 1 {
		unit := map[byte]time.Duration{'d': 24 * time.Hour, 'w': 7 * 24 * time.Hour, 'y': 365 * 24 * time.Hour}[s[len(s)-1]]
		if n, err := strconv.Atoi(s[:len(s)-1]); unit > 0 && err == nil && n > 0 {
			return time.Duration(n) * unit
		}
	}
	return durationOr(s, def)
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

// GooseConfig drives the Goose CLI backend ("goose run"). It always runs
// without the user's profile, so their extensions don't load. Blank
// provider or model leaves Goose's own configured default in place.
type GooseConfig struct {
	Binary   string `yaml:"binary"`
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`
	MaxTurns int    `yaml:"max_turns"` // --max-turns per call
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
	// AggregatorTools gives a claude aggregator the read-profile MCP tools
	// (researchguy mcp --profile read) when store.dsn is set, so it can
	// retrieve and quote the run's fetched text and prior research.
	AggregatorTools bool `yaml:"aggregator_tools"`
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

# Default LLM backend ("claude", "ollama", "codex", "goose", or "hybrid")
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

# Goose CLI configuration ("goose run"). Runs without your Goose profile, so
# none of its extensions load; with web tools on it gets researchguy's
# web_search and web_fetch (researchguy mcp --profile web) and nothing else.
# Blank provider/model use what Goose is configured with.
goose:
  binary: goose
  provider: ""
  model: ""
  max_turns: 40

# Hybrid configuration (small local workers + aggregator model).
# worker_backend may be "codex" or "goose" to run the shards through that
# CLI instead; worker_models are then that backend's model names (leave it
# empty to use codex.model or goose.model).
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
  # With store.dsn set, a claude aggregator gets the read-only researchguy
  # MCP tools to retrieve and quote the run's fetched text.
  aggregator_tools: true

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
  # After a dive, review, compare, enrich or watch ends, fetch the pages its
  # workers opened, the URLs its drafts and report cite, and each search's
  # top_results results, storing their raw bytes and text. Asks wait for the
  # daemon or ` + "`researchguy store fetch`" + `. budget caps one run's fetch
  # ("0" is no limit); the rest waits for the next pass. openalex looks DOIs
  # up for abstracts and open-access copies of blocked papers.
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
  # Claims: the daemon extracts checkable claims, each with a quote that's
  # checked against the document's text, from fetched documents (cited ones
  # first) with a local model (blank model uses ollama.utility_model),
  # within budget per pass and only while no task is running. link has a
  # stronger model label how each claim relates to its nearest claims from
  # other origins, at most max_pairs_per_day pairs a day.
  claims:
    enabled: true
    model: ""
    host: ""
    budget: 10m
    chunk_chars: 6000
    max_chunks: 8
    max_attempts: 3
    link:
      enabled: true
      backend: claude
      model: sonnet
      neighbors: 5
      min_similarity: 0.75
      batch_size: 20
      max_pairs_per_day: 500
  # A volatile claim (a status, a count to date, a price) dated older than
  # this is flagged possibly outdated.
  volatile_max_age: 30d
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
		Goose: GooseConfig{
			Binary:   "goose",
			MaxTurns: 40,
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
			AggregatorTools:    true,
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
			Claims: StoreClaimsConfig{
				Enabled:     true,
				Budget:      "10m",
				ChunkChars:  6000,
				MaxChunks:   8,
				MaxAttempts: 3,
				Link: StoreLinkConfig{
					Enabled:        true,
					Backend:        "claude",
					Model:          "sonnet",
					Neighbors:      5,
					MinSimilarity:  0.75,
					BatchSize:      20,
					MaxPairsPerDay: 500,
				},
			},
			VolatileMaxAge: "30d",
		},
	}
}
