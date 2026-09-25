# researchguy (researchguy)

CLI + MCP server for research collection, storage, and retrieval. Generates
markdown research reports via LLM backends (claude, ollama, hybrid), stores
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
- A `lead` node is an unconfirmed breadcrumb (forum comment, offhand
  mention), not evidence. Link it with `references` to what it concerns;
  once confirmed, create a `claim` that `supersedes` it rather than editing
  the lead into a claim.
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
  `landscape`/`inquiry`), and the groundedness/narrative critic passes.
  Claude and Ollama backends are single-shot and ignore `Request.Mode`/
  `BranchCount` — that's intentional, not a gap to fix.
- After `go install ./cmd/researchguy`, smoke-test the actual binary
  (`researchguy <cmd> --help`, or a real run against a scratch
  `RESEARCHGUY_CONFIG_DIR`) — most command wiring bugs don't show up in unit
  tests alone.
