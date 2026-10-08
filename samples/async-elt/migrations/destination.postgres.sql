-- The destination of the Worker, for PostgreSQL. Run it once, with a user that may make tables:
--
--   psql -h db.example.com -U owner warehouse -f migrations/destination.postgres.sql
--
-- Then give the user of the Worker only what it needs (select, insert, update and delete on these tables).
-- Metagente does not make tables: this is a migration of yours. The tables are the same as in
-- destination.sqlite.sql, in the types of PostgreSQL.

CREATE TABLE etl_meta (
  schema_version INTEGER NOT NULL
);
INSERT INTO etl_meta (schema_version) VALUES (1);

CREATE TABLE etl_jobs (
  job_id           TEXT PRIMARY KEY,
  state            TEXT NOT NULL DEFAULT 'running' CHECK (state IN ('running', 'paused', 'done', 'failed', 'mismatch')),
  pause_reason     TEXT,
  total_batches    BIGINT,
  total_rows       BIGINT,
  max_model_calls  BIGINT,
  max_model_tokens BIGINT,
  budget_warned    INTEGER NOT NULL DEFAULT 0,
  started_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  finished_at      TIMESTAMPTZ
);

CREATE TABLE etl_batches (
  job_id            TEXT   NOT NULL,
  seq               BIGINT NOT NULL,
  state             TEXT   NOT NULL CHECK (state IN ('landed', 'done', 'failed')),
  rows_read         BIGINT NOT NULL,
  rows_loaded       BIGINT,
  rows_rejected     BIGINT,
  attempts          INTEGER NOT NULL DEFAULT 0,
  landed_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  done_at           TIMESTAMPTZ,
  transform_version TEXT,
  last_error_code   TEXT,
  enrich_version    TEXT,
  model_calls       BIGINT NOT NULL DEFAULT 0,
  model_tokens      BIGINT NOT NULL DEFAULT 0,
  PRIMARY KEY (job_id, seq)
);

CREATE TABLE etl_rejects (
  job_id      TEXT   NOT NULL,
  seq         BIGINT NOT NULL,
  source_key  BIGINT NOT NULL,
  reason_code TEXT   NOT NULL,
  PRIMARY KEY (job_id, seq, source_key, reason_code)
);

CREATE TABLE stg_orders (
  job_id     TEXT   NOT NULL,
  seq        BIGINT NOT NULL,
  source_key BIGINT NOT NULL,
  customer   TEXT,
  document   TEXT,
  total      DOUBLE PRECISION,
  note       TEXT,
  note_category  TEXT,
  enrich_version TEXT,
  enrich_tries   INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (job_id, seq, source_key)
);

CREATE TABLE orders_final (
  id            BIGINT PRIMARY KEY,
  customer      TEXT   NOT NULL,
  document_tail TEXT   NOT NULL,
  total_cents   BIGINT NOT NULL,
  note_category TEXT,
  loaded_job    TEXT   NOT NULL,
  loaded_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
