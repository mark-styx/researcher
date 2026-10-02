# Research store: design

Status: phases 1-3 built 2026-10-02 (see "Phase 1 as built", "Phase 2 as
built" and "Phase 3 as built" under Phases); phases 4-7 proposed. Decisions
accepted 2026-10-02.
Written 2026-10-02. "What exists today" describes the code before phase 1.

researchguy collects evidence and then loses it. Workers' tool results live in
process memory, one aggregator prompt reads a capped slice of them, and the
only durable output is the synthesized markdown report. Retrieval works over
those reports, dated by file mtime. This design replaces that with a store
where every collected item is written to disk before anything uses it,
indexed for fast semantic and full-text lookup, linked into a graph of
sources, documents, passages and claims, and dated well enough that
retrieval can say how old a piece of evidence is and what newer evidence
agrees or disagrees with it.

Scope is researchguy as it's used today: one user on one machine, with three
producers (researchguy's own `dive`/`ask`/`review`/`compare`/`watch`, alo's
`deep_research` workflow, and bookworm) and agents consuming it through the
MCP server and `researchguy context`. Not in scope: multiple users, cloud
sync, and code search, which stays with grepai.

## What exists today

Measured or read from the code on 2026-10-02.

| Area | Current behavior | Evidence |
|---|---|---|
| Raw evidence | Held in memory, passed to one prompt under a char cap, never written to disk | `hybrid.go:153`, `buildLedger` at `hybrid.go:432` |
| Cap | 400k chars in the codex-hybrid config; the six inquiry dives on 2026-10-02 captured 0.83-1.01M chars each and passed 40-60% | run metadata in `~/.researchguy/codex-hybrid/runs/2026-10-02/` |
| Why the cap exists | The Claude provider passes the whole prompt as one argv entry (`claude.go:32`); macOS `ARG_MAX` is 1048576. The Codex provider already uses stdin for this reason (`codex.go:122`) | `getconf ARG_MAX` |
| Codex evidence | Search results are title, URL and snippet. A page the model opened is recorded with no page text: a probe opening `go.dev/doc/effective_go` produced `"snippet":"Total lines: 1753"` | probe run, `codexSearchEvidence` at `codex.go:250` |
| Local worker pages | `web_fetch` text is cut at 50KB | `tools/webfetch.go:19` |
| Critics | Groundedness and narrative critics get the same capped ledger | `hybrid.go:207`, `hybrid.go:224` |
| Run metadata | Printed on stdout with `--json`, not saved anywhere | `cmd/researchguy/dive.go:80` |
| Report dates | A prose line (`*Generated: ...*`), no frontmatter | `runner.go`, `runDive` |
| Retrieval unit | grepai chunks of markdown files: reports and book research, not the evidence behind them | `search.BuildContext` |
| Freshness | File mtime. Hits older than `max_age` (90d) are dropped after grepai's top-k, so stale hits take result slots and then vanish | `search.go:239`, `context.go:73-76` |
| Graph | 5383 `source`, 49 `claim`, 28 `lead`, 9 `report`, 3 `entity` nodes; edge vocabulary includes `supports`, `contradicts`, `supersedes`, but only `references` edges exist (5726). The dive path writes nothing to the graph | `tasks.db` counts |
| Retrieval speed | grepai search on one project: 80-120 ms. `researchguy context` across 7 projects: 260 ms | timed |
| Vector index | grepai's `chunks` table (419,901 rows) has btree indexes only, so vector search is a full scan: ~200 ms warm over 64k rows, ~460 ms warm and 4.3 s cold over all rows | `pg_indexes`, `EXPLAIN ANALYZE` |
| Embeddings | `nomic-embed-text` (768d) via Ollama: 10-50 ms per query, ~0.95 s per batch of 64 passages of 1,500 chars (~67/s) | timed |

Two points from that table drive the design. Speed is fine today, and the
problem is what can be retrieved: syntheses, not evidence. And mtime is the
wrong clock. A `watch` file appends every update to one file, so its mtime
is its newest entry and its oldest entries pass the 90-day filter; editing or
copying a report makes old research look new.

## Requirements

1. **Nothing collected is dropped.** Caps apply only to what one prompt
   receives, and everything else stays retrievable.
2. **Collection survives failure.** A crash, a down database or a down
   embedding server loses nothing already captured.
3. **Evidence is the retrievable unit.** Agents can reach the source text,
   an exact quote and its location, not only a report's paraphrase.
4. **Everything is dated, with the date's origin.** When the source was
   published, when it was collected, and by which run.
5. **Retrieval is age-aware.** It shows age instead of silently dropping old
   material, and it groups reinforcing and competing evidence so the newest
   contradiction is visible next to the claim it contradicts.
6. **Fast and LLM-free at query time.** Retrieval stays as quick as today
   (low hundreds of ms) at much larger volume. The only model call is the
   query embedding, the same as grepai today, in keeping with the rule that
   `search.BuildContext` stays LLM-free.
7. **One access path for every consumer.** The runner, the MCP server, alo
   and bookworm all go through the same retrieval API.

## Overview

```
producers (hybrid workers, aggregator, deep_research agents, bookworm)
   |
   v
capture log + blob store (on disk, append-only, the record of truth)
   |
   v
ingest -> fetch -> extract text + dates -> passages -> FTS + embeddings
                                                   -> claims -> claim links
   |
   v
index (Postgres + pgvector, rebuildable from the store)
   |
   v
retrieval API (internal/store/retrieve): hybrid rank, graph expansion,
evidence clusters, age flags
   |
   v
MCP tools, researchguy CLI, runner context, aggregator tools
```

The split between the on-disk store and the index is the robustness
mechanism. Producers only ever append to the capture log and write blobs;
both are plain files written atomically. Everything in Postgres is derived
and can be rebuilt from the store with `researchguy store rebuild`, so a
schema change, a corrupted index or a down database is a delay, not a loss.

## Storage

### Index: Postgres + pgvector

The index lives in a new `researchguy` database on the local Postgres server
grepai already uses (Postgres 14, pgvector 0.8.1, both launchd services that
start at login). It is a separate database from grepai's, so neither can
break the other.

SQLite (where the graph lives today) was the other candidate. It loses on
two points. researchguy uses `modernc.org/sqlite`, a pure Go translation of
SQLite whose API has no extension loading (checked in v1.46.1). Getting
`sqlite-vec`, a native extension, would mean switching to a cgo driver;
otherwise vector search is a brute-force scan in Go. The full scan measured above already takes ~460 ms
warm at 420k rows, and the store will grow past that once it holds full
documents. And the daemon, the MCP server and parallel agents all write at
once, which is why `internal/sqlitedb` exists to manage busy timeouts;
Postgres handles that natively. Postgres also gives full-text search
(`tsvector`), JSONB and recursive CTEs for graph walks in the same query
engine, so hybrid retrieval is one SQL query instead of three systems merged
in Go.

The cost is that the full feature needs a running server. Degraded modes are
listed under failure handling.

New Go dependencies: `github.com/jackc/pgx/v5` (added in phase 2) and
`github.com/pgvector/pgvector-go` (phase 3, with the first vector column).

### On-disk store

```
~/.researchguy/store/                 (store.dir)
  runs/<run_id>/
    run.json                          run record and final metadata
    captures.jsonl                    one line per tool result, fsynced per line
    fetches.jsonl                     one line per fetch attempt, fsynced per line
    fetch.json                        the last fetch pass's summary
  blobs/sha256/<ab>/<hash>.gz         raw fetched bytes (HTML, PDF), gzip
  text/sha256/<ab>/<hash>.txt         extracted text
  vectors/<model>/<ab>/<hash>.f32     embedding cache keyed by text hash + model
```

Blobs are content-addressed and immutable, written to a temp file and
renamed. The embedding cache means a rebuild doesn't need to re-embed.

### Schema

The nesting runs source -> document version -> passage -> claim, with claims
linked to each other and to entities. Runs and reports hang off the same
nodes, so any report points at the passages it used and any passage points
back at every run that found it.

| Table | Holds | Key columns |
|---|---|---|
| `runs` | One row per research run from any producer | `id`, `kind` (`dive`, `ask`, `deep_research`, `bookworm`, `manual`), `topic`, `mode`, `backend`, `started_at`, `finished_at`, `status`, `report_document_id`, `meta` jsonb |
| `captures` | Every raw tool result, as captured | `run_id`, `seq`, `shard`, `worker`, `tool`, `action` (`search`, `open`, `fetch`), `query`, `url`, `payload` jsonb, `captured_at`; unique `(run_id, seq)` |
| `sources` | One row per thing that can be cited | `id`, `url_key` (via `graph.NormalizeURL`), `url`, `domain`, `title`, `kind` (`web`, `paper`, `court`, `book`, `video`, `report`), `doi`, `first_seen_at`, `node_id` |
| `fetches` | Every fetch attempt, including failures | `source_id`, `attempted_at`, `http_status`, `final_url`, `error`, `document_id` |
| `documents` | Distinct content versions of a source | `source_id`, `sha256`, `fetched_at`, `content_type`, `text_chars`, `published_at`, `published_precision` (`year`/`month`/`day`), `published_from`, `origin` (`primary`, `synthesis`), `content_kind` (`full`, `abstract`, `snippet`); unique `(source_id, sha256)` |
| `passages` | Chunks of a document's text with offsets | `document_id`, `ord`, `char_start`, `char_end`, `text`, `tsv` (generated), `embedding vector(768)`, `embed_model`; HNSW index on `embedding`, GIN on `tsv` |
| `claims` | Atomic assertions anchored to a verbatim quote | `text`, `quote`, `passage_id`, `char_start`, `char_end`, `quote_verified`, `as_of`, `volatile`, `extracted_at`, `extractor`, `embedding`; HNSW index |
| `claim_links` | Relations between claims | `from_claim`, `to_claim`, `relation` (`same`, `supports`, `contradicts`, `refines`, `supersedes`), `method` (`rule`, `model`, `human`), `confidence`, `model`, `created_at` |
| `origins` | Groups documents that are one origin repeated (syndication, mirrors, wire copy) | `id`, `document_id`, `reason` (`canonical`, `doi`, `near-duplicate`, `same-link`) |
| `citations` | Every citation marker in a report, resolved | `run_id`, `marker`, `target_kind`, `target_id`, `report_offset`, `quote`, `resolved`, `quote_found` |
| `nodes`, `edges`, `node_keys` | The existing graph, moved over in phase 6 | as in `internal/graph/store.go` |

Reports are documents too, with `origin = synthesis`. They stay searchable,
but retrieval never presents one as primary evidence.

## Collection

### Capture

Every tool result is appended to `runs/<run_id>/captures.jsonl` when it
arrives, before the worker continues. For Codex this means reading
`codex exec --json` stdout line by line instead of buffering it to the end
(`codex.go:73` today). For Ollama it means writing in the tool loop
(`ollama.go:233`). The Claude aggregator and deep_research agents capture
through the `researchguy_ingest_url` tool (below). The run record is written
at start and finalized at the end, including the metadata that today only
goes to stdout.

### Fetch

Snippets aren't enough to quote from, and Codex doesn't expose the pages it
reads, so researchguy fetches documents itself. Every URL a worker opened,
every URL cited in a worker draft or the final report, and the top results
of each search (default 3, configurable) go into a fetch queue. The fetcher:

- stores the raw bytes and the extracted text in full (the 50KB limit
  applies only to what a single prompt receives);
- extracts text from HTML (main content) and from PDFs with `pdftotext`
  (installed at `/opt/homebrew/bin/pdftotext`);
- records every attempt in `fetches`, so a 403, paywall or timeout is data
  rather than a gap, and the source keeps its snippet;
- rate-limits per domain and retries transient failures with backoff;
- stores a re-fetch with different content as a new document version, and
  keeps the old one.

Every other URL seen in results becomes a `sources` row with its snippet, so
it can still be found and fetched later. Fetch scope decides what is fetched
eagerly, not what is kept: a snippet-only source is fetched on demand when
retrieval surfaces it or an agent calls `researchguy_ingest_url`, and the gap
between `captured_at` and `fetched_at` is recorded.

Opened pages are identifiable from the capture: a Codex page open is a
`web_search` item with a non-search action and the page URL in
`results[0].url`. `codexSearchEvidence` currently builds the label from
`action.url`, which is empty for these, so phase 2 has to take the URL from
the result.

#### Measured fetch yield

A plain HTTP fetch (browser user agent, 8 parallel, 20 s timeout) of the 350
unique URLs cited in the six 2026-10-02 inquiry reports, on 2026-10-02:

| Outcome | Count | Notes |
|---|---|---|
| Usable text (3,000+ chars) | 148 (42%) | PubMed 30/30, PMC 22/22, nature.com, PLOS, Frontiers, arXiv, news |
| HTTP 403 | 136 | Concentrated in academic publishers. Failures of any kind by final host: SAGE 28, Wiley 21, ScienceDirect 16, Taylor & Francis 13, Science 10, OUP 10, PNAS 5 |
| 200/203 with under 3,000 chars of text | 57 | JavaScript shells, redirect stubs (`linkinghub.elsevier.com`), APA DOI stubs |
| Other | 9 | 404 x4, 400 x2, timeout x2, 402 x1 |

Sizes per attempted URL: 382 KB raw (227 KB gzipped, since PDFs don't
compress), 18.5 KB of extracted text, ~12.6 passages. Medians: HTML 146 KB
raw and 8.5 KB text; PDF 1.1 MB raw and 77 KB text. The whole set took 52 s,
~0.15 s per URL at 8 parallel.

The fetcher therefore needs a scholarly path, because the blocked sources are
mostly the papers reports lean on. For the 92 unusable URLs with a DOI,
OpenAlex (`api.openalex.org/works/doi:<doi>`, no key) returned metadata and a
publication date for all 92 and an abstract for 88. Its single "best"
open-access location was a poor rescue: 50 had one, and only 6 yielded
usable text, because most point back to the same blocking publishers.
Repository copies did better: 37 of the 92 listed an open-access copy in a
repository (PubMed Central, university repositories, Figshare), and 20 of
those yielded usable text. So for DOI sources the fetcher:

- resolves the DOI through OpenAlex for title, authors, venue and
  `publication_date` (`published_from = openalex`, precision from the date);
- stores the abstract as a document with `content_kind = abstract`;
- tries the publisher page, then every repository location OpenAlex
  lists, then the remaining open-access locations, and stores full text when
  one works. That still leaves ~70 of the 92 abstract-only.

Claims and quotes on an abstract-only source are labeled abstract-only. A
quote the citation check can't find in an abstract-only or snippet-only
source is `unverifiable`, not `not found`. Workers' search tools can read
pages this fetcher can't, so missing text doesn't mean a worker invented
the quote.

### Dates

Three dates are kept apart and never substituted for each other:

| Date | Meaning | Where it comes from |
|---|---|---|
| `published_at` | When the source says it was published | `citation_publication_date`, JSON-LD `datePublished`, `article:published_time`, Crossref for DOIs, a date in the URL path, bookworm's `date` field. `published_from` records which one, and `published_precision` records year/month/day. HTTP `Last-Modified` and PDF creation dates are marked weak |
| `captured_at` / `fetched_at` | When researchguy collected it | the capture log and fetch records |
| `as_of` | The time a claim itself refers to ("as of March 2026, 11.5%") | claim extraction |

An unknown publication date stays null. It is never filled in with the
collection date.

### Passages and embeddings

Text is split into passages of ~1,500 chars on paragraph boundaries, with
character offsets into the stored text. Full-text indexing happens on
insert. Embeddings use the configured embedder (`nomic-embed-text` by
default, matching grepai). At the measured ~67 passages/s, one run's
documents embed in well under a few minutes (estimate: 50-150 fetched
documents, 1-3k passages per run, not yet measured). Vectors carry their
model name; changing the model queues a background re-embed, and queries use
only one model's vectors.

### Claims

A claim is one checkable assertion with the verbatim quote that supports it.
Extraction runs per document and outputs `text`, `quote`, `as_of` and
`volatile`. A volatile claim is one whose truth changes with time: a case
status, a trial date, a count to date, a price, who holds an office.

Each quote is then checked mechanically: it must occur in the stored text
(whitespace-normalized), and its offsets are set from where it was found.
A claim whose quote isn't found is stored with `quote_verified = false` and
is never shown as quoted. This is the defense against an extractor inventing
quotes, and it doesn't depend on the extractor being good.

Extraction runs in the daemon after the run finishes, so it adds nothing to
dive latency. Documents cited in the report are extracted first.

### Claim links

Each new claim is compared with its nearest existing claims (vector
neighbors that share entities or terms). A model labels each pair `same`,
`supports`, `contradicts`, `refines`, `supersedes` or unrelated. A
`supersedes` link means a newer measurement of the same quantity, a
correction, a retraction or a reversed ruling. Links store `method`, `model`
and `confidence`. A model-labeled link is an inference, retrieval labels it
as one, and `researchguy store link set` lets a human confirm or override it.
Rule-based links (same DOI, same quote) are marked `rule`.

`origins` groups documents that are the same origin repeated, so ten outlets
running one wire story count once. Grouping uses canonical URLs, DOIs,
near-duplicate text and `same` links. It's a heuristic: a press release
rewritten by different outlets won't always be caught.

## Retrieval

### Ranking

One query in `internal/store/retrieve`:

1. Embed the query (10-50 ms measured).
2. Full-text rank (`ts_rank` over `tsv`) and vector distance (HNSW) over
   passages and claims, with filters on kind, date range, domain, run and
   project in the same query.
3. Fuse the two rankings with reciprocal rank fusion.
4. Expand the top claims through `claim_links` to pull in their clusters.
5. Compute cluster flags (below) from links and dates in SQL. No model call.

Old material is never dropped silently. `max_age` stays as an explicit
filter, applied on a chosen date (`published` or `captured`) inside the
query rather than after top-k. The default is to rank everything and label
age.

### Age and competing evidence

Results come back as evidence clusters: a claim with the claims that are
the `same`, `support` it, `contradict` it or `supersede` it, each dated.

| Flag | Meaning |
|---|---|
| `reinforced` | Supported by 2+ independent origins; reports the count and the date range |
| `single_origin` | All support traces to one origin, however many documents repeat it |
| `contested` | The cluster contains a `contradicts` link |
| `newer_contradiction` | The newest contradicting claim was published after the newest supporting one |
| `superseded` | A newer claim supersedes this one; shown collapsed under the newer claim by default |
| `possibly_outdated` | Volatile, and `as_of` (or `published_at`) is older than `store.volatile_max_age` (default 30d) |
| `last_confirmed` | Most recent collection date of any supporting document |

Recency weighting is off by default for evidence, because a 1995 primary
source isn't worse than a 2026 blog post. `prefer_recent` (a half-life) is
on by default for volatile claims, and any query can turn it on. `as_of`
on a query restricts results to what had been collected by that date, which
shows what an earlier report could have seen.

A result card, as agents see it (illustrative values):

```
[C:48213] claim, contested, newer_contradiction
  "Exposure to the feed shifted stated opinion by 0.1 SD"
  source: example-journal.org, published 2025-03 (citation_publication_date)
  collected 2026-10-02 (run 2026-10-02-dive-5), last confirmed 2026-10-02
  supports: 2 claims, 2 origins (2023-2025)
  contradicts: 1 claim, published 2026-01 (newer)
```

### Speed

Baseline today is 80-260 ms. The target is to stay in that range at ~1M
passages: one embedding call plus one indexed SQL query. That is a target,
not a measurement. Phase 4 measures it against the backfilled store, and
the HNSW index exists because the full scans above already take ~460 ms
warm at 420k rows.

## Agent access

New MCP tools, also available as CLI commands with `--json`:

| Tool | Returns |
|---|---|
| `researchguy_find` | Ranked evidence cards (claims, passages, report excerpts) with dates and cluster flags. Params: `query`, `kinds`, `since`, `until`, `date_field`, `run_id`, `prefer_recent`, `as_of`, `limit` |
| `researchguy_claim` | One claim: quote, surrounding passage, source and document version, and its cluster grouped by relation and sorted by date |
| `researchguy_passage` | A passage with N neighboring passages |
| `researchguy_document` | Full document text, paged by offset, with every fetched version and fetch attempt |
| `researchguy_source` | A source by URL or ID: versions, which runs found it, which reports cite it |
| `researchguy_timeline` | Dated sequence of claims for a query, entity or claim cluster, showing where the evidence changed |
| `researchguy_ingest_url` | Fetch and store a URL now, attached to a run, and return its document and passage IDs. Write tool |

`researchguy_context` becomes a formatter over `find`: evidence clusters and
report excerpts with dates, no silent age drop. `researchguy_search` keeps
using grepai for files that aren't in the store.

`researchguy mcp --profile read` exposes only the read tools. Workers and
the aggregator get this profile. That lets them search prior research and
the run's own evidence without being able to start dives recursively, which
is the reason Codex workers skip the user's MCP config today
(`codex.go:24-26`).

## Changes to the hybrid pipeline

1. Workers write captures as they go (above).
2. After workers finish, the fetch stage runs with bounded concurrency and a
   time budget; passages for the run are indexed and embedded before
   aggregation.
3. The aggregator prompt goes on stdin. It gets the worker drafts, the full
   snippet ledger (which fits in Opus's 1M-token context at the volumes
   measured), and a table of the run's sources with their dates, plus the
   read-profile tools scoped to this run and prior research. It quotes from
   passages it retrieves, not from memory.
4. The aggregator runs isolated: an empty working directory, no CLAUDE.md or
   memory discovery, and only the web and read-profile tools. The report goes
   between markers, and anything outside them is logged and discarded. This
   removes the chatter lines found in the 2026-10-02 reports.
5. Citations use store IDs (`[C:id]`, `[P:id]`, `[S:id]`) instead of
   per-prompt `[E#]` numbers. A deterministic check after writing resolves
   every marker and confirms every quoted string appears in the cited
   passage. Failures are flagged in the report's notes and in `citations`.
6. The critics use the same retrieval instead of a capped copy.
7. The report is saved with YAML frontmatter (`run_id`, `generated_at`,
   `backend`, `mode`, `topic`, source and capture counts), ingested as a
   synthesis document, and linked to its run.

## Other producers

- **alo `deep_research`.** Branch agents call `researchguy_ingest_url` for
  pages they rely on, so their evidence is captured the same way. Claims they
  register get anchored quotes, or are stored unverified. `prior_lookup`
  switches from `researchguy context` to `find`, and the workflow's tool
  allowlists add the read tools.
- **bookworm.** `graph import-sources` also creates `sources` rows, with the
  bibliography `date` as `published_at` (precision `year`, from
  `bookworm:date`), and optionally queues the URLs for fetching. Book
  research markdown is ingested as synthesis documents.

## Failure handling

| Failure | Behavior |
|---|---|
| Crash mid-run | Captures are already on disk; the run is marked `interrupted`; `researchguy store ingest --run <id>` replays it |
| Postgres down | Captures and blobs are still written; the daemon ingests when it comes back; retrieval falls back to grepai and says so |
| Ollama down | Passages are stored with full-text indexing only and embeddings are queued; `find` runs full-text only and says so |
| Fetch fails | A `fetches` row with the error; the source keeps its snippet; claims on it are labeled snippet-only |
| Page changed | New document version; the old one is kept; re-verification records "quote not found in the 2026-11 version" instead of rewriting the claim |
| Duplicate ingest | Unique `(run_id, seq)` and `(source_id, sha256)`; replays are no-ops |
| Schema change or corrupt index | `researchguy store rebuild` from the store, using cached vectors |

`researchguy store doctor` reports orphan blobs, unembedded passages,
unverified quotes, unresolved citations, failed fetches and ingest lag.

## Backfill and migration

- Existing reports in `research_dir` and federated book research become
  synthesis documents. Report dates come from the `*Generated:*` header
  where it exists, otherwise from file birth time, marked approximate.
- URLs in reports are queued as sources. Fetching them is optional, because
  they may have changed since the report was written.
- The graph tables move from `tasks.db` into Postgres, keeping node IDs, and
  `graph` commands switch to the new store. `tasks.db` keeps the scheduler
  tables.
- grepai stays the fallback for research retrieval until `find` matches it
  on a fixed set of queries, then research drops out of grepai's
  responsibilities. Code search is unaffected.

Rough backfill cost: ~64k existing chunks in the `sentinel-personal`
workspace take ~16 minutes to embed at the measured rate.

## Phases

Each phase ships on its own, with tests, a merge, a rebuild and a smoke test
against a scratch config.

| Phase | Delivers | Unblocks |
|---|---|---|
| 1 | Stop the drops: aggregator prompt on stdin, cap removed, aggregator isolated with output markers, run metadata and full ledger saved to `store/runs/` | Nothing is dropped from new runs, even before the index exists |
| 2 | Store foundation: `internal/store`, capture log, blob store, Postgres schema and migrations, `store ingest`/`rebuild`/`doctor`, `store.dir`/`store.dsn` config; hybrid workers write captures | Durable collection |
| 3 | Fetch, text extraction, dates, passages, full-text and embeddings | Real documents and quotes |
| 4 | Retrieval API, MCP tools, read profile, runner context on `find`, aggregator retrieval, citation check | Agents query evidence; reports cite it |
| 5 | Claims, quote verification, claim links, origins, cluster flags | Age-aware reinforcing and competing evidence |
| 6 | Backfill reports and book research, graph migration, bookworm sources, grepai parity check | One store for all research |
| 7 | deep_research and bookworm write through the store | Every producer captures the same way |

Phase 1 is small and independent, and it fixes the drops on its own.

### Phase 1 as built

Built 2026-10-02 in `e35acd6`.

- **Run records.** `internal/store` writes `runs/<run_id>/` for every runner
  task (CLI, MCP, scheduler): `run.json` (written as `running` at start,
  rewritten as `succeeded` or `failed` with the report path, error and
  provider metadata), `captures.jsonl` (fsynced), and for hybrid
  `workers.jsonl` (full drafts), `aggregator-prompt.md` and
  `aggregator-output.md`. Run IDs are a UTC timestamp plus 6 random hex
  characters. `--json` and the MCP research tools return `run_id` and
  `run_dir`.
- **Stable citations.** Ledger items are numbered `E1..En` across all
  workers, failed ones included, and that number is the capture's `seq`. The
  aggregator and both critics see the same IDs, and the report header names
  the run (`| Run: <id>`), so `[E12]` resolves to a line in
  `captures.jsonl`.
- **Claude provider.** The prompt goes on stdin and the system prompt in a
  file (`--system-prompt-file`), in an empty temp dir that is removed
  afterwards, with `--no-session-persistence`, an explicit `--tools` list
  (`""` when the request has no tools) and, under the new
  `claude.ignore_user_config` (default true), `--safe-mode` and
  `--strict-mcp-config`. Checked against the CLI (2.1.282): Opus reports a
  1,000,000-token `contextWindow`; a probe's context fell from 15,290 to
  3,912 tokens with `--safe-mode`, and its reply named no CLAUDE.md; with
  `--tools ""` a request to run `touch` via Bash created no file. The model
  still wrote "DONE", which is the chatter the markers below exist for.
- **Output markers.** The aggregator writes between `===BEGIN REPORT===` and
  `===END REPORT===`. The text between them is the report, and the raw
  output is kept in the run. Metadata `aggregator_output` records
  `markers` (`ok`, `unterminated`, `missing`, `empty`) and the discarded
  character count.

Deviations from the plan above:

- **The cap is sized to the backend, not removed.** `max_evidence_chars: 0`
  (the new default) means ~2M chars for a claude consumer, ~500-670k tokens
  at 3-4 chars per token, and 80k otherwise. The 2026-10-02 dives captured
  0.83-1.01M chars, so they would pass whole. An explicit cap still applies
  to every consumer. The critics get their own cut of the ledger when their
  backend's budget is smaller than the aggregator's, recorded as
  `critic_evidence_ledger`; before, they shared the aggregator's.
- **The store dir defaults to `<config dir>/store`,** not a fixed
  `~/.researchguy/store`, so a scratch `RESEARCHGUY_CONFIG_DIR` (tests,
  smoke runs) never writes into the real store. A config dir that should
  share the main store sets `store.dir` (the codex-hybrid config does).
- **Captures are written when the workers return,** not per tool call.
  Codex output is still buffered until the worker exits (`codex.go:73`), so
  a worker killed mid-run loses its own results. Streaming is phase 2.
- **No YAML frontmatter yet.** The run ID goes in the existing header line.
  Frontmatter is item 7 under "Changes to the hybrid pipeline".
- **Not done:** marking crashed runs `interrupted` (they stay `running`),
  and the Codex page-open label bug. Both are phase 2.

### Phase 2 as built

Built 2026-10-02, `c3b9d76` to `1808098`.

- **Captures stream.** Each tool result is appended to `captures.jsonl` as
  it arrives, through `Request.Capture`: Codex stdout is read line by line,
  Ollama writes in its tool loop. The run assigns every `seq`, so parallel
  workers' streamed captures get distinct IDs in arrival order. Providers
  that don't stream (Claude) are appended after the call returns, in worker
  order. A seq whose write failed stays used, so no later capture takes an
  ID a prompt may already cite. Each capture carries its structured call:
  tool, action (`search`, `open`, `fetch`, or a Codex action such as
  `find_in_page`), query, URL, and for searches the ranked results with an
  `opened` flag. That is the rank recording decision 2 asks for.
- **Codex page opens** are labeled `web_search open: <url>`, with the URL
  taken from the opened result when the action has none (the phase 1 bug).
- **Interrupted runs.** `run.json` records the PID and host.
  `store.Reconcile` marks a `running` run `interrupted` when it started on
  this host and its PID is gone, taking its end time from the newest file in
  the run dir and its capture count from the log. Runs recorded before PIDs
  were are left alone. `store ingest`, `store rebuild` and the daemon
  reconcile; `store doctor` only reports.
- **Index.** `internal/store/index`, on `pgx/v5`. Migrations are embedded SQL
  files applied in one transaction under an advisory lock and tracked in
  `schema_migrations`; an index schema newer than the binary is refused.
  Migration 1 creates `runs`, `captures`, `sources` and `sightings`.
- **Ingest** is idempotent: captures are keyed by `(run_id, seq)`, sightings
  by `(run_id, seq, ord)`, and a run is skipped when the SHA-256 of its
  `run.json` and the size of its `captures.jsonl` match the last ingest.
  Each run's ingest holds an advisory lock on its ID, so the runner and the
  daemon take turns, and sources are upserted in sorted key order, so
  concurrent ingests of runs sharing sources can't deadlock. A partial last
  line (a crash mid-write) isn't ingested; lines that don't decode are
  skipped and reported.
- **When runs are indexed.** With `store.dsn` set, the runner ingests its
  run when it finishes (30 s timeout; a failure is a warning and the run is
  still on disk), and the daemon syncs at start and every poll interval,
  logging a problem when it changes rather than every pass.
- **Commands.** `store init` creates the database if needed and migrates
  it. `store ingest [--run <id>] [--force]` reconciles, then indexes what's
  new or changed. `store rebuild` empties and re-ingests in one transaction,
  so a failed rebuild leaves the old index. `store doctor` checks the store
  (run dirs without a record, orphaned runs, capture count mismatches, bad
  or truncated capture lines) and the index (reachable, schema version, row
  counts, finished runs not indexed) without changing either, and exits
  non-zero on a problem. `ingest`, `rebuild` and `doctor` take `--json`.

Deviations from the plan above:

- **No blob store yet.** Nothing writes blobs before the fetcher exists, so
  `blobs/`, `text/` and `vectors/` move to phase 3 with their first writer,
  as does `pgvector-go`.
- **A `sightings` table,** not in the schema above. A source is seen many
  times, in different runs, at different ranks. `sightings` holds each one:
  `(run_id, seq, ord)`, the source, its role (`result`, `opened`,
  `fetched`), rank, title and snippet. Without it the index can't answer
  which runs found a source, or decision 2's rank question.
- **Source IDs are hashes,** the first 63 bits of the SHA-256 of `url_key`,
  not a sequence, so a rebuild reproduces them and an ID stored outside the
  index stays valid. `node_id` waits for phase 6; `kind` is `web` until
  phase 3 classifies sources. `runs.report_path` stands in for
  `report_document_id` until documents exist.
- **`store.dsn` defaults to blank,** meaning no index, for the same reason
  as the store dir: a scratch config never writes into the real index.
- **`worker_error`** is set only on captures appended after the worker
  returned. A streamed capture is written before its worker's outcome is
  known.
- **Runs recorded before phase 2 add no sources.** Their captures have a
  label and content but no structured call, so they index as captures only.
  Checked on the 2026-10-02 smoke run `20261002T170200Z-0ad1e9`: 14
  captures, 0 sources. Recovering their URLs means parsing the content,
  which phase 6's backfill can do.

### Phase 3 as built

Built 2026-10-02, `f8780c4` to `2d71534`.

- **Fetch stage** (`internal/fetch`). For a finished run it plans, in
  order, the URLs cited in worker drafts and in the report's own section
  for the run (a watch file holds many runs), the pages workers opened or
  fetched, and the top `store.fetch.top_results` (3) of each search,
  deduplicated by `url_key`, skipping images, media and archives by
  extension. Each target is fetched with bounded concurrency (8), per-host
  spacing (250 ms) and a per-host limit (2), retries on network errors, 429
  and 5xx (2 retries, backoff from 1 s, `Retry-After` up to 10 s), a 20 s
  timeout and a 25 MB cap. Private addresses are refused in the dialer
  unless `RESEARCHGUY_ALLOW_PRIVATE_URLS=true`.
- **Extraction.** HTML: navigation, footers, asides, scripts, hidden
  elements and unlikely class names are pruned, then the main content is
  the longest `<article>` of 500+ chars, else `<main>`, else the best
  paragraph cluster scored by text and link density, else the body. A meta
  refresh is followed (2 hops). PDF: `pdftotext` for text, `pdfinfo` for
  title and creation date. Text is cleaned of NULs and invalid UTF-8.
- **DOI rescue.** When the direct fetch gives under 3,000 chars and the URL
  has a DOI, OpenAlex gives the title, the publication date and the
  abstract (a document with `content_kind = abstract`). Then up to 4 of
  its locations are fetched, repository copies before other open-access
  ones, until one gives usable text.
- **Dates.** For a page: meta tags, most trusted first (`citation_*`, then
  `article:published_time`, Dublin Core and `prism`, then JSON-LD
  `datePublished` and generic date tags), then `<time pubdate>`, then a date
  in the URL path, then `Last-Modified` (weak). For a PDF: the URL path,
  then the creation date (weak). OpenAlex records take OpenAlex's date. Each date has its source and precision (year, month, day). A later
  fetch with a strong date replaces a weak one; an unknown date stays null.
- **Files.** Raw bytes go to `blobs/` gzipped, text to `text/`, and every
  attempt to the run's `fetches.jsonl` (fsynced) with the reason, rank, how
  it was reached (`direct`, `openalex`, `repository`, `open_access`),
  attempts, status, error, final URL and the hashes. `fetch.json` holds the
  pass's counts; a run is fetched when `remaining` is 0. A per-run
  `fetch.lock` (non-blocking `flock`) keeps two processes from fetching the
  same run.
- **When runs are fetched.** The runner fetches after a dive, review,
  compare, enrich or watch ends, within `store.fetch.budget` (3 m). Asks and
  canceled tasks are left for the daemon, whose pass fetches up to 4 waiting
  runs, newest first, then syncs the index, then embeds. `store fetch`
  fetches every waiting run (or `--run`, with `--force` to try again) and
  indexes them.
- **Index.** Migration 2 enables `vector` and adds `documents`, `fetches`
  and `passages`, plus `runs.fetch_bytes` so a grown fetch log makes a run
  pending. Ingest reads the fetch log with the captures, upserts a source
  for every fetched URL (a cited URL may be in no capture) and classifies
  sources (`paper` for DOIs, arXiv and PubMed, `video`, `court`, else
  `web`). Passages are cut only for documents that have none yet.
- **Passages** are ~1,500 chars on paragraph boundaries (at most 2,000; a
  short tail joins the previous one), with offsets into the stored text.
  The split is deterministic, so passage IDs survive a rebuild.
- **Embeddings** (`internal/embed`) go through Ollama's `/api/embed` with
  `nomic-embed-text` and its `search_document:`/`search_query:` prefixes,
  64 per request. `Index.Embed` takes passages without a vector (then ones
  another model embedded) in batches locked with `SKIP LOCKED`, uses a
  cached vector when there is one, and caches what the model makes. The
  runner embeds within `store.embed.budget` (2 m); `store embed` runs until
  done. Ingest attaches cached vectors to new passages, so a rebuild doesn't
  call the model.
- **Doctor** adds fetch, document and passage counts, passages without a
  vector from the configured model, and runs waiting on fetching. The last
  two are reported, not problems: both are normal between a run ending and
  the daemon's next pass.

Smoke run, 2026-10-02, a hand-made run of 6 real URLs: 8 attempts in 1.9 s,
5 fetched. The SAGE DOI got a 403, then an OpenAlex abstract and a PubMed
repository copy. The arXiv PDF extracted 39,902 chars with a weak
`pdf-creation` date. 6 documents, 53 passages, embedded in 1.2 s (~44/s),
and a rebuild reattached all 53 vectors from the cache.

Deviations from the plan above:

- **Fetch records live in each run,** in `fetches.jsonl`, not only in the
  index, because the index must be rebuildable. A URL two runs both cite is
  fetched by each; there's no cross-run fetch reuse yet.
- **A document is identified by its text's hash,** not the raw bytes, so a
  page whose markup changes but whose text doesn't is one document.
  Offsets are characters (runes) into that text.
- **The fetch runs after the run ends,** not between the workers and the
  aggregator. Moving it before aggregation goes with aggregator retrieval in
  phase 4. (Phase 4 added the pass before aggregation; see below.)
- **Retries are counted per record** (`attempts`), not one row per HTTP
  try. One row per URL and route reads better and still shows a flaky host.
- **Added:** meta refresh following, source kind classification, and
  `store fetch`/`store embed`.
- **Not done:** report documents (`origin = synthesis`) wait for phase 4's
  citation check and phase 6's backfill; doctor doesn't look for blobs no
  run references; snippets aren't stored as `content_kind = snippet`
  documents (they stay in sightings).
- **Found while testing:** PubMed began serving a proof-of-work challenge
  (HTTP 203, `Cookies must be enabled`) to every request from this machine
  on 2026-10-02, after it served 30 of 30 in the fetch measurement earlier
  that day. The fetcher records
  those as failures and doesn't try to pass the challenge; NCBI's
  E-utilities are the sanctioned route and a follow-up. Passages of figure
  labels (`<pad> <pad>` in an arXiv PDF) embed close to many queries, so
  phase 4's ranking needs to discount low-information passages. A news
  feature with a DOI is classified `paper`.

### Phase 4 as built

Built 2026-10-02, `80456f5` to `1c44025`.

- **Find** (`internal/store/retrieve`). One read-only transaction runs a
  full-text arm (`plainto_tsquery` with its terms ORed, ranked by
  `ts_rank_cd`) and, when the query embeds, a vector arm (HNSW cosine,
  `hnsw.iterative_scan = relaxed_order` so a filtered query doesn't come
  back short), each taking the top `max(5 x limit, 50)` under the same
  filters. Reciprocal rank fusion (k = 60) combines them. A passage's score
  is scaled by its share of distinct words relative to prose (floored at
  0.2), so figure labels and token tables sink, and by a half-life when
  `prefer_recent` is set. A document gives at most 2 cards; `limit` is at
  most 100. Filters: kind (`passage` or `report`), `since`/`until` on the
  published date (undated documents drop out) or the collected date,
  `as_of`, `run_id`, domain and its subdomains, and `min_similarity`. When
  the embedder is off, down or the wrong size, the search is full-text only
  and the result's `note` says why.
- **Cards** carry the `P:<id>` and `S:<id>` refs, URL, domain, title, the
  published date with its source and precision (weak dates flagged), the
  collected date, an age label, the kind, the passage text and both ranks.
- **Lookups.** `Passage` with up to 5 neighbors on each side; `Document`
  paged by character offset (at most 50,000 chars), with every version and
  fetch attempt; `Source` by URL, ID or `S:` ref, with its documents,
  sightings, fetches and the runs that cited it; `RunSources`, a run's
  sources with their dates and fetch state, fetched ones first.
- **CLI and MCP.** `find`, `store passage`/`document`/`source`/`ingest-url`,
  and `researchguy_find`/`_passage`/`_document`/`_source`/`_ingest_url`.
  MCP takes IDs as strings, since 63-bit IDs exceed JSON's 2^53.
  `ingest-url` fetches one URL into a given run or a new `manual` run, then
  indexes and embeds just that document. `mcp --profile read` serves 11
  read tools: search, context, list, read, graph list/show/find, find,
  passage, document and source.
- **Context.** `search.BuildContext` adds store evidence from `find` next to
  the grepai excerpts, each labeled primary evidence or synthesis, with its
  dates. Only an explicit max age filters it, on the collected date.
- **Reports as documents.** `Finish` copies a succeeded run's section of its
  report into `text/` (`report_sha256` in `run.json`). Ingest makes it an
  `origin = synthesis` document under the source `researchguy:run/<id>`,
  and migration 3 adds `runs.report_document_id`.
- **Hybrid.** `Request.BeforeAggregate` runs after the workers are recorded
  and unloaded: the runner fetches the run (not for asks), indexes and
  embeds it, lists up to 200 sources as a table, and, for a claude
  aggregator with `hybrid.aggregator_tools` on, attaches `researchguy mcp
  --profile read` on the same config. The prompt asks for `[E<n>]`, `[P:<id>]`
  and `[S:<id>]` citations with exact quotes in double quotes. The critics'
  evidence adds the text of every passage the draft cites (at most 50). A
  failure in the hook is a warning, and the aggregator works from the
  ledger alone.
- **Claude isolation with MCP.** `--safe-mode` disables every MCP server,
  including those `--mcp-config` names, so a call with MCP uses
  `--setting-sources ""` and `--strict-mcp-config`, with `--allowedTools`
  naming `mcp__researchguy`. Checked against Claude Code 2.1.282: no
  CLAUDE.md, memory, hooks or user MCP servers load, and tools not allowed
  are denied. That mode also skips the `env` block of `settings.json`, so
  without `CLAUDE_CODE_OAUTH_TOKEN` in the process environment the call
  fails "Not logged in"; the provider then retries once under `--safe-mode`
  without MCP, with a warning.
- **Citation check** (`internal/cite`, no model). Markers are parsed from
  the run's section of the report. A quote is a double-quoted string
  (straight or curly) of 4+ words on the same line, closing within 100
  chars of the citation. Matching ignores case, punctuation and spacing,
  and an ellipsis splits a quote into fragments that must appear in order.
  Citations in one bracket or adjacent brackets share a quote, which fails
  only if every one of them is `not_found`. Search snippets, abstracts and
  sources without text give `unverifiable`; no index gives `unchecked`. A
  passage from a report resolves, with the note that it isn't primary
  evidence. Results go to the run's `citations.jsonl` and, through
  migration 4, the `citations` table (`runs.citation_bytes` makes a
  rewritten file pending). Failures are listed under `## Citation Check`
  at the end of the report, and `store doctor` lists runs with failures
  as information, not problems.

Smoke run, 2026-10-02, the installed binary on a scratch config:
`ingest-url` on a Wikipedia page (6,494 chars, 5 passages), then `find` in
hybrid mode in 23 ms, and `claude -p` (Haiku, `--setting-sources ""`, the
read profile through `--mcp-config`) called `researchguy_find` and answered
with the card's ref.

Speed, on a synthetic index since the backfill is phase 6: 20k documents
and 100k passages of random text from a 78-word vocabulary with random unit
vectors, HNSW built, on Postgres 14.22 with pgvector 0.8.1 on this machine,
and the query embedded by `nomic-embed-text` through Ollama. `took_ms`
covers embedding the query through building the cards, not process start.
Over 5 queries run 3 times each:

| Mode | took_ms |
|---|---|
| Hybrid, warm | 150-239, median ~170 |
| Hybrid, first query | 423 |
| Full-text only | 95-185, median ~125 |
| Hybrid, `--since 2024` (12% of documents) | 130-194 |

With so small a vocabulary each ORed full-text query matches ~15% of the
passages (14,971 of 100k for "vaccine trial efficacy"), which should be
worse than real text. That's inside the 80-260 ms target at 100k; 1M
passages is unmeasured.

Deviations from the plan above:

- **`[E<seq>]` stays** next to `[P:]` and `[S:]`. It's the capture's `seq`
  (phase 2), stable across prompts and checked against the run's captures,
  so it isn't the per-prompt numbering the plan replaces. `[C:]` waits for
  claims.
- **The critics get the cited passages,** not retrieval tools: the passages
  are appended to their evidence. Retrieval tools for critics are a
  follow-up.
- **Only a claude aggregator gets the read profile.** Codex and Ollama
  workers still don't, and Codex still skips the user's MCP config.
- **Not done:** `researchguy_claim`, `researchguy_timeline` and cluster
  flags wait for claims in phase 5. Reports have no YAML frontmatter: the
  header line names the run, and `run.json` holds the rest.
- **The fetch runs twice:** before aggregation for what workers found, and
  after the run for URLs only the report cites.
- **Context isn't only a formatter over `find`.** Reports in `research_dir`
  aren't in the store until phase 6's backfill, so grepai stays the source
  for them.
- **Speed was measured on a synthetic 100k index,** not the backfilled
  store. Phase 6 measures it again on real text.
- **Added:** `min_similarity`, the low-information discount,
  `hybrid.aggregator_tools`, the sources table and the "Not logged in"
  retry.
- **Found while testing:** an `ingest-url` run writes no `fetch.json`, so
  `store doctor` counts it as waiting on fetching until the daemon's next
  pass finds nothing to fetch and writes one. Harmless, and a follow-up.
  `TestHybrid_CodexWorkersClaudeAggregator` is flaky under load: the fake
  codex's parallel workers each rewrite one `args.txt`, so a read can see
  interleaved writes.

## Decisions

Accepted 2026-10-02 as recommended: Postgres + pgvector in a new
`researchguy` database; eager fetch of opened, cited and top 3 results with
rank recorded; the local model for claim extraction and a stronger one for
links; retire grepai for research at parity; move the graph to Postgres in
phase 6. The options as they were weighed:

1. **Index backend.** Postgres + pgvector in a new `researchguy` database
   (recommended, reasons above) or SQLite with brute-force vectors.
2. **Eager fetch scope per run.** Opened pages, cited URLs and the top 3
   results per search (recommended), or every result URL. Either way every
   result is kept as a source with its snippet and can be fetched on demand.
   Per-run estimates from the measured per-URL averages (URL counts are
   estimates: ~58 cited URLs per report measured; 122-166 captured search
   items per run; ~19 results per item, inferred from chars per item):

   | Scope | URLs | Stored (gzip) | Text | Passages | Fetch + embed |
   |---|---|---|---|---|---|
   | Cited only | ~58 | ~13 MB | ~1 MB | ~730 | ~20 s |
   | Opened + cited + top 3 | ~300-550 | ~70-125 MB | ~6-10 MB | ~4-7k | ~2-3 min |
   | Every result | ~2.3-3.2k | ~0.5-0.7 GB | ~42-59 MB | ~29-40k | ~13-18 min |

   Fetching every result costs ~4-10x the recommended scope per run, ~50-70
   GB per 100 runs before cross-run dedup, and fills retrieval with pages no
   worker judged relevant. The recommended scope records each result's rank
   in the capture. After ~5 runs, measure how often a cited URL was only a
   rank 4+ result nobody opened, and raise N if it's common.
3. **Models for claims.** The local utility model (`qwen3.5:9b`) for
   extraction, where the mechanical quote check catches invented quotes, and
   a stronger model for `contradicts`/`supersedes` labels, which are judgment
   calls (recommended). Cost isn't measured yet.
4. **grepai for research.** Retire it for research once `find` reaches
   parity (recommended), or keep federating both.
5. **Graph migration.** Move the graph into Postgres in phase 6
   (recommended), or keep it in SQLite and bridge by `node_keys`, which
   leaves claims and sources in two databases.

## Caveats

- The store records what was found and when. It doesn't decide what's true.
  Model-labeled links are inferences and are labeled as such.
- Origin grouping is heuristic, so `reinforced` can overcount when
  rewritten copies of one source aren't detected.
- Many pages won't yield a publication date. Unknown stays unknown, and
  retrieval says so instead of falling back to the collection date.
- Size and per-run volume are estimates until phase 3 measures real runs.
  Raw HTML is the bulk; it's gzipped, and pruning raw bytes while keeping the
  extracted text is an option if it grows.
- Postgres becomes required for the full feature. The degraded modes above
  keep collection working without it, but retrieval drops back to grepai.
