-- A small source database to try the Extractor with: 2500 orders. Make the file once:
--
--   sqlite3 demo/source.db < migrations/demo-source.sql
--
-- `document` is personal data: the statement `page` of metagente.toml masks it at the source, so that
-- only its last four digits ever leave this database. `note` is free text: the model step reads it, so
-- decide with the owner of data protection whether it may leave (see "The model step" in README.md).

CREATE TABLE orders (
  id       INTEGER PRIMARY KEY,
  customer TEXT NOT NULL,
  document TEXT NOT NULL,
  total    REAL NOT NULL,
  note     TEXT              -- what the customer wrote on the order: free text, and it may hold personal data
);

WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 2500)
INSERT INTO orders (id, customer, document, total, note)
SELECT i, 'Customer ' || i, printf('%011d', i * 7919), round(i * 1.37, 2),
       CASE i % 5
         WHEN 0 THEN 'Please wrap it as a gift'
         WHEN 1 THEN 'Leave it with the neighbour if nobody is home'
         WHEN 2 THEN 'The box came damaged and I want my money back'
         WHEN 3 THEN NULL
         ELSE ''
       END
FROM n;
