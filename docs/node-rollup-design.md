# Node rollup: design (not yet built)

Phase 5 of the researchguy plan. Gives the currently-dormant scheduler a job:
periodically re-summarize graph nodes whose linked content has changed,
instead of leaving `Summary` stuck at whatever it was when the node was
created. Deferred to a design pass before implementation because it's an
LLM writing to stored knowledge on a timer, not a bounded one-shot command
like everything else built so far.

## Trigger condition

mtime-based, not a fixed re-check interval. For every node with a non-empty
`Path`: if `mtime(research_dir/Path) > node.UpdatedAt`, the node is stale —
its linked markdown changed since the node was last summarized. This
directly captures "new information arrived" rather than re-summarizing
unchanged content on a schedule.

Nodes with empty `Path` (thin nodes, e.g. some `report` nodes) are never
stale by this rule; they have nothing to re-summarize from.

`graph.Store` needs a `ListStale() ([]*Node, error)` query. SQLite can't
stat the filesystem itself, so this has to be: fetch all nodes with
`path != ''`, stat each file, compare mtimes in Go. Fine at the node counts
this tool will realistically have; revisit if it doesn't scale.

## Review before overwrite

Do not let the rollup pass overwrite `Summary` directly. Write the proposed
resummarization into `Node.Metadata["pending_summary"]` and set
`Metadata["needs_review"] = true` — the same flag name the original design
reserved for low-confidence entity-resolution matches, reused here for the
same reason: a machine-proposed change to stored knowledge that hasn't been
confirmed yet.

A human confirms via a new `researchguy graph approve <id>` command: copies
`pending_summary` into `Summary`, clears `needs_review`, bumps `UpdatedAt`.
`researchguy graph list` should visibly flag nodes with `needs_review: true`
(e.g. a marker column) so they don't sit invisibly forever.

Auto-apply without review is a plausible v2 for low-stakes node types
(thin `report` nodes) but not for `funding-pattern`/`claim` nodes, which
are exactly the node types the whole epistemic-rigor design exists to keep
honest. Don't build the auto-apply tier until the review-gated version has
actually been used.

## Failure handling

- LLM call fails: leave the node untouched, skip, retry next poll cycle.
  No new retry/backoff infrastructure — matches how the existing scheduler
  already just tries again next tick.
- Linked file no longer exists: don't delete the node (destructive to
  in-degree/out-degree edges pointing at it). Set `Metadata["orphaned"] =
  true`. Surfacing orphaned nodes in `graph list` is a follow-up, not part
  of this pass.
- Cap nodes-per-poll-cycle (config field, e.g. `graph.rollup.max_per_cycle`)
  so one poll tick can't silently burn through LLM budget resummarizing
  everything at once.

## Where it runs

Not the existing `tasks` table (cron/queue) — that models user-initiated
research tasks, and a rollup pass isn't one. Runs as its own poll loop
inside `researchguy daemon start`, gated by a new config flag
(`graph.rollup.enabled`, default `false`) so it stays off until it's had
real use.

## Resummarization prompt

Needs the node's `Type` to select instructions — a `funding-pattern` node's
resummarization has to preserve the `EXPLICIT NON-CLAIM`/`SUFFICIENCY`
framing from the original design (correlation is not evidence of intent);
an `entity`/`source` node doesn't carry that constraint. One shared prompt
template with a type-specific clause, not a full prompt per node type.
