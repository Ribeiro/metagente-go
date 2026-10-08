-- A small source database to try the Extractor with: 2500 orders. Make the file once:
--
--   sqlite3 demo/source.db < migrations/demo-source.sql
--
-- `document` is personal data: the statement `page` of metagente.toml masks it at the source, so that
-- only its last four digits ever leave this database.

CREATE TABLE orders (
  id       INTEGER PRIMARY KEY,
  customer TEXT NOT NULL,
  document TEXT NOT NULL,
  total    REAL NOT NULL
);

WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 2500)
INSERT INTO orders (id, customer, document, total)
SELECT i, 'Customer ' || i, printf('%011d', i * 7919), round(i * 1.37, 2) FROM n;
