-- The destination of the Worker, for SQL Server (2022 or later). Run it once, in the database of the warehouse, with
-- a user that may make tables:
--
--   sqlcmd -S db.example.com -d warehouse -U owner -i migrations/destination.sqlserver.sql
--
-- Then give the user of the Worker only what it needs (select, insert, update and delete on these tables).
-- Metagente does not make tables: this is a migration of yours. The tables are the same as in
-- destination.sqlite.sql, in the types of SQL Server. One difference: etl_resends has a row for each request,
-- because the dialect has no upsert that the sql tool accepts (see metagente.sqlserver.toml).

CREATE TABLE etl_meta (
  schema_version INT NOT NULL
);
INSERT INTO etl_meta (schema_version) VALUES (1);

CREATE TABLE etl_jobs (
  job_id           NVARCHAR(100) NOT NULL PRIMARY KEY,
  state            NVARCHAR(20)  NOT NULL DEFAULT 'running' CHECK (state IN ('running', 'paused', 'done', 'failed', 'mismatch')),
  pause_reason     NVARCHAR(200) NULL,
  total_batches    BIGINT NULL,
  total_rows       BIGINT NULL,
  max_model_calls  BIGINT NULL,
  max_model_tokens BIGINT NULL,
  budget_warned    INT NOT NULL DEFAULT 0,
  totals_at        DATETIME2 NULL,
  started_at       DATETIME2 NOT NULL DEFAULT SYSUTCDATETIME(),
  finished_at      DATETIME2 NULL
);

CREATE TABLE etl_batches (
  job_id            NVARCHAR(100) NOT NULL,
  seq               BIGINT NOT NULL,
  state             NVARCHAR(20) NOT NULL CHECK (state IN ('landed', 'done', 'failed')),
  rows_read         BIGINT NOT NULL,
  rows_loaded       BIGINT NULL,
  rows_rejected     BIGINT NULL,
  attempts          INT NOT NULL DEFAULT 0,
  landed_at         DATETIME2 NOT NULL DEFAULT SYSUTCDATETIME(),
  failed_at         DATETIME2 NULL,
  done_at           DATETIME2 NULL,
  transform_version NVARCHAR(100) NULL,
  last_error_code   NVARCHAR(100) NULL,
  enrich_version    NVARCHAR(100) NULL,
  model_calls       BIGINT NOT NULL DEFAULT 0,
  model_tokens      BIGINT NOT NULL DEFAULT 0,
  PRIMARY KEY (job_id, seq)
);

CREATE TABLE etl_rejects (
  job_id      NVARCHAR(100) NOT NULL,
  seq         BIGINT NOT NULL,
  source_key  BIGINT NOT NULL,
  reason_code NVARCHAR(100) NOT NULL,
  PRIMARY KEY (job_id, seq, source_key, reason_code)
);

CREATE TABLE stg_orders (
  job_id     NVARCHAR(100) NOT NULL,
  seq        BIGINT NOT NULL,
  source_key BIGINT NOT NULL,
  customer   NVARCHAR(400) NULL,
  document   NVARCHAR(100) NULL,
  total      FLOAT NULL,
  note       NVARCHAR(MAX) NULL,
  note_category  NVARCHAR(20) NULL,
  enrich_version NVARCHAR(100) NULL,
  enrich_tries   INT NOT NULL DEFAULT 0,
  PRIMARY KEY (job_id, seq, source_key)
);

CREATE TABLE orders_final (
  id            BIGINT NOT NULL PRIMARY KEY,
  customer      NVARCHAR(400) NOT NULL,
  document_tail NVARCHAR(10)  NOT NULL,
  total_cents   BIGINT NOT NULL,
  note_category NVARCHAR(20) NULL,
  loaded_job    NVARCHAR(100) NOT NULL,
  loaded_at     DATETIME2 NOT NULL DEFAULT SYSUTCDATETIME()
);

-- What the sweeper (sweeper.ag) keeps; see destination.sqlite.sql.
CREATE TABLE etl_alerts (
  job_id     NVARCHAR(100) NOT NULL,
  kind       NVARCHAR(40)  NOT NULL,
  ref        NVARCHAR(100) NOT NULL,
  alerted_at DATETIME2 NOT NULL DEFAULT SYSUTCDATETIME(),
  PRIMARY KEY (job_id, kind, ref)
);

CREATE TABLE etl_incidents (
  job_id NVARCHAR(100) NOT NULL,
  seq    BIGINT NOT NULL,
  code   NVARCHAR(100) NOT NULL,
  noted_at DATETIME2 NOT NULL DEFAULT SYSUTCDATETIME(),
  PRIMARY KEY (job_id, seq, code)
);

-- One row for each request to send a batch again: the number of the request is part of the key.
CREATE TABLE etl_resends (
  job_id   NVARCHAR(100) NOT NULL,
  seq      BIGINT NOT NULL,
  requests INT NOT NULL,
  PRIMARY KEY (job_id, seq, requests)
);
