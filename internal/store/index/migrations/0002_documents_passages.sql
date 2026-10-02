-- Fetched documents, every fetch attempt, and the passages documents are
-- split into, with full-text and vector indexes. Derived from each run's
-- fetches.jsonl and the store's text and vector files, like everything
-- else here.

CREATE EXTENSION IF NOT EXISTS vector;

-- What of a run's fetch log was ingested, alongside its capture log.
ALTER TABLE runs ADD COLUMN fetch_bytes bigint NOT NULL DEFAULT 0;

-- A distinct text of a source. A re-fetch with different text is a new
-- version and the old one stays. The id is derived from the source's
-- url_key and the text's hash, so it is the same after a rebuild.
CREATE TABLE documents (
    id                   bigint PRIMARY KEY,
    source_id            bigint NOT NULL REFERENCES sources (id),
    sha256               text NOT NULL,
    content_kind         text NOT NULL CHECK (content_kind IN ('full', 'abstract')),
    origin               text NOT NULL DEFAULT 'primary' CHECK (origin IN ('primary', 'synthesis')),
    content_type         text NOT NULL DEFAULT '',
    title                text NOT NULL DEFAULT '',
    text_chars           integer NOT NULL,
    first_fetched_at     timestamptz NOT NULL,
    last_fetched_at      timestamptz NOT NULL,
    -- When the source says it was published; null when it doesn't say.
    -- Never the fetch date.
    published_at         date,
    published_precision  text CHECK (published_precision IN ('year', 'month', 'day')),
    published_from       text,
    published_weak       boolean NOT NULL DEFAULT false,
    UNIQUE (source_id, sha256)
);
CREATE INDEX documents_published_at ON documents (published_at);

-- Every attempt to get a source's content, failures included.
CREATE TABLE fetches (
    run_id        text NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    seq           integer NOT NULL,
    source_id     bigint NOT NULL REFERENCES sources (id),
    reason        text NOT NULL,
    rank          integer,
    via           text NOT NULL,
    fetch_url     text NOT NULL,
    attempted_at  timestamptz NOT NULL,
    attempts      integer NOT NULL DEFAULT 1,
    duration_ms   bigint NOT NULL DEFAULT 0,
    http_status   integer,
    final_url     text NOT NULL DEFAULT '',
    content_type  text NOT NULL DEFAULT '',
    error         text NOT NULL DEFAULT '',
    raw_sha256    text,
    raw_bytes     bigint,
    document_id   bigint REFERENCES documents (id),
    PRIMARY KEY (run_id, seq)
);
CREATE INDEX fetches_source ON fetches (source_id);
CREATE INDEX fetches_document ON fetches (document_id) WHERE document_id IS NOT NULL;

-- A document's text in passages of ~1,500 characters. char_start and
-- char_end are character offsets into the document text. The id is
-- derived from the document and the span.
CREATE TABLE passages (
    id           bigint PRIMARY KEY,
    document_id  bigint NOT NULL REFERENCES documents (id) ON DELETE CASCADE,
    ord          integer NOT NULL,
    char_start   integer NOT NULL,
    char_end     integer NOT NULL,
    text         text NOT NULL,
    text_sha256  text NOT NULL,
    tsv          tsvector GENERATED ALWAYS AS (to_tsvector('english', text)) STORED,
    embedding    vector(768),
    embed_model  text,
    UNIQUE (document_id, ord)
);
CREATE INDEX passages_tsv ON passages USING gin (tsv);
CREATE INDEX passages_embedding ON passages USING hnsw (embedding vector_cosine_ops);
CREATE INDEX passages_unembedded ON passages (id) WHERE embedding IS NULL;
