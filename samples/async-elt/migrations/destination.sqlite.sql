-- The destination of the Worker, for SQLite (the demo, and the tests). Make the file once:
--
--   sqlite3 dest/warehouse.db < migrations/destination.sqlite.sql
--
-- Metagente does not make tables: this is a migration of yours. The Worker checks `etl_meta` before it
-- takes a batch, and refuses to work with a version it does not know. For PostgreSQL see
-- destination.postgres.sql. The control tables hold no personal data (only keys and codes); the staging
-- table holds the rows as the Extractor masked them, so it has a purge (see `purge` in worker.ag).

CREATE TABLE etl_meta (
  schema_version INTEGER NOT NULL
);
INSERT INTO etl_meta (schema_version) VALUES (1);

-- A job is `running` until every batch it announced is done; then `done`, or `mismatch` when the counts
-- of rows do not add up. It is `paused` when a brake stopped it (too many batches in a row with too many
-- rejected rows, or the budget of the model step spent); `pause_reason` says which. A job has the model step
-- only when it has a budget: `max_model_calls` and `max_model_tokens` are set with the message `budget`.
CREATE TABLE etl_jobs (
  job_id           TEXT PRIMARY KEY,
  state            TEXT NOT NULL DEFAULT 'running' CHECK (state IN ('running', 'paused', 'done', 'failed', 'mismatch')),
  pause_reason     TEXT,
  total_batches    INTEGER,
  total_rows       INTEGER,
  max_model_calls  INTEGER,
  max_model_tokens INTEGER,
  budget_warned    INTEGER NOT NULL DEFAULT 0,
  totals_at        TEXT,             -- when the Extractor said how many batches to expect: the sweeper waits after it
  started_at       TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  finished_at      TEXT
);

-- The state of a batch is changed in the same transaction as its data: landed, then done. A batch that
-- was stopped by a brake is `failed`, and waits for a retransform.
CREATE TABLE etl_batches (
  job_id            TEXT    NOT NULL,
  seq               INTEGER NOT NULL,
  state             TEXT    NOT NULL CHECK (state IN ('landed', 'done', 'failed')),
  rows_read         INTEGER NOT NULL,
  rows_loaded       INTEGER,
  rows_rejected     INTEGER,
  attempts          INTEGER NOT NULL DEFAULT 0,
  landed_at         TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP,
  failed_at         TEXT,
  done_at           TEXT,
  transform_version TEXT,
  last_error_code   TEXT,
  enrich_version    TEXT,
  model_calls       INTEGER NOT NULL DEFAULT 0,
  model_tokens      INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (job_id, seq)
);

-- A rejected row: its key and a code, never its content.
CREATE TABLE etl_rejects (
  job_id      TEXT    NOT NULL,
  seq         INTEGER NOT NULL,
  source_key  INTEGER NOT NULL,
  reason_code TEXT    NOT NULL,
  PRIMARY KEY (job_id, seq, source_key, reason_code)
);

-- Where a batch lands, as it came (and as the Extractor masked it).
CREATE TABLE stg_orders (
  job_id     TEXT    NOT NULL,
  seq        INTEGER NOT NULL,
  source_key INTEGER NOT NULL,
  customer   TEXT,
  document   TEXT,
  total      REAL,
  note       TEXT,
  -- The answer of the model step for `note`, and which version of the question gave it.
  note_category  TEXT,
  enrich_version TEXT,
  enrich_tries   INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (job_id, seq, source_key)
);

-- The final table, written by an upsert on the business key.
CREATE TABLE orders_final (
  id           INTEGER PRIMARY KEY,
  customer     TEXT    NOT NULL,
  document_tail TEXT   NOT NULL,
  total_cents  INTEGER NOT NULL,
  note_category TEXT,
  loaded_job   TEXT    NOT NULL,
  loaded_at    TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- What the sweeper (sweeper.ag) keeps: the alerts it already sent (one for each cause, so that a problem is told
-- once and not at every sweep), the events the Worker refused for what they are (a damaged event, a version it
-- does not know: a code and a place, never the content), and how many times it asked for a batch to be sent again.
CREATE TABLE etl_alerts (
  job_id     TEXT NOT NULL,
  kind       TEXT NOT NULL,
  ref        TEXT NOT NULL,
  alerted_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (job_id, kind, ref)
);

CREATE TABLE etl_incidents (
  job_id TEXT    NOT NULL,
  seq    INTEGER NOT NULL,
  code   TEXT    NOT NULL,
  at     TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (job_id, seq, code)
);

CREATE TABLE etl_resends (
  job_id   TEXT    NOT NULL,
  seq      INTEGER NOT NULL,
  requests INTEGER NOT NULL,
  PRIMARY KEY (job_id, seq)
);
