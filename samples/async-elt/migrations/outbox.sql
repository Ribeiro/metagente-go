-- The outbox of the Extractor: a small SQLite file on the machine of the Extractor.
-- It keeps the edges of every batch, so that a batch that was planned and never confirmed by the broker
-- can be built again exactly the same after a restart. Make the file once:
--
--   sqlite3 state/outbox.db < migrations/outbox.sql
--
-- Metagente does not make tables: this is a migration of yours. The Extractor checks the version below.

CREATE TABLE outbox_meta (
  schema_version INTEGER NOT NULL
);
INSERT INTO outbox_meta (schema_version) VALUES (1);

CREATE TABLE outbox (
  job_id       TEXT    NOT NULL,
  seq          INTEGER NOT NULL,
  after_key    INTEGER NOT NULL,   -- the batch holds the rows with a key greater than this ...
  upto_key     INTEGER NOT NULL,   -- ... and not greater than this
  row_count    INTEGER NOT NULL,
  state        TEXT    NOT NULL CHECK (state IN ('planned', 'published')),
  planned_at   TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP,
  published_at TEXT,
  PRIMARY KEY (job_id, seq)
);
