-- A small source database to try the Extractor with, for PostgreSQL: 2500 orders, the same as demo-source.sql.
-- Run it once, as the user that owns the tables (psql owner@db.example.com/orders -f migrations/demo-source.postgres.sql).
--
-- `document` is personal data: the statement `page` of sources/source.postgres.toml masks it at the source, so that
-- only its last four digits ever leave this database. `note` is free text: the model step reads it, so
-- decide with the owner of data protection whether it may leave (see "The model step" in README.md).

CREATE TABLE orders (
  id       BIGINT PRIMARY KEY,
  customer VARCHAR(100) NOT NULL,
  document VARCHAR(20) NOT NULL,
  total    NUMERIC(12,2) NOT NULL,
  note     VARCHAR(500)      -- what the customer wrote on the order: free text, and it may hold personal data
);

INSERT INTO orders (id, customer, document, total, note)
SELECT i, 'Customer ' || i, lpad((i * 7919)::text, 11, '0'), round(i * 1.37, 2),
       CASE i % 5
         WHEN 0 THEN 'Please wrap it as a gift'
         WHEN 1 THEN 'Leave it with the neighbour if nobody is home'
         WHEN 2 THEN 'The box came damaged and I want my money back'
         WHEN 3 THEN NULL
         ELSE ''
       END
FROM generate_series(1, 2500) AS i;
