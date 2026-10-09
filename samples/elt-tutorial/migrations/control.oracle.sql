-- The control tables of the Worker, for Oracle (19c or later): the same as control.sql, in the types of Oracle. Run it
-- once, as the user that owns the tables:
--
--   sqlplus owner@//db.example.com:1521/ORCLPDB1 @migrations/control.oracle.sql
--
-- Then give the user of the Worker only what it needs (select, insert, update and delete on these tables).
-- Two differences come from Oracle itself: an empty text is nothing (so an empty customer is stored as nothing, and a
-- rule that rejects it says `customer IS NULL`), and etl_resends has a row for each request, because the dialect has no
-- upsert that the sql tool accepts for it.

CREATE TABLE etl_meta (
  schema_version INTEGER NOT NULL
);
INSERT INTO etl_meta (schema_version) VALUES (1);

CREATE TABLE etl_jobs (
  job_id        VARCHAR2(100) PRIMARY KEY,
  state         VARCHAR2(20) DEFAULT 'running' NOT NULL CHECK (state IN ('running', 'paused', 'done', 'failed', 'mismatch')),
  pause_reason  VARCHAR2(200),
  total_batches NUMBER(19),
  total_rows    NUMBER(19),
  totals_at     TIMESTAMP WITH TIME ZONE,
  started_at    TIMESTAMP WITH TIME ZONE DEFAULT SYSTIMESTAMP NOT NULL,
  finished_at   TIMESTAMP WITH TIME ZONE
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
  PRIMARY KEY (job_id, seq)
);

CREATE TABLE etl_rejects (
  job_id      VARCHAR2(100) NOT NULL,
  seq         NUMBER(19) NOT NULL,
  source_key  NUMBER(19) NOT NULL,
  reason_code VARCHAR2(100) NOT NULL,
  PRIMARY KEY (job_id, seq, source_key, reason_code)
);

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

CREATE TABLE etl_resends (
  job_id   VARCHAR2(100) NOT NULL,
  seq      NUMBER(19) NOT NULL,
  requests INTEGER NOT NULL,
  PRIMARY KEY (job_id, seq, requests)
);

COMMIT;
