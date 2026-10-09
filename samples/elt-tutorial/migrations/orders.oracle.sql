-- The two tables that belong to the copy of `orders`, in an Oracle destination: where a batch lands, and the final
-- table. Run it once, after control.oracle.sql. See orders.sql for what each one is.

CREATE TABLE stg_orders (
  job_id   VARCHAR2(100) NOT NULL,
  seq      NUMBER(19) NOT NULL,
  id       NUMBER(19) NOT NULL,
  customer VARCHAR2(400),
  document VARCHAR2(100),
  total    BINARY_DOUBLE,
  PRIMARY KEY (job_id, seq, id)
);

CREATE TABLE orders_final (
  id            NUMBER(19) PRIMARY KEY,
  customer      VARCHAR2(400) NOT NULL,
  document_tail VARCHAR2(10) NOT NULL,
  total_cents   NUMBER(19) NOT NULL,
  loaded_job    VARCHAR2(100) NOT NULL
);

COMMIT;
