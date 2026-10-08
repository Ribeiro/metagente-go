-- A small source database to try the Extractor with, for Oracle: 2500 orders, the same as demo-source.sql.
-- Run it once, as the user that owns the tables (sqlplus owner@//db.example.com:1521/ORCLPDB1 @migrations/demo-source.oracle.sql).
--
-- `document` is personal data: the statement `page` of sources/source.oracle.toml masks it at the source, so that
-- only its last four digits ever leave this database. `note` is free text: the model step reads it, so
-- decide with the owner of data protection whether it may leave (see "The model step" in README.md).

-- Oracle treats an empty text as nothing, so the notes that are empty here are nothing there; the Worker treats both alike.

CREATE TABLE orders (
  id       NUMBER(19) PRIMARY KEY,
  customer VARCHAR2(100) NOT NULL,
  document VARCHAR2(20) NOT NULL,
  total    NUMBER(12,2) NOT NULL,
  note     VARCHAR2(500)     -- what the customer wrote on the order: free text, and it may hold personal data
);

INSERT INTO orders (id, customer, document, total, note)
SELECT LEVEL, 'Customer ' || LEVEL, LPAD(TO_CHAR(LEVEL * 7919), 11, '0'), ROUND(LEVEL * 1.37, 2),
       CASE MOD(LEVEL, 5)
         WHEN 0 THEN 'Please wrap it as a gift'
         WHEN 1 THEN 'Leave it with the neighbour if nobody is home'
         WHEN 2 THEN 'The box came damaged and I want my money back'
         WHEN 3 THEN NULL
         ELSE ''
       END
FROM dual
CONNECT BY LEVEL <= 2500;
COMMIT;
