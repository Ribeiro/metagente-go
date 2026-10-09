-- A small source: 2500 orders. Every 500th order has a negative total, so that the Worker has something to reject.
CREATE TABLE orders (
  id       INTEGER PRIMARY KEY,
  customer TEXT NOT NULL,
  document TEXT NOT NULL,
  total    REAL NOT NULL
);

WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 2500)
INSERT INTO orders (id, customer, document, total)
SELECT i, 'Customer ' || i, printf('%011d', i * 7919),
       CASE WHEN i % 500 = 0 THEN -1.0 ELSE round(10 + (i % 97) * 1.5, 2) END
FROM n;
