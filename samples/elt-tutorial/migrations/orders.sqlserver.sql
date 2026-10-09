-- The two tables that belong to the copy of `orders`, in a SQL Server destination: where a batch lands, and the
-- final table. Run it once, after control.sqlserver.sql. See orders.sql for what each one is.

CREATE TABLE stg_orders (
  job_id   NVARCHAR(100) NOT NULL,
  seq      BIGINT NOT NULL,
  id       BIGINT NOT NULL,
  customer NVARCHAR(400) NULL,
  document NVARCHAR(100) NULL,
  total    FLOAT NULL,
  PRIMARY KEY (job_id, seq, id)
);

CREATE TABLE orders_final (
  id            BIGINT NOT NULL PRIMARY KEY,
  customer      NVARCHAR(400) NOT NULL,
  document_tail NVARCHAR(10)  NOT NULL,
  total_cents   BIGINT NOT NULL,
  loaded_job    NVARCHAR(100) NOT NULL
);
