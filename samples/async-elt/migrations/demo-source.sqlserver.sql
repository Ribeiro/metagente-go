-- A small source database to try the Extractor with, for SQL Server: 2500 orders, the same as demo-source.sql.
-- Run it once, as the user that owns the tables (sqlcmd -S db.example.com -d orders -i migrations/demo-source.sqlserver.sql).
--
-- `document` is personal data: the statement `page` of sources/source.sqlserver.toml masks it at the source, so that
-- only its last four digits ever leave this database. `note` is free text: the model step reads it, so
-- decide with the owner of data protection whether it may leave (see "The model step" in README.md).

CREATE TABLE orders (
  id       BIGINT PRIMARY KEY,
  customer NVARCHAR(100) NOT NULL,
  document NVARCHAR(20) NOT NULL,
  total    DECIMAL(12,2) NOT NULL,
  note     NVARCHAR(500)      -- what the customer wrote on the order: free text, and it may hold personal data
);

WITH n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 2500)
INSERT INTO orders (id, customer, document, total, note)
SELECT i, CONCAT('Customer ', i), RIGHT(CONCAT('00000000000', i * 7919), 11), ROUND(i * 1.37, 2),
       CASE i % 5
         WHEN 0 THEN 'Please wrap it as a gift'
         WHEN 1 THEN 'Leave it with the neighbour if nobody is home'
         WHEN 2 THEN 'The box came damaged and I want my money back'
         WHEN 3 THEN NULL
         ELSE ''
       END
FROM n
OPTION (MAXRECURSION 0);
