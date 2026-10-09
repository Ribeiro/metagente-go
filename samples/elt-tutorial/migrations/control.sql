-- The control tables of the Worker: what it knows about each job and each batch. They are the same for every
-- table you copy (the statements that a description makes use them, and so do the sweeper and the cleaning),
-- and they hold keys and codes, never the content of a row. Make them once:
--
--   sqlite3 warehouse.db < migrations/control.sql
--
-- Metagente makes no tables. The Worker checks `etl_meta` before it takes a batch, and refuses to work with a
-- version it does not know. For PostgreSQL see ../async-elt/migrations/destination.postgres.sql, which has the
-- same tables.

CREATE TABLE etl_meta (
  schema_version INTEGER NOT NULL
);
INSERT INTO etl_meta (schema_version) VALUES (1);

-- A job is `running` until every batch it announced is done; then `done`, or `mismatch` when the counts of
-- rows do not add up. It is `paused` when a brake stopped it; `pause_reason` says which.
CREATE TABLE etl_jobs (
  job_id        TEXT PRIMARY KEY,
  state         TEXT NOT NULL DEFAULT 'running' CHECK (state IN ('running', 'paused', 'done', 'failed', 'mismatch')),
  pause_reason  TEXT,
  total_batches INTEGER,
  total_rows    INTEGER,
  totals_at     TEXT,
  started_at    TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  finished_at   TEXT
);

-- The state of a batch is changed in the same transaction as its data: landed, then done. A batch that was
-- stopped by a brake is `failed`.
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

-- What the sweeper keeps: the alerts it already sent, the events the Worker refused for what they are, and
-- how many times it asked for a batch to be sent again.
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
