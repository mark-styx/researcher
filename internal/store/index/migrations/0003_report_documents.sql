-- A run's report, as a synthesis document: its text is copied into the
-- store when the run finishes (run.json report_sha256), so a rebuild finds
-- it even if the report file moved or changed.

ALTER TABLE runs ADD COLUMN report_document_id bigint REFERENCES documents (id) ON DELETE SET NULL;
CREATE INDEX runs_report_document ON runs (report_document_id) WHERE report_document_id IS NOT NULL;
