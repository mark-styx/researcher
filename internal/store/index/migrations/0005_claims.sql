-- Claims extracted from documents, the links between them, and origins:
-- documents that are one origin repeated. Claims come from the store's
-- claims/ files and model and human links from links.jsonl; rule links and
-- origins are computed here from what's indexed. All of it rebuilds.

-- Which extraction files are indexed, per document: a document's text can
-- belong to several sources, and each gets its own claims.
CREATE TABLE extractions (
    document_id    bigint NOT NULL REFERENCES documents (id) ON DELETE CASCADE,
    extractor_dir  text NOT NULL,
    extractor      text NOT NULL,
    file_bytes     bigint NOT NULL,
    complete       boolean NOT NULL,
    claims         integer NOT NULL,
    extracted_at   timestamptz NOT NULL,
    PRIMARY KEY (document_id, extractor_dir)
);

-- One checkable assertion. quote is what the model wrote; when it's found
-- in the document's text, quote_verified is set and char_start/char_end
-- (characters into the text) say where. quote_key is the found span,
-- normalized, when it has 6+ words: two documents with the same one quote
-- the same words. An unverified claim is never shown as quoted.
CREATE TABLE claims (
    id                bigint PRIMARY KEY,
    document_id       bigint NOT NULL REFERENCES documents (id) ON DELETE CASCADE,
    passage_id        bigint REFERENCES passages (id) ON DELETE SET NULL,
    extractor         text NOT NULL,
    chunk             integer NOT NULL,
    ord               integer NOT NULL,
    text              text NOT NULL,
    quote             text NOT NULL,
    quote_verified    boolean NOT NULL,
    char_start        integer,
    char_end          integer,
    quote_key         text,
    as_of             date,
    as_of_precision   text CHECK (as_of_precision IN ('year', 'month', 'day')),
    volatile          boolean NOT NULL,
    extracted_at      timestamptz NOT NULL,
    text_sha256       text NOT NULL,
    tsv               tsvector GENERATED ALWAYS AS (to_tsvector('english', text)) STORED,
    embedding         vector(768),
    embed_model       text
);
CREATE INDEX claims_document ON claims (document_id);
CREATE INDEX claims_passage ON claims (passage_id);
CREATE INDEX claims_quote_key ON claims (quote_key) WHERE quote_verified;
CREATE INDEX claims_tsv ON claims USING gin (tsv);
CREATE INDEX claims_embedding ON claims USING hnsw (embedding vector_cosine_ops);
CREATE INDEX claims_unembedded ON claims (id) WHERE embedding IS NULL;

-- from_claim relates to to_claim; for supersedes, from_claim is the newer.
-- No foreign keys: a logged link waits for its claims to be indexed, and
-- retrieval joins through claims, so a dangling link shows nowhere.
CREATE TABLE claim_links (
    from_claim  bigint NOT NULL,
    to_claim    bigint NOT NULL,
    relation    text NOT NULL CHECK (relation IN ('same', 'supports', 'contradicts', 'refines', 'supersedes', 'unrelated')),
    method      text NOT NULL CHECK (method IN ('rule', 'model', 'human')),
    model       text,
    confidence  real,
    note        text,
    created_at  timestamptz NOT NULL,
    PRIMARY KEY (from_claim, to_claim, method)
);
CREATE INDEX claim_links_to ON claim_links (to_claim);

-- The link that holds for each pair of claims, either way round: a human's
-- over a model's over a rule's, the latest of each.
CREATE VIEW claim_relations AS
SELECT DISTINCT ON (LEAST(from_claim, to_claim), GREATEST(from_claim, to_claim))
       from_claim, to_claim, relation, method, model, confidence, note, created_at
FROM claim_links
ORDER BY LEAST(from_claim, to_claim), GREATEST(from_claim, to_claim),
         CASE method WHEN 'human' THEN 0 WHEN 'model' THEN 1 ELSE 2 END, created_at DESC;

-- Documents that are one origin repeated: versions of one source,
-- syndication, mirrors, wire copy. origin_id is the group's lowest document
-- id; a document alone is its own origin, with reason 'self'. Only primary
-- documents have origins.
CREATE TABLE origins (
    document_id  bigint PRIMARY KEY REFERENCES documents (id) ON DELETE CASCADE,
    origin_id    bigint NOT NULL,
    reason       text NOT NULL CHECK (reason IN ('self', 'source', 'final-url', 'doi', 'same-link', 'near-duplicate'))
);
CREATE INDEX origins_origin ON origins (origin_id);

-- A 64-bit SimHash of a document's text, for near-duplicate origins.
ALTER TABLE documents ADD COLUMN simhash bigint;

-- How far links.jsonl has been read.
CREATE TABLE store_state (
    key    text PRIMARY KEY,
    value  bigint NOT NULL
);
