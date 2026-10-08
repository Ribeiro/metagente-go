-- The destination of the Worker, for Oracle (19c or later). Run it once, as the user that owns the tables:
--
--   sqlplus owner@//db.example.com:1521/ORCLPDB1 @migrations/destination.oracle.sql
--
-- Then give the user of the Worker only what it needs (select, insert, update and delete on these tables).
-- Metagente does not make tables: this is a migration of yours. The tables are the same as in
-- destination.sqlite.sql, in the types of Oracle. Two differences come from Oracle itself: an empty text is
-- nothing (so an empty customer is stored as nothing, and the Worker rejects it all the same), and etl_resends has
-- a row for each request, because the dialect has no upsert that the sql tool accepts (see metagente.oracle.toml).

CREATE TABLE etl_meta (
  schema_version INTEGER NOT NULL
);
INSERT INTO etl_meta (schema_version) VALUES (1);

CREATE TABLE etl_jobs (
  job_id           VARCHAR2(100) PRIMARY KEY,
  state            VARCHAR2(20) DEFAULT 'running' NOT NULL CHECK (state IN ('running', 'paused', 'done', 'failed', 'mismatch')),
  pause_reason     VARCHAR2(200),
  total_batches    NUMBER(19),
  total_rows       NUMBER(19),
  max_model_calls  NUMBER(19),
  max_model_tokens NUMBER(19),
  budget_warned    INTEGER DEFAULT 0 NOT NULL,
  totals_at        TIMESTAMP WITH TIME ZONE,
  started_at       TIMESTAMP WITH TIME ZONE DEFAULT SYSTIMESTAMP NOT NULL,
  finished_at      TIMESTAMP WITH TIME ZONE
);

CREATE TABLE etl_batches (
  job_id            VARCHAR2(100) NOT NULL,
  seq               NUMBER(19) NOT NULL,
  state             VARCHAR2(20) NOT NULL CHECK (state IN ('landed', 'done', 'failed')),
  rows_read         NUMBER(19) NOT NULL,
  rows_loaded       NUMBER(19),
  rows_rejected     NUMBER(19),
  attempts          INTEGER DEFAULT 0 NOT NULL,
  landed_at         TIMESTAMP WITH TIME ZONE DEFAULT SYSTIMESTAMP NOT NULL,
  failed_at         TIMESTAMP WITH TIME ZONE,
  done_at           TIMESTAMP WITH TIME ZONE,
  transform_version VARCHAR2(100),
  last_error_code   VARCHAR2(100),
  enrich_version    VARCHAR2(100),
  model_calls       NUMBER(19) DEFAULT 0 NOT NULL,
  model_tokens      NUMBER(19) DEFAULT 0 NOT NULL,
  PRIMARY KEY (job_id, seq)
);

CREATE TABLE etl_rejects (
  job_id      VARCHAR2(100) NOT NULL,
  seq         NUMBER(19) NOT NULL,
  source_key  NUMBER(19) NOT NULL,
  reason_code VARCHAR2(100) NOT NULL,
  PRIMARY KEY (job_id, seq, source_key, reason_code)
);

CREATE TABLE stg_orders (
  job_id     VARCHAR2(100) NOT NULL,
  seq        NUMBER(19) NOT NULL,
  source_key NUMBER(19) NOT NULL,
  customer   VARCHAR2(400),
  document   VARCHAR2(100),
  total      BINARY_DOUBLE,
  note       VARCHAR2(4000),
  note_category  VARCHAR2(20),
  enrich_version VARCHAR2(100),
  enrich_tries   INTEGER DEFAULT 0 NOT NULL,
  PRIMARY KEY (job_id, seq, source_key)
);

CREATE TABLE orders_final (
  id            NUMBER(19) PRIMARY KEY,
  customer      VARCHAR2(400) NOT NULL,
  document_tail VARCHAR2(10) NOT NULL,
  total_cents   NUMBER(19) NOT NULL,
  note_category VARCHAR2(20),
  loaded_job    VARCHAR2(100) NOT NULL,
  loaded_at     TIMESTAMP WITH TIME ZONE DEFAULT SYSTIMESTAMP NOT NULL
);

-- What the sweeper (sweeper.ag) keeps; see destination.sqlite.sql.
CREATE TABLE etl_alerts (
  job_id     VARCHAR2(100) NOT NULL,
  kind       VARCHAR2(40) NOT NULL,
  ref        VARCHAR2(100) NOT NULL,
  alerted_at TIMESTAMP WITH TIME ZONE DEFAULT SYSTIMESTAMP NOT NULL,
  PRIMARY KEY (job_id, kind, ref)
);

CREATE TABLE etl_incidents (
  job_id   VARCHAR2(100) NOT NULL,
  seq      NUMBER(19) NOT NULL,
  code     VARCHAR2(100) NOT NULL,
  noted_at TIMESTAMP WITH TIME ZONE DEFAULT SYSTIMESTAMP NOT NULL,
  PRIMARY KEY (job_id, seq, code)
);

-- One row for each request to send a batch again: the number of the request is part of the key.
CREATE TABLE etl_resends (
  job_id   VARCHAR2(100) NOT NULL,
  seq      NUMBER(19) NOT NULL,
  requests INTEGER NOT NULL,
  PRIMARY KEY (job_id, seq, requests)
);

COMMIT;
