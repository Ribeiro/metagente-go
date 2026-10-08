-- A small source database to try the Extractor with, for MySQL 8 and MariaDB 10.5 or later: 2500 orders, the same as
-- demo-source.sql. Run it once, as the user that owns the tables:
--
--   mysql -h db.example.com -u owner -p orders < migrations/demo-source.mysql.sql
--
-- `document` is personal data: the statement `page` of sources/source.mysql.toml (or source.mariadb.toml) masks it at
-- the source, so that only its last four digits ever leave this database. `note` is free text: the model step reads
-- it, so decide with the owner of data protection whether it may leave (see "The model step" in README.md).

CREATE TABLE orders (
  id       BIGINT PRIMARY KEY,
  customer TEXT NOT NULL,
  document TEXT NOT NULL,
  total    DECIMAL(12,2) NOT NULL,
  note     TEXT      -- what the customer wrote on the order: free text, and it may hold personal data
);

-- The numbers 1 to 2500 come from four digits, so that no limit of recursion gets in the way.
INSERT INTO orders (id, customer, document, total, note)
SELECT i, CONCAT('Customer ', i), LPAD(i * 7919, 11, '0'), ROUND(i * 1.37, 2),
       CASE i % 5
         WHEN 0 THEN 'Please wrap it as a gift'
         WHEN 1 THEN 'Leave it with the neighbour if nobody is home'
         WHEN 2 THEN 'The box came damaged and I want my money back'
         WHEN 3 THEN NULL
         ELSE ''
       END
FROM (
  SELECT a.n * 1000 + b.n * 100 + c.n * 10 + d.n AS i
  FROM (SELECT 0 AS n UNION ALL SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4 UNION ALL SELECT 5 UNION ALL SELECT 6 UNION ALL SELECT 7 UNION ALL SELECT 8 UNION ALL SELECT 9) a
  CROSS JOIN (SELECT 0 AS n UNION ALL SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4 UNION ALL SELECT 5 UNION ALL SELECT 6 UNION ALL SELECT 7 UNION ALL SELECT 8 UNION ALL SELECT 9) b
  CROSS JOIN (SELECT 0 AS n UNION ALL SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4 UNION ALL SELECT 5 UNION ALL SELECT 6 UNION ALL SELECT 7 UNION ALL SELECT 8 UNION ALL SELECT 9) c
  CROSS JOIN (SELECT 0 AS n UNION ALL SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4 UNION ALL SELECT 5 UNION ALL SELECT 6 UNION ALL SELECT 7 UNION ALL SELECT 8 UNION ALL SELECT 9) d
) numbers
WHERE i BETWEEN 1 AND 2500;
