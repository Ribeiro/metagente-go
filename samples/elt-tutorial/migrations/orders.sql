-- The two tables that belong to the copy of `orders`, in the destination: where a batch lands, and the final
-- table. Make them once, after control.sql:
--
--   sqlite3 warehouse.db < migrations/orders.sql

-- Staging: job_id and seq, then the columns of the event, with the same names. The key of the table is
-- part of the primary key. A batch lands here as it came (and as the Extractor masked it).
CREATE TABLE stg_orders (
  job_id   TEXT    NOT NULL,
  seq      INTEGER NOT NULL,
  id       INTEGER NOT NULL,
  customer TEXT,
  document TEXT,
  total    REAL,
  PRIMARY KEY (job_id, seq, id)
);

-- The final table, written by an upsert on its business key.
CREATE TABLE orders_final (
  id            INTEGER PRIMARY KEY,
  customer      TEXT    NOT NULL,
  document_tail TEXT    NOT NULL,
  total_cents   INTEGER NOT NULL,
  loaded_job    TEXT    NOT NULL
);
