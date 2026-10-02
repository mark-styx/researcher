# Researchguy

A CLI that automates research workflows using LLM backends. Generate structured research reports, literature reviews, and enriched documents — saved as markdown and indexed for semantic search.

## Features

- **Deep research** — comprehensive structured reports with executive summaries, key concepts, state of the art, and references
- **One-shot Q&A** — quick answers with optional web search and tool use
- **Literature review** — synthesis from topics or source documents
- **Comparative analysis** — side-by-side comparison of two topics or existing documents
- **Document enrichment** — expand thin sections and add context to existing research; interactive file picker when no path given
- **Smart file organization**: bounded utility-model categorization into topic directories with descriptive filenames
- **Migration tool** — reorganize existing flat research directories into the new category structure
- **Semantic search** — find across all research via grepai, including grepai workspace mode for shared multi-project indexes
- **Context-aware generation** — `dive`/`review`/`compare` search existing research before generating, not just `ask`
- **Task scheduling** — cron-based recurring research with a background daemon
- **Task queue** — batch one-shot research tasks with priority ordering
- **MCP server** — expose all tools to Claude Code and other MCP clients via stdio
- **Multi-backend**: supports Claude CLI, Codex CLI, Ollama, and hybrid worker aggregation
- **Epistemic branch roles** — hybrid backend fan-out driven by a `--mode` (`landscape`, `inquiry`) instead of generic angles, plus a `--branches` effort/breadth dial
- **Evidence-ledger critics**: hybrid verification checks the unchanged draft against raw successful tool results and reports flags instead of rewriting
- **Run records**: every task writes what it collected (raw tool results, worker drafts, the aggregator's prompt and raw output, metadata) to `<store.dir>/runs/<run_id>/`, so evidence that didn't fit in a prompt is still on disk and a report's `[E12]` citations resolve to a captured item
- **Knowledge graph** — entities, sources, claims, funding-pattern observations, and reports persist as referenceable nodes with typed edges (`researchguy graph`), instead of being re-derived per report

## Installation

### Build from source

```bash
git clone https://github.com/marklubin/researchguy.git
cd researchguy
make build      # produces ./researchguy binary
make install    # installs to $GOPATH/bin
```

### Go install

```bash
go install github.com/marklubin/researchguy/cmd/researchguy@latest
```

## Quick Start

```bash
# 1. Initialize config and directories
researchguy init

# 2. Ask a quick question
researchguy ask "What is quantum computing?"

# 3. Generate a full research report
researchguy dive "quantum computing"

# 4. List your research projects
researchguy list

# 5. Search across all research
researchguy search "entanglement"
```

## Commands

### Research

| Command | Description |
|---------|-------------|
| `ask <question>` | One-shot Q&A, prints answer to stdout |
| `dive <topic>` | Deep research report saved as structured markdown |
| `review <topic>` | Literature review / synthesis, optional `--sources` |
| `compare <topicA> <topicB>` | Side-by-side comparative analysis of two topics or documents |
| `critique --text <file> --evidence <file>... [--json]` | Flag claims in a text that its evidence doesn't support (the hybrid backend's two critics in flag mode; see below) |
| `enrich [path]` | Expand and add context to an existing document (interactive picker if no path) |
| `search <query>` | Semantic search across research via grepai |
| `context <topic>` | Existing research on a topic, formatted as an ask/dive would see it. Never calls an LLM |

`dive`/`review`/`compare` accept `--no-research` (skip searching existing research for context) and `--max-age` (freshness filter, e.g. `90d`), same as `ask`. With the hybrid backend, they also accept `--mode` (`landscape`, `inquiry`) and `--branches` (effort/breadth dial) — see [Hybrid Backend](#hybrid-backend-modes-and-critics) below.

For scripts and workflow engines:

- `--json` on `ask`/`dive`/`review`/`compare` prints only `{"<answer|report|review|comparison>", "saved_to", "backend", "metadata", "run_id", "run_dir"}` on stdout (progress goes to stderr). The MCP research tools return `run_id`/`run_dir` too, and a failed tool call names the run record that kept its evidence. `search --json` prints hits with their grepai `project` and on-disk `path`. `context --json` prints `{"topic", "sources", "context", "count"}`.
- `--out <path>` on `dive`/`review`/`compare` writes the report to that exact path and skips the LLM categorizer call.
- `--project <name>` (repeatable) on every research command plus `search`/`context` picks which grepai workspace projects to search, overriding `grepai.projects`.
- `--max-age none` turns off the freshness filter, e.g. to include past book research.

### Knowledge Graph

| Command | Description |
|---------|-------------|
| `graph add-node --type <type> --title <title>` | Create a node (`entity`, `source`, `claim`, `funding-pattern`, `report`, `lead`) |
| `graph add-edge --from <id> --to <id> --type <type>` | Create a typed edge (`funds`, `authored-by`, `supports`, `contradicts`, `sponsors-research`, `references`, `supersedes`) |
| `graph show <id>` | Show a node's details and its connected edges |
| `graph list [--type <type>]` | List nodes, optionally filtered by type |
| `graph link-report --path <report.md> --title <title> [--prefix <dir/>] [--json]` | Create (or reuse) the `report` node for a file and add a `references` edge to every node whose path starts with the prefix (default: the report's directory). Idempotent; alo's `deep_research` workflow runs it after synthesis |
| `graph export [--out <path>]` | Export the full graph as JSON for visualization |
| `graph approve <id>` | Approve a pending rollup resummarization (see below) |

Node structure/relationships live in `~/.researchguy/tasks.db`; node content lives in markdown files under `research_dir` (so grepai keeps indexing it). A `lead` is an unconfirmed breadcrumb (a forum comment, an offhand mention) worth chasing but not evidence: link it to what it concerns with a `references` edge, and when it's confirmed, add a `claim` that `supersedes` it. There's no automated entity resolution/dedup yet — check `graph list`/`graph show` before creating a node that might already exist.

### Book bibliographies (source graph)

`researchguy graph import-sources --book <slug> --file <book>/research/sources.json --title "<title>" --path <book>/research` records a bookworm bibliography: one `report` node for the book, one `source` node per distinct URL shared across every book that cites it, and a `references` edge from the book to each source. URLs are keyed after normalization (http/https, `www.`, trailing slash, fragments, and `utm_*`/click-ID parameters don't count as differences; keys live in the `node_keys` table). Re-running is safe; per-book notes are appended, never overwritten. bookworm runs this automatically after research when `research.researchguy.enabled` is on.

- `researchguy graph list --type source --cited-by-min 2` lists sources cited by two or more books, most-cited first.
- `researchguy graph show <url>` resolves a URL to its source node and lists the books that cite it.

### Node rollup (keeping summaries fresh)

A node's `Summary` is set at creation time and otherwise static. With `graph.rollup.enabled: true` in config, `researchguy daemon start` also polls for nodes whose linked file has changed since it was last summarized (mtime-based) and re-summarizes them with the LLM — capped at `graph.rollup.max_per_cycle` per poll so one tick can't burn through LLM budget resummarizing everything at once.

Rollup never overwrites `Summary` directly: a proposal is written to the node's metadata and the node is flagged `REVIEW` in `graph list`/`graph show`. Run `researchguy graph approve <id>` to accept it. Nodes whose linked file has disappeared are flagged `ORPHANED` instead (and left alone — no LLM call, nothing to resummarize from).

Off by default until it's had real use.

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

Config file: `~/.researchguy/config.yaml` (created by `researchguy init`)

```yaml
# Where research output is saved
research_dir: ~/sentinel/research

# LLM backend: "claude", "ollama", "codex", or "hybrid"
default_backend: claude

# Claude CLI settings
claude:
  binary: claude
  model: opus
  max_tokens: 16000
  ignore_user_config: true  # --safe-mode: no CLAUDE.md, memory, hooks or MCP servers

# Ollama settings
ollama:
  host: http://localhost:11434
  model: glm-4.7-flash
  fallback_model: ""
  utility_model: qwen3.5:9b
  num_ctx: 32768
  num_predict: 4096
  keep_alive: 0s

# Codex CLI settings (blank model/reasoning_effort = Codex defaults)
codex:
  binary: codex
  model: ""
  reasoning_effort: ""
  ignore_user_config: true

# Hybrid settings (fan-out to worker models, then aggregate)
hybrid:
  worker_backend: ollama
  worker_models: [glm-4.7-flash]
  aggregator_backend: ollama
  aggregator_model: qwen3.8:27b-q4_K_M
  verifier_backend: ollama
  verifier_model: qwen3.5:9b
  enable_verification: false
  max_parallel: 1
  max_evidence_chars: 0  # per-prompt ledger budget; 0 = ~2M chars for claude, 80k otherwise

# Web search and tool use
tools:
  enabled: true
  max_iterations: 6
  max_results: 6

# Background scheduler
scheduler:
  poll_interval: 60s
  max_concurrent: 1
  log_file: ~/.researchguy/scheduler.log
  pid_file: ~/.researchguy/scheduler.pid

# Semantic search indexing
grepai:
  auto_index: true
  binary: grepai
  # If research_dir is registered as a project inside a grepai workspace
  # (see "grepai workspace list"), set these so search hits the workspace's
  # shared index instead of bootstrapping a separate standalone one.
  workspace: ""
  project: ""
  # Search several workspace projects at once (replaces "project" when set),
  # e.g. [research, manipulation, the_poisoned_well]. Hits carry their project.
  projects: []
  # grepai's workspace registry, used to map hits back to files on disk.
  workspace_file: ~/.grepai/workspace.yaml

# Extra directories researchguy_read may open besides research_dir, e.g. the
# roots of the other projects in grepai.projects.
read_roots: []

# Knowledge graph rollup (off by default)
graph:
  rollup:
    enabled: false
    max_per_cycle: 5

# Research store: run records under <dir>/runs/<run_id>/.
# Blank means <config dir>/store.
store:
  dir: ""
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
researchguy daemon start

# Queue a one-shot task
researchguy queue add --type dive --topic "quantum computing"

# Schedule a recurring task (every Monday at 9am)
researchguy schedule add --type watch --topic "AI safety" --cron "0 9 * * 1"

# Check what's scheduled
researchguy schedule list
researchguy queue list

# Check daemon status
researchguy daemon status
```

Cron format: `minute hour day-of-month month day-of-week`

## Backends

### Claude (default)

Uses the [Claude CLI](https://github.com/anthropics/claude-code). Ensure `claude` is installed and authenticated:

```bash
claude --version
researchguy config set default_backend claude
```

Each call runs `claude -p` in a fresh empty temp dir, removed afterwards, with the prompt on stdin and the system prompt in a file (`--system-prompt-file`), so prompt size isn't bound by the 1 MB argv limit. `--tools` names exactly the tools the request asked for (`WebSearch`, `WebFetch`), or none, so a call can't run commands or edit files. `ignore_user_config: true` (the default) adds `--safe-mode` and `--strict-mcp-config`, which keep your CLAUDE.md files, memory, hooks and MCP servers out of research calls; auth is unaffected. Calls use `--no-session-persistence`, so they don't show up in `claude --resume`.

### Ollama

Uses a local [Ollama](https://ollama.com) instance. The default local stack uses a sparse worker for repeated tool calls, a dense model for one synthesis call, and a small utility model for categorization and optional verification:

```bash
ollama serve
ollama pull glm-4.7-flash
ollama pull qwen3.8:27b-q4_K_M
ollama pull qwen3.5:9b
researchguy config set default_backend ollama
```

`num_ctx` bounds the context allocation. `num_predict` is an output ceiling: a smaller per-request `MaxTokens` still wins. `keep_alive: 0s` keeps a model resident across one tool loop, then explicitly unloads it. Hybrid also unloads every worker stage before loading the aggregator, and unloads the aggregator before an optional verifier.

For a memory-constrained Ollama service, also set `OLLAMA_MAX_LOADED_MODELS=1` and `OLLAMA_NUM_PARALLEL=1` in the service environment. These are server-wide admission controls, while the YAML fields above bound each Researchguy request.

Override per-command:

```bash
researchguy dive "topic" --backend ollama --model llama3
```

### Codex

Uses the [Codex CLI](https://github.com/openai/codex) through `codex exec`. Each call runs in a fresh empty scratch dir with a read-only sandbox, `--ephemeral`, and the prompt on stdin. The system prompt is folded into the prompt because `codex exec` has no flag for it. When the request has tools, Codex's built-in live web search is on; otherwise it is off. The backend reads `codex exec --json`, so every web search result (title, URL, snippet) is kept as raw evidence, which the hybrid aggregator gets in its ledger.

`ignore_user_config: true` (the default) skips `~/.codex/config.toml`, so worker runs don't start your Codex MCP servers, researchguy among them. Auth still comes from `CODEX_HOME`. With it on, set `model` explicitly, since your config's default model is skipped too.

```bash
codex login
researchguy dive "topic" --backend codex --model gpt-5.6-sol
```

The usual use is Codex as the hybrid worker backend with Claude aggregating:

```yaml
codex:
  reasoning_effort: high
hybrid:
  worker_backend: codex
  worker_models: [gpt-5.6-sol]
  aggregator_backend: claude
  aggregator_model: opus
  max_parallel: 5
store:
  dir: ~/.researchguy/store  # share the main config's store
```

Keep that in its own config dir (`RESEARCHGUY_CONFIG_DIR=~/.researchguy/codex-hybrid researchguy dive ...`) if the MCP server should keep its default stack. A dive doesn't touch `tasks.db`, so a separate config dir is safe for it. Graph and scheduler commands do read `tasks.db` from the config dir, so run those with your main config. Set `store.dir` as above so its run records land in the same store as the main config's.

### Hybrid backend: modes and critics

The hybrid backend fans out to worker models, unloads them, runs one aggregation stage, then optionally runs two flag-only critic passes:

```bash
researchguy dive "topic" --backend hybrid --mode inquiry --branches 5
```

- `--mode landscape` — for tool/alternatives-comparison questions. Branches: documented alternatives, vendor claims vs. independently reported usage, competitive positioning, adoption evidence.
- `--mode inquiry` — for open-ended or contested claims. Branches: primary evidence, counter-evidence/disconfirming cases, funding and institutional provenance, independent replication, narrative-vs-evidence gap.
- `--branches <n>`: how many angles to investigate, decoupled from how many worker models are configured. Models are reused round-robin if `n` exceeds `worker_models` length.

Without `--branches`, a named mode covers its complete role set: four branches for `landscape` and five for `inquiry`. General mode defaults to the number of configured worker models. Set `--branches` explicitly when cost or latency matters more than full role coverage.

Worker prose is analysis, not evidence. Successful tool results form an evidence ledger that is passed separately to the aggregator and optional critics. Every item gets an ID (`E1`, `E2`, ...) numbered across all workers, and the aggregator cites items by it (`[E12]`). The whole ledger is written to the run record (`captures.jsonl`, below) before aggregation, under the same IDs.

`hybrid.max_evidence_chars` caps how much of the ledger one prompt receives. `0` (the default) sizes it to the backend that reads it: ~2M chars for claude, which is ~500-670k tokens and leaves room in Opus's 1M-token window for the drafts and the report, and 80000 for Ollama or Codex. Set it explicitly for a smaller-window claude model such as haiku. The cap is split evenly across shards, and a shard that needs less than its share passes the rest to the others, so the first shard can't fill the ledger and starve the counter-evidence branches. When the critics' backend has a smaller budget than the aggregator's, they get their own cut of the ledger. Run metadata records `evidence_ledger` (items and chars captured vs. passed, and how many shards made it in), plus `critic_evidence_ledger` when the critics got a different cut.

The aggregator writes its report between a `===BEGIN REPORT===` line and an `===END REPORT===` line. Text outside them, such as status chatter or offers to save a file, is left out of the report and kept in the run record's `aggregator-output.md`. Metadata `aggregator_output.markers` is `ok`, `unterminated` (no end line, so everything after the begin line is kept), `missing` or `empty` (the whole output is kept), and a warning goes to stderr when it isn't `ok`.

Both critic passes leave the answer body unchanged and write their findings under `## Critic Notes`:

- **Groundedness critic**: flags factual claims that the raw evidence ledger does not support or that overstate it.
- **Narrative-vs-evidence critic**: flags claims stated as settled or consensus that are not tied to a distinct ledger entry.

Verification is off by default. When enabled, use the small verifier model and treat its output as review notes, not an automatic correction. Ollama timing and token counts are retained in run metadata, including per-worker telemetry inside hybrid metadata.

`docs/bakeoff-2026-09-25.md` compares this backend with bookworm's researcher on cost, time, sourcing, and critic value; `go run ./tools/bakeoff` is the measuring tool it used.

## File Organization

Research files are automatically categorized by `ollama.utility_model` into topic directories with short descriptive filenames. This bypasses the research provider, so a hybrid run does not fan out again just to choose a path:

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

Each report's header line names its run (`*Generated: ... | Backend: hybrid | Run: 20261002T153045Z-a1b2c3*`), which is where its evidence is.

## Run records

Every `ask`, `dive`, `review`, `compare`, `enrich` and `watch` task, from the CLI, the MCP server or the scheduler, gets a run directory in the research store (`store.dir`, default `<config dir>/store`):

```
<store.dir>/runs/<run_id>/       # run_id: UTC start time + random suffix
  run.json                       # kind, topic, backend, mode, status, timing, report_path, metadata
  captures.jsonl                 # one raw tool result per line: seq, worker, shard, backend, model, label, content
  workers.jsonl                  # hybrid: each worker's full draft, error and metadata
  aggregator-prompt.md           # hybrid: the exact system and user prompt the aggregator got
  aggregator-output.md           # hybrid: the aggregator's raw output, before the report was cut out
```

`run.json` is written as `running` when the task starts and rewritten as `succeeded` or `failed` when it ends, so a failed run keeps what it collected and says why. A run whose process was killed stays `running`. Captures are fsynced, and the other files are written to a temp file and renamed. A capture's `seq` is the report's citation: `[E12]` is the line with `"seq":12`. `--json` output and the MCP research tools include `run_id` and `run_dir`. If the store can't be written, the task still runs and a warning on stderr says its evidence isn't being kept.

This is phase 1 of `docs/research-store-design.md`. The index, fetching and retrieval over the store come in later phases.

To migrate existing flat directories (e.g. `long-slug-topic-name/README.md`) into the new structure:

```bash
researchguy migrate --dry-run   # Preview changes
researchguy migrate             # Apply changes
```

## Critique

`researchguy critique` runs the hybrid backend's two critics against any text
and evidence files, in flag mode: it lists problems and rewrites nothing.

- **Groundedness** lists factual claims (names, dates, numbers, attributions)
  the evidence does not support or overstates. A flag means "not found in the
  evidence given", not "false": the 2026-09-25 bake-off
  (`docs/bakeoff-2026-09-25.md`) found the hybrid backend's rewrite-mode
  groundedness critic deleting accurate facts that were simply missing from
  the worker drafts.
- **Narrative vs. evidence** lists claims stated as settled or consensus that
  aren't tied to a distinct piece of evidence. In the bake-off it caught a
  summary contradicting the report's own body.

Evidence files are read in order up to `--max-evidence-chars` (default
120,000); the file crossing the cap is cut, later ones are skipped, and
`--json` reports both (`truncated`, `skipped`). `--json` prints `{text,
evidence, evidence_chars, truncated, backend, groundedness, narrative,
flags}`. bookworm runs it on approved chapters when `critique.policy` is set.
The prompts live in `internal/critique`, shared with the hybrid backend.

## MCP Server

The `researchguy mcp` command starts a [Model Context Protocol](https://modelcontextprotocol.io/) server over stdio, exposing researchguy's capabilities as tools that any MCP client can call.

### Tools

| Tool | Description |
|------|-------------|
| `researchguy_ask` | Ask a question with optional research context |
| `researchguy_dive` | Generate a deep research report |
| `researchguy_review` | Literature review / synthesis |
| `researchguy_compare` | Side-by-side comparative analysis |
| `researchguy_enrich` | Expand and add context to a document |
| `researchguy_critique` | Flag claims in `text` (or `text_path`) that `evidence_paths` don't support; paths resolve like `researchguy_read` |
| `researchguy_search` | Raw semantic search across research (chunk results, no synthesis) |
| `researchguy_context` | Search + freshness filter + read + format into ready-to-use context |
| `researchguy_list` | List research files by category |
| `researchguy_read` | Read a research document, a search hit's `file_path`, or a file under `read_roots`. Capped at 100KB; `start_line`/`end_line` read part of a large file |
| `researchguy_graph_list` | List graph nodes, optionally by type (default limit 100) |
| `researchguy_graph_show` | One node with its outgoing and incoming edges |
| `researchguy_graph_find` | Find nodes by `title`, `path`, or a metadata key (e.g. `url`) |
| `researchguy_graph_add_node` | Create a node |
| `researchguy_graph_add_edge` | Create an edge between existing nodes |

`researchguy_dive`/`_review`/`_compare` accept `no_research`/`max_age` params (same semantics as the CLI flags), plus `backend` (`claude`, `ollama`, `codex`, `hybrid`), `mode` (`landscape`, `inquiry`), `branches`, and `projects`. `mode`/`branches` only apply with the hybrid backend; with any other backend the result carries a `warning`. `researchguy_ask`, `_search`, and `_context` accept `projects`; `researchguy_ask` additionally accepts `no_save`. `max_age: none` disables the freshness filter.

### Claude Code Configuration

Add to `~/.claude/settings.json`:

```json
{
  "mcpServers": {
    "researchguy": {
      "command": "researchguy",
      "args": ["mcp"]
    }
  }
}
```

### Config Directory

```
~/.researchguy/
  config.yaml          # Configuration
  tasks.db             # Scheduler tasks + knowledge graph (nodes/edges) tables
  tasks.db-wal/-shm    # WAL sidecars; back up with `sqlite3 tasks.db ".backup <dest>"`, not cp
  scheduler.log        # Daemon log
  scheduler.pid        # Daemon PID file
  store/runs/          # Run records (store.dir), see "Run records"
```
