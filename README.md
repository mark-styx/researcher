# Researcher

A CLI that automates research workflows using LLM backends. Generate structured research reports, literature reviews, and enriched documents — saved as markdown and indexed for semantic search.

## Features

- **Deep research** — comprehensive structured reports with executive summaries, key concepts, state of the art, and references
- **One-shot Q&A** — quick answers with optional web search and tool use
- **Literature review** — synthesis from topics or source documents
- **Comparative analysis** — side-by-side comparison of two topics or existing documents
- **Document enrichment** — expand thin sections and add context to existing research; interactive file picker when no path given
- **Smart file organization** — LLM-based auto-categorization into topic directories with descriptive filenames
- **Migration tool** — reorganize existing flat research directories into the new category structure
- **Semantic search** — find across all research via grepai, including grepai workspace mode for shared multi-project indexes
- **Context-aware generation** — `dive`/`review`/`compare` search existing research before generating, not just `ask`
- **Task scheduling** — cron-based recurring research with a background daemon
- **Task queue** — batch one-shot research tasks with priority ordering
- **MCP server** — expose all tools to Claude Code and other MCP clients via stdio
- **Multi-backend** — supports Claude CLI, Ollama, and hybrid local-worker aggregation
- **Epistemic branch roles** — hybrid backend fan-out driven by a `--mode` (`landscape`, `inquiry`) instead of generic angles, plus a `--branches` effort/breadth dial
- **Transparent critics** — hybrid backend's groundedness and narrative-vs-evidence critic passes report what they changed/flagged instead of silently rewriting
- **Knowledge graph** — entities, sources, claims, funding-pattern observations, and reports persist as referenceable nodes with typed edges (`researcher graph`), instead of being re-derived per report

## Installation

### Build from source

```bash
git clone https://github.com/marklubin/researcher.git
cd researcher
make build      # produces ./researcher binary
make install    # installs to $GOPATH/bin
```

### Go install

```bash
go install github.com/marklubin/researcher/cmd/researcher@latest
```

## Quick Start

```bash
# 1. Initialize config and directories
researcher init

# 2. Ask a quick question
researcher ask "What is quantum computing?"

# 3. Generate a full research report
researcher dive "quantum computing"

# 4. List your research projects
researcher list

# 5. Search across all research
researcher search "entanglement"
```

## Commands

### Research

| Command | Description |
|---------|-------------|
| `ask <question>` | One-shot Q&A, prints answer to stdout |
| `dive <topic>` | Deep research report saved as structured markdown |
| `review <topic>` | Literature review / synthesis, optional `--sources` |
| `compare <topicA> <topicB>` | Side-by-side comparative analysis of two topics or documents |
| `enrich [path]` | Expand and add context to an existing document (interactive picker if no path) |
| `search <query>` | Semantic search across research via grepai |

`dive`/`review`/`compare` accept `--no-research` (skip searching existing research for context) and `--max-age` (freshness filter, e.g. `90d`), same as `ask`. With the hybrid backend, they also accept `--mode` (`landscape`, `inquiry`) and `--branches` (effort/breadth dial) — see [Hybrid Backend](#hybrid-backend-modes-and-critics) below.

### Knowledge Graph

| Command | Description |
|---------|-------------|
| `graph add-node --type <type> --title <title>` | Create a node (`entity`, `source`, `claim`, `funding-pattern`, `report`) |
| `graph add-edge --from <id> --to <id> --type <type>` | Create a typed edge (`funds`, `authored-by`, `supports`, `contradicts`, `sponsors-research`, `references`, `supersedes`) |
| `graph show <id>` | Show a node's details and its connected edges |
| `graph list [--type <type>]` | List nodes, optionally filtered by type |
| `graph export [--out <path>]` | Export the full graph as JSON for visualization |

Node structure/relationships live in `~/.researcher/tasks.db`; node content lives in markdown files under `research_dir` (so grepai keeps indexing it). There's no automated entity resolution/dedup yet — check `graph list`/`graph show` before creating a node that might already exist.

### Project Management

| Command | Description |
|---------|-------------|
| `list` | List all research files grouped by category |
| `show <category>` | List files in a category |
| `show <category>/<file>` | Show details for a specific file |
| `migrate` | Reorganize flat slug directories into categories |
| `link <path>` | Create a symlink to the research directory |

### Task Scheduling

| Command | Description |
|---------|-------------|
| `watch <topic>` | Schedule recurring topic monitoring |
| `daemon start` | Start the scheduler daemon (foreground) |
| `daemon stop` | Stop the scheduler daemon |
| `daemon status` | Check if the daemon is running |
| `queue list` | Show pending queued tasks |
| `queue add` | Add a one-shot task to the queue |
| `schedule list` | List scheduled recurring tasks |
| `schedule add` | Add a scheduled task with cron expression |
| `schedule remove <id>` | Remove a scheduled task |

### Setup

| Command | Description |
|---------|-------------|
| `init` | Initialize config, directories, and grepai |
| `config show` | Print current configuration |
| `config set <key> <value>` | Update a config value |
| `mcp` | Start MCP server (stdio transport) |
| `version` | Print version |

All commands support `--help` for detailed usage and examples.

## Configuration

Config file: `~/.researcher/config.yaml` (created by `researcher init`)

```yaml
# Where research output is saved
research_dir: ~/sentinel/research

# LLM backend: "claude", "ollama", or "hybrid"
default_backend: claude

# Claude CLI settings
claude:
  binary: claude
  model: opus
  max_tokens: 16000

# Ollama settings
ollama:
  host: http://localhost:11434
  model: qwen3-coder-next
  fallback_model: nemotron

# Hybrid settings (fan-out to local models, then aggregate)
hybrid:
  worker_backend: ollama
  worker_models: [qwen3-coder-next, nemotron]
  aggregator_backend: claude
  aggregator_model: opus
  verifier_backend: claude
  verifier_model: sonnet
  enable_verification: true
  max_parallel: 2

# Web search and tool use
tools:
  enabled: true
  max_iterations: 20
  max_results: 10

# Background scheduler
scheduler:
  poll_interval: 60s
  max_concurrent: 1
  log_file: ~/.researcher/scheduler.log
  pid_file: ~/.researcher/scheduler.pid

# Semantic search indexing
grepai:
  auto_index: true
  binary: grepai
  # If research_dir is registered as a project inside a grepai workspace
  # (see "grepai workspace list"), set these so search hits the workspace's
  # shared index instead of bootstrapping a separate standalone one.
  workspace: ""
  project: ""
```

Override backend and model per-command with `--backend` and `--model` flags.

## Research Types

| Type | Command | Purpose | Output |
|------|---------|---------|--------|
| Ask | `ask` | Quick one-shot Q&A | Printed to stdout |
| Dive | `dive` | Comprehensive research report | `<research_dir>/<category>/<topic>.md` |
| Review | `review` | Literature review / synthesis | `<research_dir>/<category>/<topic>-review.md` |
| Enrich | `enrich` | Expand existing document | Enriched version alongside original |
| Compare | `compare` | Side-by-side comparative analysis | `<research_dir>/<category>/<topic>-comparison.md` |
| Watch | `watch` | Recurring topic monitoring | `<research_dir>/<category>/<topic>-watch.md` |

## Task Scheduling

The scheduler lets you automate research tasks in the background.

```bash
# Start the daemon
researcher daemon start

# Queue a one-shot task
researcher queue add --type dive --topic "quantum computing"

# Schedule a recurring task (every Monday at 9am)
researcher schedule add --type watch --topic "AI safety" --cron "0 9 * * 1"

# Check what's scheduled
researcher schedule list
researcher queue list

# Check daemon status
researcher daemon status
```

Cron format: `minute hour day-of-month month day-of-week`

## Backends

### Claude (default)

Uses the [Claude CLI](https://github.com/anthropics/claude-code). Ensure `claude` is installed and authenticated:

```bash
claude --version
researcher config set default_backend claude
```

### Ollama

Uses a local [Ollama](https://ollama.ai) instance:

```bash
ollama serve
ollama pull qwen3-coder-next
researcher config set default_backend ollama
```

Override per-command:

```bash
researcher dive "topic" --backend ollama --model llama3
```

### Hybrid backend: modes and critics

The hybrid backend fans out to worker models in parallel, aggregates their drafts, then (if `enable_verification` is on) runs two critic passes before returning:

```bash
researcher dive "topic" --backend hybrid --mode inquiry --branches 5
```

- `--mode landscape` — for tool/alternatives-comparison questions. Branches: documented alternatives, vendor claims vs. independently reported usage, competitive positioning, adoption evidence.
- `--mode inquiry` — for open-ended or contested claims. Branches: primary evidence, counter-evidence/disconfirming cases, funding and institutional provenance, independent replication, narrative-vs-evidence gap.
- `--branches <n>` — how many angles to investigate, decoupled from how many worker models are configured (models are reused round-robin if `n` exceeds `worker_models` length).

Both critic passes are visible in the saved output under a `## Critic Notes` section, not silently folded into the answer:
- **Groundedness critic** — revises the draft to keep only evidence-backed claims, and reports what it removed/softened and why.
- **Narrative-vs-evidence critic** — doesn't rewrite anything; flags claims stated as settled/consensus that aren't tied to a distinct piece of worker evidence.

## File Organization

Research files are automatically categorized by the LLM into topic directories with short descriptive filenames:

```
~/sentinel/research/           # Default research directory
  llm/                         # Auto-created category
    agentic-code.md            # Dive report
    open-source-models.md
    ai-agents-review.md        # Literature review
  architecture/
    tool-calling-patterns.md
  adblock/
    political-ads.md
    device-comparison.md
  .grepai/                     # Semantic search index
```

To migrate existing flat directories (e.g. `long-slug-topic-name/README.md`) into the new structure:

```bash
researcher migrate --dry-run   # Preview changes
researcher migrate             # Apply changes
```

## MCP Server

The `researcher mcp` command starts a [Model Context Protocol](https://modelcontextprotocol.io/) server over stdio, exposing researcher's capabilities as tools that any MCP client can call.

### Tools

| Tool | Description |
|------|-------------|
| `researcher_ask` | Ask a question with optional research context |
| `researcher_dive` | Generate a deep research report |
| `researcher_review` | Literature review / synthesis |
| `researcher_compare` | Side-by-side comparative analysis |
| `researcher_enrich` | Expand and add context to a document |
| `researcher_search` | Raw semantic search across research (chunk results, no synthesis) |
| `researcher_context` | Search + freshness filter + read + format into ready-to-use context |
| `researcher_list` | List research files by category |
| `researcher_read` | Read a research document |

`researcher_dive`/`_review`/`_compare` accept `no_research`/`max_age` params (same semantics as the CLI flags). `researcher_ask` additionally accepts `no_save`.

### Claude Code Configuration

Add to `~/.claude/settings.json`:

```json
{
  "mcpServers": {
    "researcher": {
      "command": "researcher",
      "args": ["mcp"]
    }
  }
}
```

### Config Directory

```
~/.researcher/
  config.yaml          # Configuration
  tasks.db             # Scheduler tasks + knowledge graph (nodes/edges) tables
  scheduler.log        # Daemon log
  scheduler.pid        # Daemon PID file
```
