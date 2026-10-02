-- Runs, their captures, and every source a capture saw. Everything here is
-- derived from the on-disk store (store.dir), and `researchguy store
-- rebuild` recreates it from there.

CREATE TABLE runs (
    id             text PRIMARY KEY,
    kind           text NOT NULL,
    topic          text NOT NULL DEFAULT '',
    mode           text NOT NULL DEFAULT '',
    backend        text NOT NULL DEFAULT '',
    branch_count   integer NOT NULL DEFAULT 0,
    status         text NOT NULL,
    error          text NOT NULL DEFAULT '',
    started_at     timestamptz NOT NULL,
    finished_at    timestamptz,
    report_path    text NOT NULL DEFAULT '',
    meta           jsonb,
    -- What was ingested, so ingest can tell when a run directory changed.
    record_sha256  text NOT NULL,
    capture_bytes  bigint NOT NULL DEFAULT 0,
    ingested_at    timestamptz NOT NULL
);
CREATE INDEX runs_started_at ON runs (started_at);

CREATE TABLE captures (
    run_id       text NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    seq          integer NOT NULL,
    captured_at  timestamptz NOT NULL,
    worker       integer NOT NULL DEFAULT 0,
    shard        text NOT NULL DEFAULT '',
    backend      text NOT NULL DEFAULT '',
    model        text NOT NULL DEFAULT '',
    tool         text NOT NULL DEFAULT '',
    action       text NOT NULL DEFAULT '',
    query        text NOT NULL DEFAULT '',
    url          text NOT NULL DEFAULT '',
    label        text NOT NULL DEFAULT '',
    -- The captures.jsonl line as written.
    payload      jsonb NOT NULL,
    PRIMARY KEY (run_id, seq)
);

-- One row per citable thing, keyed by its normalized URL. The id is derived
-- from url_key, so it is the same after a rebuild.
CREATE TABLE sources (
    id             bigint PRIMARY KEY,
    url_key        text NOT NULL UNIQUE,
    url            text NOT NULL,
    domain         text NOT NULL,
    title          text NOT NULL DEFAULT '',
    kind           text NOT NULL DEFAULT 'web',
    doi            text,
    first_seen_at  timestamptz NOT NULL,
    last_seen_at   timestamptz NOT NULL
);
CREATE INDEX sources_domain ON sources (domain);
CREATE INDEX sources_doi ON sources (doi) WHERE doi IS NOT NULL;

-- Each time a capture saw a source: a ranked search result, a page the
-- model opened, or a page fetched. The snippet is the one this capture saw.
CREATE TABLE sightings (
    run_id     text NOT NULL,
    seq        integer NOT NULL,
    ord        integer NOT NULL,
    source_id  bigint NOT NULL REFERENCES sources (id),
    role       text NOT NULL CHECK (role IN ('result', 'opened', 'fetched')),
    rank       integer,
    title      text NOT NULL DEFAULT '',
    snippet    text NOT NULL DEFAULT '',
    PRIMARY KEY (run_id, seq, ord),
    FOREIGN KEY (run_id, seq) REFERENCES captures (run_id, seq) ON DELETE CASCADE
);
CREATE INDEX sightings_source ON sightings (source_id);
