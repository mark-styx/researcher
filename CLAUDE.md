# researchguy (researchguy)

CLI + MCP server for research collection, storage, and retrieval. Generates
markdown research reports via LLM backends (claude, ollama, codex, hybrid), stores
them under `research_dir`, and indexes them via grepai. Structured
relationships (entities, sources, claims, funding-pattern observations,
reports) live as nodes/edges in `~/.researchguy/tasks.db` (`internal/graph`).
With `graph.rollup.enabled: true`, `researchguy daemon start` also
periodically re-summarizes nodes whose linked file changed (`internal/rollup`)
— proposals go to `graph approve`, never a silent overwrite. Off by default.

## Before generating new research on a topic

Check what already exists first. Re-deriving something already researched
wastes tokens and tends to drift from what's already there instead of
extending it.

- MCP: call `researchguy_context` (or `researchguy_search`) for the topic
  before calling `researchguy_dive`/`researchguy_review`/`researchguy_compare`.
  Those three already do this internally by default (see
  `internal/research/runner.go`'s `gatherResearchContext`) — only skip this
  step yourself if you're calling the LLM backends directly rather than
  through the runner.
- CLI: `researchguy search <topic>` or `researchguy graph list --type entity`
  before creating a new entity/source/funding-pattern node — check whether
  the thing you're about to research already has a node
  (`researchguy graph show <id>` to inspect it) rather than creating a
  duplicate. There's no automated entity-resolution/dedup yet (see the graph
  storage commit for why), so this check is manual until that exists.
- Book bibliographies are in the graph (`graph import-sources`). Before
  creating a `source` node for a URL, resolve it with `graph show <url>` or
  `researchguy_graph_find` (key `url`): both match normalized variants, so
  a hit means the source already exists and should get a new edge, not a
  duplicate node. Register new identity keys with `Store.SetNodeKey`.
- A `lead` node is an unconfirmed breadcrumb (forum comment, offhand
  mention), not evidence. Link it with `references` to what it concerns;
  once confirmed, create a `claim` that `supersedes` it rather than editing
  the lead into a claim.
- Research older than `ask.max_age` (90d by default) is filtered out of
  context. Pass `max_age: none` (MCP) or `--max-age none` (CLI) when older
  material matters, such as past book research reached through `projects`.
- `--no-research` / `no_research` on the CLI and MCP tools exists for the
  cases where you deliberately want a from-scratch answer; don't reach for
  it as a default.

## Working in this codebase

- `go build ./...` / `go test ./...` before considering any change done.
- Open `tasks.db` through `internal/sqlitedb.Open`, never `sql.Open`
  directly: parallel researchguy processes (daemon, MCP server, agents
  running `graph add-node`) need its busy timeout and WAL mode.
- `gofmt -l <files>` before committing — CI has no separate lint step, this
  is the only formatting gate.
- The hybrid backend (`internal/llm/hybrid.go`) is the one place with real
  parallel LLM fan-out, epistemic branch-role planning (`Mode`:
  `landscape`/`inquiry`), staged model unloading, a raw tool-result evidence
  ledger, and flag-only groundedness/narrative critic passes. Named modes use
  their full role set unless `BranchCount` is explicit.
  Claude, Codex and Ollama backends are single-shot and ignore `Request.Mode`/
  `BranchCount`. That's intentional, not a gap to fix. Codex
  (`internal/llm/codex.go`) is the hybrid worker option that isn't local:
  it parses `codex exec --json` so its web search results reach the ledger.
- Every runner task writes a run record through `internal/store`
  (`<store.dir>/runs/<id>/`): `run.json`, every raw tool result in
  `captures.jsonl`, and for hybrid the worker drafts and the aggregator's
  prompt and raw output. Prompt caps (`hybrid.max_evidence_chars`) limit
  what one prompt sees, never what's kept: write captures before anything
  that can fail, and don't add a path that drops collected evidence. Ledger
  IDs (`E<seq>`) are the capture `seq`, and the report header names the run.
  A provider with tool calls streams each result through `Request.Capture`
  as it arrives; the run assigns seqs, so never number captures yourself.
- The Postgres index (`internal/store/index`, `store.dsn`) is derived from
  the store and must stay rebuildable from it: nothing gets written only to
  the index. Ingest must stay idempotent. Schema changes are new numbered
  files in `internal/store/index/migrations/`, never edits to an applied
  one. Index tests use `indextest.DSN`, which makes a throwaway database and
  skips without Postgres (`RESEARCHGUY_TEST_PG` points elsewhere).
- The fetch stage (`internal/fetch`) writes every attempt, failures
  included, to the run's `fetches.jsonl`, and text and vectors to the
  store's `text/` and `vectors/`. Ingest builds documents and passages from
  those files only, never from the network, so keep it that way. A blocked
  fetch, paywall or bot challenge is recorded as a failure. Don't add code
  that tries to get past one. The passage split must stay deterministic
  (passage IDs hash the span). Fetch and embed tests use `httptest` with
  `RESEARCHGUY_ALLOW_PRIVATE_URLS=true` (or `Client.AllowPrivate`) and a
  fake `/api/embed`; no test reaches the internet or a real Ollama.
- The Claude provider runs `claude -p` in an empty temp dir with the prompt
  on stdin, the system prompt in a file, an explicit `--tools` list and (by
  default) `--safe-mode`. Don't move prompts back onto argv (1 MiB
  `ARG_MAX`). The hybrid aggregator's report is cut from between
  `===BEGIN REPORT===`/`===END REPORT===`; keep that contract in its prompt.
- Context retrieval lives in `search.BuildContext`; the MCP context tool,
  `researchguy context`, and the runner all use it. Keep it LLM-free.
- Commands with `--json` must print nothing but the JSON on stdout. Send
  progress and warnings to stderr.
- grepai workspace hits come back as `<workspace>/<project>/<rel>`. Resolve
  them with `search.Resolver` (reads `grepai.workspace_file`), never by
  joining onto `research_dir`.
- After `go install ./cmd/researchguy`, smoke-test the actual binary
  (`researchguy <cmd> --help`, or a real run against a scratch
  `RESEARCHGUY_CONFIG_DIR`) — most command wiring bugs don't show up in unit
  tests alone.
