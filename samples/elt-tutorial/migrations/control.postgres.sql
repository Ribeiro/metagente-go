-- The control tables of the Worker, for PostgreSQL: the same as control.sql, in the types of PostgreSQL. Run it
-- once, with a user that may make tables:
--
--   psql -h db.example.com -U owner warehouse -f migrations/control.postgres.sql
--
-- Then give the user of the Worker only what it needs (select, insert, update and delete on these tables).

CREATE TABLE etl_meta (
  schema_version INTEGER NOT NULL
);
INSERT INTO etl_meta (schema_version) VALUES (1);

CREATE TABLE etl_jobs (
  job_id        TEXT PRIMARY KEY,
  state         TEXT NOT NULL DEFAULT 'running' CHECK (state IN ('running', 'paused', 'done', 'failed', 'mismatch')),
  pause_reason  TEXT,
  total_batches BIGINT,
  total_rows    BIGINT,
  totals_at     TIMESTAMPTZ,
  started_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  finished_at   TIMESTAMPTZ
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
  failed_at         TIMESTAMPTZ,
  done_at           TIMESTAMPTZ,
  transform_version TEXT,
  last_error_code   TEXT,
  PRIMARY KEY (job_id, seq)
);

CREATE TABLE etl_rejects (
  job_id      TEXT   NOT NULL,
  seq         BIGINT NOT NULL,
  source_key  BIGINT NOT NULL,
  reason_code TEXT   NOT NULL,
  PRIMARY KEY (job_id, seq, source_key, reason_code)
);

CREATE TABLE etl_alerts (
  job_id     TEXT NOT NULL,
  kind       TEXT NOT NULL,
  ref        TEXT NOT NULL,
  alerted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (job_id, kind, ref)
);

CREATE TABLE etl_incidents (
  job_id TEXT   NOT NULL,
  seq    BIGINT NOT NULL,
  code   TEXT   NOT NULL,
  at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (job_id, seq, code)
);

CREATE TABLE etl_resends (
  job_id   TEXT   NOT NULL,
  seq      BIGINT NOT NULL,
  requests INTEGER NOT NULL,
  PRIMARY KEY (job_id, seq)
);
