-- Every citation marker in a run's report and what the citation check
-- found, from the run's citations.jsonl.

-- What of the run's citations.jsonl was ingested; -1 is no file, a run
-- whose report wasn't checked.
ALTER TABLE runs ADD COLUMN citation_bytes bigint NOT NULL DEFAULT -1;

CREATE TABLE citations (
    run_id         text NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    ord            integer NOT NULL,
    marker         text NOT NULL,
    target_kind    text NOT NULL CHECK (target_kind IN ('capture', 'passage', 'source')),
    -- A capture's seq, or a passage or source id.
    target_id      text NOT NULL,
    -- Characters into the report's stored text.
    report_offset  integer NOT NULL,
    -- Citations made together share a group and a quote.
    group_ord      integer NOT NULL,
    quote          text NOT NULL DEFAULT '',
    -- Null when the target couldn't be looked up.
    resolved       boolean,
    quote_status   text CHECK (quote_status IN ('found', 'not_found', 'unverifiable', 'unchecked')),
    note           text NOT NULL DEFAULT '',
    PRIMARY KEY (run_id, ord)
);
CREATE INDEX citations_failed ON citations (run_id) WHERE resolved = false OR quote_status = 'not_found';
