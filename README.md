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
- **Multi-backend**: supports Claude CLI, Codex CLI, Goose CLI, Ollama, and hybrid worker aggregation
- **Epistemic branch roles** — hybrid backend fan-out driven by a `--mode` (`landscape`, `inquiry`) instead of generic angles, plus a `--branches` effort/breadth dial
- **Evidence-ledger critics**: hybrid verification checks the unchanged draft against raw successful tool results and reports flags instead of rewriting
- **Run records**: every task writes what it collected (raw tool results, worker drafts, the aggregator's prompt and raw output, metadata) to `<store.dir>/runs/<run_id>/`, so evidence that didn't fit in a prompt is still on disk and a report's `[E12]` citations resolve to a captured item
- **Claims**: checkable claims extracted from fetched documents by a local model, each with a quote checked against the document's text, linked across independent origins, and flagged when contested, superseded, single-origin or possibly outdated
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
| `find <query>` | Search the passages and claims of fetched documents, and reports, in the index, with publication and collection dates. Needs `store.dsn`; see [Retrieval](#retrieval) |
| `timeline <query \| C:id>` | The claims on a question and their linked claims, oldest first, marking where a later claim supersedes or contradicts an earlier one. Never calls an LLM |

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
| `store init` | Create the index database in `store.dsn` and apply its schema |
| `store ingest [--run <id>] [--force]` | Mark dead runs interrupted, then index new or changed runs |
| `store fetch [--run <id>] [--force] [--budget <d>]` | Fetch the sources of runs whose fetching isn't done, then index them |
| `store embed [--budget <d>]` | Embed indexed passages, then claims, that have no vector from `store.embed.model` |
| `store extract [--budget <d>] [--limit <n>]` | Extract claims from indexed documents that have none from `store.claims.model` yet |
| `store link [--budget <d>]` | Label how unchecked claims relate to their nearest claims from other origins |
| `store link set <from> <to> <relation> [--note <s>]` | Record your own label for a pair of claims, over the model's |
| `store claim <C:id>` | A claim with its quote, passage, flags and linked claims |
| `store rebuild` | Empty the index and re-index every run, in one transaction |
| `store doctor` | Check the store and index for problems; changes nothing, exits non-zero on a problem |

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

# LLM backend: "claude", "ollama", "codex", "goose", or "hybrid"
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

# Goose CLI settings (blank provider/model = what Goose is configured with)
goose:
  binary: goose
  provider: ""
  model: ""
  max_turns: 40

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
# Blank means <config dir>/store. dsn is the Postgres index built from it;
# blank means no index.
store:
  dir: ""
  dsn: ""   # e.g. postgres://localhost:5432/researchguy
  fetch:              # fetch each run's cited, opened and top-ranked pages
    enabled: true
    top_results: 3    # per search
    concurrency: 8
    timeout: 20s      # per request
    budget: 3m        # per run; "0" is no limit
    openalex: true    # abstracts and open-access copies for blocked DOIs
  embed:              # passage vectors, needs dsn and Ollama
    model: nomic-embed-text
    host: ""          # blank uses ollama.host
    budget: 2m
  claims:             # claim extraction through Ollama, between tasks
    enabled: true
    model: ""         # blank uses ollama.utility_model, then ollama.model
    host: ""          # blank uses ollama.host
    budget: 10m       # per daemon pass
    chunk_chars: 6000
    max_chunks: 8     # per document
    max_attempts: 3   # per chunk the model fails on
    link:             # labeling how claims relate, through an llm backend
      enabled: true
      backend: claude
      model: sonnet
      neighbors: 5
      min_similarity: 0.75
      batch_size: 20  # pairs per call
      max_pairs_per_day: 500
  volatile_max_age: 30d   # older volatile claims are flagged possibly_outdated
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

`--safe-mode` also turns off the MCP servers `--mcp-config` names, so a call that gets MCP tools (the hybrid aggregator's read-profile tools, below) uses `--setting-sources ""` with `--strict-mcp-config` instead. That loads no user, project or local settings, so CLAUDE.md, memory, hooks and other MCP servers stay out, and a tool that isn't allowed is denied (checked against Claude Code 2.1.282). It also skips the `env` block in `~/.claude/settings.json`: if `CLAUDE_CODE_OAUTH_TOKEN` is only there, the call fails with "Not logged in", and researchguy retries it once under `--safe-mode` without the tools, with a warning.

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

### Goose

Uses the [Goose CLI](https://github.com/block/goose) through `goose run`. Each call runs in a fresh empty scratch dir with `--no-session` and `--no-profile`, so none of your Goose extensions load: not the developer shell, and not a researchguy MCP server that could start research inside research. The prompt goes on stdin and the system prompt through `--system`. Blank `provider` and `model` leave Goose's own configured ones in place.

When the request has tools, the one extension Goose gets is `researchguy mcp --profile web` (below), run from the same researchguy binary: `web_search` and `web_fetch`, the keyless search and page fetch the Ollama workers call. The backend reads `goose run --output-format stream-json`, so every successful search (ranked titles, URLs, snippets) and fetched page is kept as evidence the hybrid aggregator gets in its ledger, captured as each result arrives. A failed tool result is counted in the metadata (`tool_errors`) but isn't evidence.

```bash
researchguy dive "topic" --backend goose
```

As the hybrid worker backend, leave `worker_models` empty to run every shard on `goose.model`, or Goose's own default when that's blank:

```yaml
hybrid:
  worker_backend: goose
  worker_models: []
  aggregator_backend: claude
  aggregator_model: opus
  max_parallel: 3
```

`worker_models` are Goose model names here, so an Ollama model list left over from an Ollama worker setup goes to Goose as `--model` and fails. The separate config dir advice under Codex applies the same way.

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

A worker that had web tools and came back without one successful tool result is marked failed ("worker ran no successful tool calls"): its draft stays in the run record (`workers.jsonl`) but doesn't reach the aggregator, and a run where every worker came back empty fails. A local model can write a draft that describes searches it never ran; nothing in it can be checked against the ledger. Claude workers don't report their evidence, so the check skips them.

`hybrid.max_evidence_chars` caps how much of the ledger one prompt receives. `0` (the default) sizes it to the backend that reads it: ~2M chars for claude, which is ~500-670k tokens and leaves room in Opus's 1M-token window for the drafts and the report, and 80000 for Ollama or Codex. Set it explicitly for a smaller-window claude model such as haiku. The cap is split evenly across shards, and a shard that needs less than its share passes the rest to the others, so the first shard can't fill the ledger and starve the counter-evidence branches. When the critics' backend has a smaller budget than the aggregator's, they get their own cut of the ledger. Run metadata records `evidence_ledger` (items and chars captured vs. passed, and how many shards made it in), plus `critic_evidence_ledger` when the critics got a different cut.

With `store.dsn` set, the runner fetches, indexes and embeds what the workers found before the aggregator runs (an ask skips the fetch), and the aggregator's prompt gets a table of the run's sources: each `[S:<id>]` with its domain, title, publication date, when it was collected, what text the store holds (full, abstract only, a failed fetch, or not fetched) and the ledger items that saw it. A claude aggregator also gets the read-only researchguy tools (`researchguy mcp --profile read`; `hybrid.aggregator_tools`, on by default), so it can run `researchguy_find` over the run's fetched text (`run_id`) or prior research and quote the passages it retrieves. Reports cite ledger items as `[E12]`, passages as `[P:<id>]` and sources as `[S:<id>]`, with a direct quote in double quotes right before its citation. The critics also get the text of every passage the draft cites. Metadata records `sources_table`, `aggregator_tools`, `cited_passages` and `before_aggregate_error`.

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
  captures.jsonl                 # one raw tool result per line, written as it arrives: seq, worker, shard, backend, model, tool, action, query, url, ranked results, label, content
  workers.jsonl                  # hybrid: each worker's full draft, error and metadata
  aggregator-prompt.md           # hybrid: the exact system and user prompt the aggregator got
  aggregator-output.md           # hybrid: the aggregator's raw output, before the report was cut out
  fetches.jsonl                  # one line per fetch attempt, failures included
  fetch.json                     # the last fetch pass: queued, fetched, failed, remaining
  citations.jsonl                # every citation in the report and what the citation check found
<store.dir>/blobs/sha256/<ab>/<hash>.gz         # raw fetched bytes, gzip
<store.dir>/text/sha256/<ab>/<hash>.txt         # extracted text
<store.dir>/vectors/<model>/<ab>/<hash>.f32     # embedding cache, keyed by passage text hash
<store.dir>/claims/<extractor>/<ab>/<hash>.json # claims extracted from a text, keyed by its hash
<store.dir>/links.jsonl                         # append-only log of how claims relate: by rule, model or person
```

`run.json` is written as `running` when the task starts, with the process ID and host, and rewritten as `succeeded` or `failed` when it ends, so a failed run keeps what it collected and says why. A run whose process was killed is marked `interrupted` by the next `store ingest` or daemon pass. Each tool result is appended as it arrives, so a worker killed mid-run keeps what it had collected. Captures are fsynced, and the other files are written to a temp file and renamed. A capture's `seq` is the report's citation: `[E12]` is the line with `"seq":12`. `--json` output and the MCP research tools include `run_id` and `run_dir`. If the store can't be written, the task still runs and a warning on stderr says its evidence isn't being kept. When a run succeeds, its section of the report is copied into `text/` as well (`report_sha256` in `run.json`), so the index has it even if the file moves or changes.

### Citation check

After a task writes its report, every citation in the run's section of it is checked, with no model involved: `[E<n>]` against the run's captures, `[P:<id>]`, `[C:<id>]` and `[S:<id>]` against the index. A quote cited to a claim is checked against the document text around it, not the claim's wording, and the citation's note says when the claim is contested, superseded or possibly outdated. A double-quoted string of 4 or more words right before a citation is checked against the cited text, ignoring case, punctuation and spacing, and `...` may skip text. Citations in one bracket, or in adjacent brackets, share the quote, and it passes when any of them holds it. A quote missing from a search result's snippets, an abstract-only document or a source with no stored text is `unverifiable`, not `not_found`, because the worker may have read more than the store has. Results go to the run's `citations.jsonl` and the index's `citations` table. Citations that don't resolve, and quotes none of their citations hold, are listed under `## Citation Check` at the end of the report (under `## Critic Notes` when it has them). Without an index, passages and sources are left unchecked.

### Fetched documents

When a `dive`, `review`, `compare`, `enrich` or `watch` ends, researchguy fetches the run's sources itself (`store.fetch`): the URLs its report and worker drafts cite, the pages its workers opened, and the top 3 results of each search. Asks don't wait on this; the daemon or `researchguy store fetch` gets to them. The raw bytes and the extracted text are kept in full. HTML is reduced to its main content, and PDFs go through `pdftotext` (`brew install poppler`; without it PDFs are recorded as failures). A DOI whose publisher blocks the fetch is looked up on OpenAlex, which gives an abstract (stored as `content_kind: abstract`) and repository or open-access copies to try. Each document's publication date comes from the page's own metadata, the URL or OpenAlex, with where it came from and its precision. `Last-Modified` and PDF creation dates are marked weak, and an unknown date stays blank, never the fetch date.

Every attempt goes in the run's `fetches.jsonl`, so a 403 or a timeout is recorded rather than lost. A fetch pass is capped by `store.fetch.budget`; what it doesn't reach is fetched by the next pass. The fetcher spaces requests to each host, retries 429s and 5xx with backoff, and refuses private addresses unless `RESEARCHGUY_ALLOW_PRIVATE_URLS=true`. It doesn't try to get past bot challenges: on 2026-10-02 PubMed started serving one (HTTP 203) to every request from this machine, and those fetches are recorded as failures.

### Index

The store is the record of truth. With `store.dsn` set, runs are also indexed in Postgres: runs, captures, sources (one row per normalized URL, classified `web`, `paper`, `video` or `court`), sightings (each time a run saw a source, with its rank and whether it was opened or fetched), every fetch attempt, documents (each distinct text of a source, with its publication date), and passages: each document split into ~1,500-character passages with character offsets, full-text indexed, and embedded with `store.embed.model` through Ollama (`ollama pull nomic-embed-text`). Each finished run's report is a document too, with `origin: synthesis`, under the source `researchguy:run/<id>`, and the report's checked citations are in `citations`. The index is derived, so `store rebuild` can always recreate it, and vectors are cached under `vectors/`, so a rebuild doesn't embed again.

```bash
# store.dsn: postgres://localhost:5432/researchguy in config.yaml, then:
researchguy store init      # create the database, apply the schema (needs pgvector)
researchguy store ingest    # index runs already in the store
researchguy store fetch     # fetch sources for runs that haven't had them
researchguy store embed     # embed passages and claims without a vector
researchguy store extract   # extract claims from documents waiting on them
researchguy store link      # label how claims from different sources relate
researchguy store doctor    # check both, count failed report citations and claim work waiting
```

Runs recorded before the index existed have captures but no structured tool calls, so they index without sources. A task indexes its run when it finishes, then embeds its new passages within `store.embed.budget`. If the index or Ollama is down, the run is still saved, a warning says so, and `researchguy daemon start` or the `store` commands catch it up later. The daemon's pass fetches up to 4 runs still waiting on fetching, indexes what's new, then embeds. Ingest is idempotent: an unchanged run is skipped and a replayed one changes nothing.

This is phases 1-5 of `docs/research-store-design.md`.

### Claims

`researchguy store extract` asks a local model (`store.claims.model`, else `ollama.utility_model`, else `ollama.model`) for the checkable claims in each indexed primary document, documents a report cites first. Each claim comes with the quote the model says states it, an `as_of` date when the text gives one, and whether it's volatile (a status, a count to date, a price). The index looks the quote up in the document's text and records its character span and passage; a claim whose quote isn't there is kept but never shown as quoted, since a model that drops a parenthetical or fills in a number the text doesn't have isn't quoting. Extractions are written to `claims/` as they finish, so `--budget` or a stop keeps the work done, and a chunk the model fails on is retried up to `store.claims.max_attempts`.

`researchguy store link` compares each claim with its nearest claims from other origins (at least `min_similarity` close and sharing a word) and has `store.claims.link.model` label each pair `same`, `supports`, `contradicts`, `refines`, `supersedes` or `unrelated`, at most `max_pairs_per_day` pairs a day. Two claims quoting the same words are linked `same` by rule, with no model. Documents that are one origin repeated (versions of a source, mirrors, wire copy, near-duplicates) are grouped, so ten pages repeating one press release count once. Labels go to `links.jsonl`; `researchguy store link set C:123 C:456 supersedes` records your own, which outranks the model's, and `unrelated` overrules a wrong one.

The daemon extracts and links between research tasks: it doesn't start while a task runs, stops extracting before its next chunk when one starts, and unloads the extraction model when it's done.

### Retrieval

`researchguy find <query>` (and `researchguy_find` over MCP) searches every passage in the index, from fetched documents and from reports. It ranks by full-text match and by embedding similarity, fused, discounts passages that are mostly repetition (figure labels, token tables), and returns at most 2 passages per document. Each card has its `P:<id>` ref, its source, the publication date when the source states one, when it was collected, and whether it's primary evidence or researchguy's own synthesis. Nothing is left out for its age unless a filter asks: `--since`/`--until` bound the publication date (or the collection date with `--date collected`, and a publication bound leaves out undated documents), `--as-of` keeps what had been collected by then, and `--run`, `--domain`, `--kind` and `--min-similarity` narrow it. `--prefer-recent 1y` halves a card's score per year of age. If the embedding model is down the search is full-text only, and the result says so.

```bash
researchguy find "nicotine patch trial outcomes" --since 2023
researchguy store passage P:4821193310755112 --neighbors 2   # the text around a card
researchguy store document 7075171269261622582 --offset 8000 # a document, paged, with its versions and fetches
researchguy store source https://doi.org/10.1145/3290605.3300830
researchguy store ingest-url https://example.org/paper       # fetch one URL into the store now
```

Claims come back as `claim` cards with their `C:<id>` ref, the quote as it appears in the document, and flags from the claims linked to them: `reinforced` (two or more independent origins say it), `single_origin` (however many pages repeat it, it traces to one), `contested`, `newer_contradiction` (the newest contradiction is dated after the newest support), `superseded` (shown under the claim that supersedes it) and `possibly_outdated` (volatile and older than `store.volatile_max_age`). Each linked claim is listed with how the link was made, and a model's label says it's model-labeled: it's an inference. Volatile claims are ranked with a 1-year half-life unless `--prefer-recent` sets one. `researchguy store claim C:<id>` shows a claim with its passage and every linked claim, and `researchguy timeline <query | C:id>` lays out the claims on a question and their linked claims oldest first, marking where a later claim supersedes or contradicts an earlier one.

```bash
researchguy find "bridge toll price" --kind claim
researchguy store claim C:3517454840306805788
researchguy timeline "bridge toll price" --since 2020
```

`researchguy context`, `researchguy_context` and the runner's research context include store evidence from `find` next to the grepai report excerpts, each labeled primary evidence or synthesis, with its dates. Store evidence is only filtered by age when a max age is passed explicitly.

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
| `researchguy_find` | Ranked passages and claims from fetched documents, and report passages, with dates and claim flags; params match `find`'s flags (needs `store.dsn`) |
| `researchguy_passage` | A passage (`P:<id>`) with up to 5 passages on each side |
| `researchguy_claim` | A claim (`C:<id>`) with its quote, its passage, its flags and every linked claim, with how each link was made |
| `researchguy_timeline` | The claims on a query, or around a `C:<id>`, and their linked claims, oldest first, with where the evidence changed |
| `researchguy_document` | A document's text from `offset`, up to 50,000 chars, with its versions and fetch attempts |
| `researchguy_source` | A source by URL, id or `S:<id>`: its documents, sightings, fetches and the runs that cited it |
| `researchguy_ingest_url` | Fetch a URL into the store now, attached to `run_id` or a new `manual` run, and index and embed it |
| `researchguy_graph_list` | List graph nodes, optionally by type (default limit 100) |
| `researchguy_graph_show` | One node with its outgoing and incoming edges |
| `researchguy_graph_find` | Find nodes by `title`, `path`, or a metadata key (e.g. `url`) |
| `researchguy_graph_add_node` | Create a node |
| `researchguy_graph_add_edge` | Create an edge between existing nodes |

`researchguy mcp --profile read` serves only the tools that read: search, context, list, read, graph list/show/find, find, passage, claim, timeline, document and source. It can't start research or write anything; the hybrid aggregator gets this profile.

`researchguy mcp --profile ingest` adds `researchguy_ingest_url` to the read tools, for an agent researching a topic (bookworm's researchers get it): it can search and fetch pages into the store, but can't start research or write the graph.

`researchguy mcp --profile web` serves only `web_search` and `web_fetch`: no store, no graph, no research. A Goose worker gets this profile. `web_search` returns its ranked results as structured content along with the text list.

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
  store/blobs/ text/ vectors/  # Fetched bytes, extracted text, embedding cache
```
