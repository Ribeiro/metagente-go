-- The two tables that belong to the copy of `orders`, in a MySQL or MariaDB destination: where a batch lands, and the final
-- table. Run it once, after control.mysql.sql. See orders.sql for what each one is.

CREATE TABLE stg_orders (
  job_id   VARCHAR(100) NOT NULL,
  seq      BIGINT NOT NULL,
  id       BIGINT NOT NULL,
  customer VARCHAR(400) NULL,
  document VARCHAR(100) NULL,
  total    DOUBLE NULL,
  PRIMARY KEY (job_id, seq, id)
);

CREATE TABLE orders_final (
  id            BIGINT NOT NULL PRIMARY KEY,
  customer      VARCHAR(400) NOT NULL,
  document_tail VARCHAR(10)  NOT NULL,
  total_cents   BIGINT NOT NULL,
  loaded_job    VARCHAR(100) NOT NULL
);
