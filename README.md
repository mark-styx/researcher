# Researcher

A CLI that automates research workflows using LLM backends. Generate structured research reports, literature reviews, and enriched documents — saved as markdown and indexed for semantic search.

## Features

- **Deep research** — comprehensive structured reports with executive summaries, key concepts, state of the art, and references
- **One-shot Q&A** — quick answers with optional web search and tool use
- **Literature review** — synthesis from topics or source documents
- **Document enrichment** — expand thin sections and add context to existing research
- **Smart file organization** — LLM-based auto-categorization into topic directories with descriptive filenames
- **Migration tool** — reorganize existing flat research directories into the new category structure
- **Semantic search** — find across all research via grepai
- **Task scheduling** — cron-based recurring research with a background daemon
- **Task queue** — batch one-shot research tasks with priority ordering
- **Multi-backend** — supports Claude CLI and Ollama

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
| `enrich <path>` | Expand and add context to an existing document |
| `search <query>` | Semantic search across research via grepai |

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
| `version` | Print version |

All commands support `--help` for detailed usage and examples.

## Configuration

Config file: `~/.researcher/config.yaml` (created by `researcher init`)

```yaml
# Where research output is saved
research_dir: ~/sentinel/research

# LLM backend: "claude" or "ollama"
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
```

Override backend and model per-command with `--backend` and `--model` flags.

## Research Types

| Type | Command | Purpose | Output |
|------|---------|---------|--------|
| Ask | `ask` | Quick one-shot Q&A | Printed to stdout |
| Dive | `dive` | Comprehensive research report | `<research_dir>/<category>/<topic>.md` |
| Review | `review` | Literature review / synthesis | `<research_dir>/<category>/<topic>-review.md` |
| Enrich | `enrich` | Expand existing document | Enriched version alongside original |
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

### Config Directory

```
~/.researcher/
  config.yaml          # Configuration
  scheduler.log        # Daemon log
  scheduler.pid        # Daemon PID file
```
