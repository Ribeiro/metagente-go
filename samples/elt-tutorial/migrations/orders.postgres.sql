-- The two tables that belong to the copy of `orders`, in a PostgreSQL destination: where a batch lands, and the
-- final table. Run it once, after control.postgres.sql. See orders.sql for what each one is.

CREATE TABLE stg_orders (
  job_id   TEXT   NOT NULL,
  seq      BIGINT NOT NULL,
  id       BIGINT NOT NULL,
  customer TEXT,
  document TEXT,
  total    DOUBLE PRECISION,
  PRIMARY KEY (job_id, seq, id)
);

CREATE TABLE orders_final (
  id            BIGINT PRIMARY KEY,
  customer      TEXT   NOT NULL,
  document_tail TEXT   NOT NULL,
  total_cents   BIGINT NOT NULL,
  loaded_job    TEXT   NOT NULL
);
