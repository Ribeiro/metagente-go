-- The control tables of the Worker, for MySQL and MariaDB: the same as control.sql, in the types of these databases (the
-- same file serves both). Run it once, with a user that may make tables:
--
--   mysql -h db.example.com -u owner warehouse < migrations/control.mysql.sql
--
-- Then give the user of the Worker only what it needs (select, insert, update and delete on these tables).

CREATE TABLE etl_meta (
  schema_version INT NOT NULL
);
INSERT INTO etl_meta (schema_version) VALUES (1);

CREATE TABLE etl_jobs (
  job_id        VARCHAR(100) NOT NULL PRIMARY KEY,
  state         VARCHAR(20)  NOT NULL DEFAULT 'running' CHECK (state IN ('running', 'paused', 'done', 'failed', 'mismatch')),
  pause_reason  VARCHAR(200) NULL,
  total_batches BIGINT NULL,
  total_rows    BIGINT NULL,
  totals_at     DATETIME(6) NULL,
  started_at    DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  finished_at   DATETIME(6) NULL
);

CREATE TABLE etl_batches (
  job_id            VARCHAR(100) NOT NULL,
  seq               BIGINT NOT NULL,
  state             VARCHAR(20)  NOT NULL CHECK (state IN ('landed', 'done', 'failed')),
  rows_read         BIGINT NOT NULL,
  rows_loaded       BIGINT NULL,
  rows_rejected     BIGINT NULL,
  attempts          INT NOT NULL DEFAULT 0,
  landed_at         DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  failed_at         DATETIME(6) NULL,
  done_at           DATETIME(6) NULL,
  transform_version VARCHAR(100) NULL,
  last_error_code   VARCHAR(100) NULL,
  PRIMARY KEY (job_id, seq)
);

CREATE TABLE etl_rejects (
  job_id      VARCHAR(100) NOT NULL,
  seq         BIGINT NOT NULL,
  source_key  BIGINT NOT NULL,
  reason_code VARCHAR(100) NOT NULL,
  PRIMARY KEY (job_id, seq, source_key, reason_code)
);

CREATE TABLE etl_alerts (
  job_id     VARCHAR(100) NOT NULL,
  kind       VARCHAR(40)  NOT NULL,
  ref        VARCHAR(100) NOT NULL,
  alerted_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  PRIMARY KEY (job_id, kind, ref)
);

CREATE TABLE etl_incidents (
  job_id   VARCHAR(100) NOT NULL,
  seq      BIGINT NOT NULL,
  code     VARCHAR(100) NOT NULL,
  noted_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  PRIMARY KEY (job_id, seq, code)
);

CREATE TABLE etl_resends (
  job_id   VARCHAR(100) NOT NULL,
  seq      BIGINT NOT NULL,
  requests INT NOT NULL,
  PRIMARY KEY (job_id, seq)
);
