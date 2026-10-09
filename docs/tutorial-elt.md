# Tutorial: an Extractor and a Worker for the asynchronous ELT

In this tutorial you build, from an empty folder, the two agents of the asynchronous ELT: an **Extractor**, that reads
a table of a source database and sends it in batches, and a **Worker**, that receives the batches, loads them into
another database and checks that nothing was lost. When you finish, you will have copied 2,500 orders from one SQLite
file to another, through a broker, and seen the books close.

You do not write the steps. Copying a table takes the same steps every time (read a page, save its edges, pack it, send
it; land it, transform it, count it), and what changes from a table to another is **which table, which columns, what must
not leave the source, and which rows are wrong**. So that is what you write, in a **description**, and each agent is one
line.

You should have done [your first agent](tutorial.md) first. The reasons behind each choice are in
[the design](design-async-elt.md). The files of this tutorial are in [`samples/elt-tutorial`](../samples/elt-tutorial/), and a
test runs them at every change. A bigger version, with a step with a language model and a sweeper that heals a job, is [the sample](../samples/async-elt/README.md), written step by step.

## 1. The idea in one page

```text
 ZONE OF THE SOURCE                 BROKER (JetStream)                 ZONE OF THE DESTINATION
┌────────────────────────┐      ┌───────────────────────┐       ┌──────────────────────────────┐
│ Extractor              │ ───► │ etl.<job>.batch       │ ───►  │ Worker                       │
│  reads pages by key    │      │ etl.<job>.control     │       │  lands, transforms, confirms │
│  keeps an outbox       │      └───────────────────────┘       └───────────────┬──────────────┘
└──────────┬─────────────┘                                                      │
       source DB                                                           destination DB
```

- The **Extractor** reads the table a page at a time, **by key** (`WHERE id > :after ORDER BY id LIMIT :size`), and
  sends each page as one *batch event*. Before it sends a page it writes the edges of the batch (`after`, `upto`) in a small
  database of its own, the **outbox**. If it stops, you run it again with the same job name: it builds again, from the
  same edges, any batch that was planned and never confirmed, and goes on from the last key.
- The **broker** keeps the events until someone takes them. Two events with the same id are one: the broker drops the copy.
- The **Worker** takes the events with `metagente consume`, which gives each event to the agent as a call. The call
  that ends with `reply` confirms the event; one that ends with `fail` asks for it again, or sends it to the *dead letters*.
  The Worker works in **two transactions**: the first *lands* the rows in a staging table and marks the batch `landed`;
  the second *transforms* them into the final table and marks the batch `done`. If it stops between the two, the event
  comes again and the Worker goes straight to the second.
- At the end the Extractor sends a **control event** with the totals. The job is closed as `done` when every batch is done
  and **rows read = rows loaded + rows rejected**. If the rows do not add up it is `mismatch`, and a person looks.

Metagente makes no tables. The databases are yours; the migrations below are yours too.

## 2. What you need

- `metagente` (see [step 1 of the first tutorial](tutorial.md#1-get-metagente)).
- `sqlite3`, to make the demo databases (any SQLite tool will do).
- A NATS server with JetStream, and a stream called `ETL` that takes `etl.>`. With Docker and the
  [`nats` command](https://github.com/nats-io/natscli):

  ```sh
  docker run -d --name nats -p 4222:4222 nats:2.10-alpine -js
  nats stream add ETL --subjects 'etl.>' --discard new --max-msgs 100000 --storage file --defaults
  ```

  `--discard new` makes a full stream refuse new events instead of throwing away old ones nobody has processed. A real
  stream also has a maximum age and size: see [the design](design-async-elt.md#14-sizing-without-numbers).

Make a folder and go into it. Everything below happens there. You can also copy
[`samples/elt-tutorial`](../samples/elt-tutorial/), which has all the files.

```sh
mkdir elt-tutorial && cd elt-tutorial
mkdir migrations
```

## 3. The source and the Extractor

### 3.1 The source database

`migrations/source.sql` makes 2,500 demo orders. Every 500th order has a negative total, so that the Worker will have
something to reject later.

```sql
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
```

```sh
sqlite3 source.db < migrations/source.sql
```

### 3.2 The outbox

The outbox is the small file where the Extractor keeps the edges of every batch. It is the same for every table.
`migrations/outbox.sql`:

```sql
-- The outbox of the Extractor: a small SQLite file on the machine of the Extractor.
-- It keeps the edges of every batch, so that a batch that was planned and never confirmed by the broker
-- can be built again exactly the same after a restart. Make the file once:
--
--   sqlite3 state/outbox.db < migrations/outbox.sql
--
-- Metagente does not make tables: this is a migration of yours. The Extractor checks the version below.

CREATE TABLE outbox_meta (
  schema_version INTEGER NOT NULL
);
INSERT INTO outbox_meta (schema_version) VALUES (1);

CREATE TABLE outbox (
  job_id       TEXT    NOT NULL,
  seq          INTEGER NOT NULL,
  after_key    INTEGER NOT NULL,   -- the batch holds the rows with a key greater than this ...
  upto_key     INTEGER NOT NULL,   -- ... and not greater than this
  row_count    INTEGER NOT NULL,
  state        TEXT    NOT NULL CHECK (state IN ('planned', 'published')),
  planned_at   TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP,
  published_at TEXT,
  PRIMARY KEY (job_id, seq)
);
```

```sh
sqlite3 outbox.db < migrations/outbox.sql
```

### 3.3 The description and the settings of the Extractor

Everything that depends on the place is in a settings file, and the agent names nothing: no database, no broker, no secret.
Make `extractor.toml`:

```toml
# The Extractor: reads the table `orders` of a source database and publishes it in batches.

[runtime]
timeout_seconds = 60
max_loop_turns = 1000000          # one turn for each page, and a big table has many pages

[sql.source]
driver = "sqlite"
path = "source.db"                # no `mode`, so it can only be read

[sql.outbox]
driver = "sqlite"
path = "outbox.db"
mode = "write"

[broker.main]
driver = "jetstream"
url = "nats://127.0.0.1:4222"
stream = "ETL"

# The description of the copy. The statements `page` and `range` of the source, and the ones of the outbox,
# are made from it, and `metagente trust` shows them.
[elt.orders]
key = "id"
columns = ["id", "customer", "document", "total"]

[elt.orders.source]
connection = "source"
table = "orders"
outbox = "outbox"
broker = "main"
mask = { document = "last 4" }    # only '***' and the last four digits leave the source
```

Read it in two parts.

**The places** are `[sql.source]`, `[sql.outbox]` and `[broker.main]`. The source has no `mode`, so Metagente opens it
**read only**: an agent that reads a source cannot change it. The outbox is a database the Extractor writes. For a broker
with a user and a password, add `user = "extractor"` and, in `[credentials]`, `events = "BROKER_PASSWORD"`: the file says
the *name* of the variable, never the password.

**A source that is not SQLite.** This tutorial reads a SQLite file so that you need nothing else, but the Extractor reads
PostgreSQL, **MySQL**, **MariaDB**, SQL Server and Oracle just the same. Only the place of the source changes, and the
statements are made in the dialect of each (`LIMIT` for MySQL and MariaDB, `TOP` for SQL Server, `FETCH FIRST` for Oracle; the
mask of `document` becomes `CONCAT('***', RIGHT(document, 4))` in MySQL and MariaDB). For MySQL or MariaDB:

```toml
[sql.source]
driver = "mysql"                  # or "mariadb"
host = "db.example.com"
database = "orders"
user = "extractor"                # a user that may only read; the password goes in [credentials] as source = "SOURCE_DB_PASSWORD"

[elt.orders.source]
select = { total = "CAST(total AS DOUBLE)" }   # a DECIMAL column is read as a number
```

`select` is how a column is read in the SQL of the source. The cast makes a `DECIMAL` column reach the Extractor as a number of the language, which is how the
sample reads it in every dialect; the other columns need nothing. The two databases are served by the same migration to try it
([`demo-source.mysql.sql`](../samples/async-elt/migrations/demo-source.mysql.sql)) and the same statements: MariaDB is named apart
only because it is another server and another driver.

**The description** is `[elt.orders]`. It says what the table is:

- `columns` are the columns of the event, in order, and `key` is the one that orders the table and marks where a batch ends
  (a whole number that grows; `id` if you leave it out).
- `source` says where the Extractor reads (`connection`, `table`), where it keeps its outbox, and which broker it sends to.
- **`mask = { document = "last 4" }`** is the part about personal data. The document is cut **in the statement that reads
  the source**: only `***` and its last four digits ever leave that database. What the `SELECT` does not give never leaves it,
  so the data is cut here and not later.

You did not write a single statement. `page` (a page of the table by key) and `range` (the same rows again, by the edges in the
outbox, which is how a batch is built again after a restart) are made from this description, with the mask, and so are the
statements of the outbox. They are the same text that a person would have written, and `metagente trust` will show them.

### 3.4 The Extractor

Make `extractor.ag`:

```text
agent Extractor
  goal "Copy the orders table to the broker, in batches"
  tool orders from elt "orders" extract
```

That is all. The line `tool orders from elt "orders" extract` says: this agent is the Extractor of the description
`orders`. When the file is loaded, the line is taken out and the agent is given what the description calls for:

- the tools `source`, `outbox`, `events`, `codec` and `clock`. The broker tool may publish **only** to `etl.*.batch` and
  `etl.*.control`;
- the message `extract job` (copy the table, and say how many batches and rows went out), and `resend`, which the sweeper
  of the destination uses to ask for a batch that never arrived;
- the loop that plans a batch (and halves a page until its rows fit), saves its edges in the outbox before anything is
  sent, packs the rows with a hash, publishes the event with the id `<job>:<seq>`, marks it published, and at the end
  sends the control event with the totals.

What comes out is an ordinary agent. It is the Extractor of [`samples/async-elt`](../samples/async-elt/README.md), which
you can read to see the steps.

### 3.5 Check, approve and run

```sh
metagente check extractor.ag --config extractor.toml
# No problems found in extractor.ag (1 agent).
metagente trust extractor.ag --config extractor.toml
```

`trust` lists what the agent will do: it connects to the broker and may publish on two subjects, it **reads** `source.db`
(with the statements `page` and `range`) and it **changes** `outbox.db` (with the statements of the outbox). You approve it
once for the project. If what the agent can reach changes, `metagente` asks again, and that includes the description: change
a mask or a column and the statements change, so you approve again.

```sh
metagente run extractor.ag extract job=demo --config extractor.toml
# job demo: 3 batches, 2500 rows
```

Three batches went to the broker: 1,000, 1,000 and 500 rows (a page is 1,000 rows unless the description says `rows`). Run
the same line again: it finds everything done, plans no new batch, and publishes the control event once more with the
same id (the broker drops it while it still remembers the id). **Run it with the same job name to go on after a stop; use a
new name for a new copy.**

## 4. The destination and the Worker

### 4.1 The destination database

The destination has two kinds of tables. The **control tables** are the same for every table you copy: what the Worker
knows about each job and each batch. `migrations/control.sql`:

```sql
-- The control tables of the Worker: what it knows about each job and each batch. They are the same for every
-- table you copy (the statements that a description makes use them, and so do the sweeper and the cleaning),
-- and they hold keys and codes, never the content of a row. Make them once:
--
--   sqlite3 warehouse.db < migrations/control.sql
--
-- Metagente makes no tables. The Worker checks `etl_meta` before it takes a batch, and refuses to work with a
-- version it does not know. For PostgreSQL see ../async-elt/migrations/destination.postgres.sql, which has the
-- same tables.

CREATE TABLE etl_meta (
  schema_version INTEGER NOT NULL
);
INSERT INTO etl_meta (schema_version) VALUES (1);

-- A job is `running` until every batch it announced is done; then `done`, or `mismatch` when the counts of
-- rows do not add up. It is `paused` when a brake stopped it; `pause_reason` says which.
CREATE TABLE etl_jobs (
  job_id        TEXT PRIMARY KEY,
  state         TEXT NOT NULL DEFAULT 'running' CHECK (state IN ('running', 'paused', 'done', 'failed', 'mismatch')),
  pause_reason  TEXT,
  total_batches INTEGER,
  total_rows    INTEGER,
  totals_at     TEXT,
  started_at    TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  finished_at   TEXT
);

-- The state of a batch is changed in the same transaction as its data: landed, then done. A batch that was
-- stopped by a brake is `failed`.
CREATE TABLE etl_batches (
  job_id            TEXT    NOT NULL,
  seq               INTEGER NOT NULL,
  state             TEXT    NOT NULL CHECK (state IN ('landed', 'done', 'failed')),
  rows_read         INTEGER NOT NULL,
  rows_loaded       INTEGER,
  rows_rejected     INTEGER,
  attempts          INTEGER NOT NULL DEFAULT 0,
  landed_at         TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP,
  failed_at         TEXT,
  done_at           TEXT,
  transform_version TEXT,
  last_error_code   TEXT,
  PRIMARY KEY (job_id, seq)
);

-- A rejected row: its key and a code, never its content.
CREATE TABLE etl_rejects (
  job_id      TEXT    NOT NULL,
  seq         INTEGER NOT NULL,
  source_key  INTEGER NOT NULL,
  reason_code TEXT    NOT NULL,
  PRIMARY KEY (job_id, seq, source_key, reason_code)
);

-- What the sweeper keeps: the alerts it already sent, the events the Worker refused for what they are, and
-- how many times it asked for a batch to be sent again.
CREATE TABLE etl_alerts (
  job_id     TEXT NOT NULL,
  kind       TEXT NOT NULL,
  ref        TEXT NOT NULL,
  alerted_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (job_id, kind, ref)
);

CREATE TABLE etl_incidents (
  job_id TEXT    NOT NULL,
  seq    INTEGER NOT NULL,
  code   TEXT    NOT NULL,
  at     TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (job_id, seq, code)
);

CREATE TABLE etl_resends (
  job_id   TEXT    NOT NULL,
  seq      INTEGER NOT NULL,
  requests INTEGER NOT NULL,
  PRIMARY KEY (job_id, seq)
);
```

Two things to see. The control tables hold **keys and codes, never the content of a row**: a rejected row is its key and a
reason. And the Worker checks `etl_meta` before it takes a batch, so a Worker never works on tables of a version it does
not know.

The tables of **this copy** are staging (where a batch lands as it came) and the final table. Staging has `job_id`, `seq`
and the `columns` of the description, with the same names; the final table is yours. `migrations/orders.sql`:

```sql
-- The two tables that belong to the copy of `orders`, in the destination: where a batch lands, and the final
-- table. Make them once, after control.sql:
--
--   sqlite3 warehouse.db < migrations/orders.sql

-- Staging: job_id and seq, then the columns of the event, with the same names. The key of the table is
-- part of the primary key. A batch lands here as it came (and as the Extractor masked it).
CREATE TABLE stg_orders (
  job_id   TEXT    NOT NULL,
  seq      INTEGER NOT NULL,
  id       INTEGER NOT NULL,
  customer TEXT,
  document TEXT,
  total    REAL,
  PRIMARY KEY (job_id, seq, id)
);

-- The final table, written by an upsert on its business key.
CREATE TABLE orders_final (
  id            INTEGER PRIMARY KEY,
  customer      TEXT    NOT NULL,
  document_tail TEXT    NOT NULL,
  total_cents   INTEGER NOT NULL,
  loaded_job    TEXT    NOT NULL
);
```

```sh
sqlite3 warehouse.db < migrations/control.sql
sqlite3 warehouse.db < migrations/orders.sql
```

### 4.2 The description and the settings of the Worker

The Worker may live on another machine, which never reaches the source. Its settings are in a file of its own,
`worker.toml`:

```toml
# The Worker: takes the batches from the broker, loads them into the destination and closes the job.

[runtime]
timeout_seconds = 60

[sql.dest]
driver = "sqlite"
path = "warehouse.db"
mode = "write"

# The same key and columns as in extractor.toml: they are what the two machines agree on.
[elt.orders]
key = "id"
columns = ["id", "customer", "document", "total"]

[elt.orders.destination]
connection = "dest"
staging = "stg_orders"            # where a batch lands, as it came
table = "orders_final"
upsert_on = "id"                  # a batch loaded twice gives the same table
set = { id = "id", customer = "trim(customer)", document_tail = "substr(document, -4)", total_cents = "CAST(round(total * 100) AS INTEGER)", loaded_job = "job_id" }
reject = [
  { when = "total < 0", code = "TOTAL_NEGATIVE" },
  { when = "trim(customer) = ''", code = "CUSTOMER_EMPTY" },
]
reject_share = 20                 # a batch with more rejected rows than this percent is stopped
pause_after = 3                   # and three stopped in a row pause the job

# Only `metagente consume --from main` reads it: the Worker gets its events from here and puts the ones it
# cannot do in the dead letters.
[broker.main]
driver = "jetstream"
url = "nats://127.0.0.1:4222"
stream = "ETL"
```

The top of `[elt.orders]` (`key` and `columns`) is what the two machines agree on, so it is written in both files. The rest
is what the Worker does, and it is **what the data means to you**:

- **`set`** says what each column of the final table is made of, as an expression over the columns of staging (and `job_id`).
  Here the document is cut to its last four digits and the total becomes cents. These are SQL, in the dialect of the
  destination.
- **`upsert_on`** is the column that tells a row from another. The rows are loaded with an upsert, so loading a batch twice
  gives the same table.
- **`reject`** is a list of rules. A row for which a `when` holds goes to `etl_rejects` with its **key and the code, never its
  content**, and the batch goes on.
- **`reject_share`** and **`pause_after`** are the brakes. A batch with more than that percent of rejected rows is
  stopped, because a rate that high is usually a change in the source and not a few bad rows; and that many batches stopped in a
  row pause the job (reason `QUALITY`).

Again, no statement is written. `land_batch` (the rows go to staging and the batch is marked, in one transaction),
`transform_batch` (the rejects, the upsert and the batch marked done, in one transaction), `try_close` (the one conditional
`UPDATE` that closes the job, so that two Workers that try at the same moment close it once) and the rest are made from the
description. For example, from the two rules and the `set` above, the statement that loads is:

```sql
INSERT INTO orders_final (customer, document_tail, id, loaded_job, total_cents)
SELECT trim(customer), substr(document, -4), id, job_id, CAST(round(total * 100) AS INTEGER)
FROM stg_orders
WHERE job_id = :job AND seq = :seq
  AND CASE WHEN (total < 0) OR (trim(customer) = '') THEN 1 ELSE 0 END = 0
ON CONFLICT (id) DO UPDATE SET customer = excluded.customer, document_tail = excluded.document_tail,
  loaded_job = excluded.loaded_job, total_cents = excluded.total_cents
```

The `[sql.dest]` section has `mode = "write"` because the Worker changes rows. The statements are made for SQLite, PostgreSQL,
MySQL, MariaDB, SQL Server and Oracle. For another database, the place of the database is `host`, `database` and `user`, the password goes in
`[credentials]` as `dest = "DEST_DB_PASSWORD"`, the tables are the ones of `migrations/control.<database>.sql` and
`migrations/orders.<database>.sql`, and the expressions are the ones of that database. Compare the line of `set` for
the document in each:

| Database | `document_tail` |
|---|---|
| SQLite, Oracle | `substr(document, -4)` |
| PostgreSQL, MySQL, MariaDB, SQL Server | `right(document, 4)` (written `RIGHT` in the others) |

MySQL and MariaDB (one set of tables, `migrations/control.mysql.sql` and `migrations/orders.mysql.sql`, serves both) have no
`ON CONFLICT`, but they have `INSERT ... ON DUPLICATE KEY UPDATE`, which does the same: the statement above is written with it,
and a row that is there already is updated to what the batch says. Where the database has neither (SQL Server and Oracle), the upsert is made as an `UPDATE` of the rows that are
there, followed by an `INSERT` of the ones that are not, as the sample does. You do not write either of them. One difference
comes from Oracle itself: an empty text is nothing, so the rule that rejects an empty customer is `customer IS NULL` there.

### 4.3 The Worker

Make `worker.ag`:

```text
agent Worker
  goal "Load the batches of the orders table and close the job"
  tool orders from elt "orders" load
```

The line gives the agent the tools `dest` and `codec`, and the messages:

| Message | What it does |
|---|---|
| `batch` | lands one batch and transforms it |
| `control` | registers the totals of a job and closes it if everything is done |
| `resume job` | lets a paused job go on |
| `retransform` | transforms again a batch that stayed stuck, from the rows in staging (the sweeper asks) |
| `purge days`, `purge_control days` | clean staging, and, much later, the control tables of the jobs that are done |

For each batch, the Worker **checks what it can read** (the version of the tables, the version of the event, the columns; a
final failure sends the event to the dead letters, because trying again would give the same answer); looks at the **state of
the batch** (`done`: it is a copy, so it only replies; `landed`: the rows are in staging already, so it goes straight to the
transform); **lands** it after it checks the hash (a wrong hash is a final failure, and nothing lands); applies the **brake**;
**transforms** it; tries to close the job; and replies. `reply` is what confirms the event.

### 4.4 Approve and run

```sh
metagente check worker.ag --config worker.toml
metagente trust worker.ag --from main --subject 'etl.*.batch' --dead etl.dead --config worker.toml
metagente trust worker.ag --from main --subject 'etl.*.control' --dead etl.dead --config worker.toml
```

`consume` runs an agent as a service. You start **one copy for each kind of event** (and as many copies of each as you want:
copies of the same one share the work). In two terminals:

```sh
metagente consume worker.ag --from main --subject 'etl.*.batch'   --dead etl.dead --message batch   --in-flight 2 --config worker.toml
metagente consume worker.ag --from main --subject 'etl.*.control' --dead etl.dead --message control --config worker.toml
```

- `--from main` is the broker of the settings, and `--subject` says which events this copy takes.
- `--message batch` is the message of the agent that gets each event.
- `--dead etl.dead` is where an event goes when nobody can do it (a final failure, or too many tries). It holds the same data
  as the event, so give it the same care and a retention.
- `--in-flight 2` is how many batches this copy works on at once.

The copies wait for events and keep running; stop them with Ctrl-C. They already have the three batches and the
control event that you published, so they print what they do and the job closes.

### 4.5 See the books close

```sh
sqlite3 warehouse.db "SELECT job_id, state, total_batches, total_rows FROM etl_jobs"
# demo|done|3|2500
sqlite3 warehouse.db "SELECT seq, state, rows_read, rows_loaded, rows_rejected FROM etl_batches ORDER BY seq"
# 1|done|1000|998|2
# 2|done|1000|998|2
# 3|done|500|499|1
sqlite3 warehouse.db "SELECT count(*) FROM orders_final"
# 2495
sqlite3 warehouse.db "SELECT source_key, reason_code FROM etl_rejects ORDER BY source_key"
# 500|TOTAL_NEGATIVE
# 1000|TOTAL_NEGATIVE
# 1500|TOTAL_NEGATIVE
# 2000|TOTAL_NEGATIVE
# 2500|TOTAL_NEGATIVE
sqlite3 warehouse.db "SELECT id, customer, document_tail, total_cents, loaded_job FROM orders_final WHERE id = 1"
# 1|Customer 1|7919|1150|demo
```

2,500 rows read = 2,495 loaded + 5 rejected, and the job is `done`. The document in the final table is only its last four
digits, because that is all that ever left the source.

## 5. Break it on purpose

The point of the design is that a failure costs a retry and not a rebuild. Try these.

**The Worker is not there.** Stop the `batch` copy with Ctrl-C, and run the Extractor for a new job:

```sh
metagente run extractor.ag extract job=second --config extractor.toml
```

The events wait in the broker. Start the copy again and it takes them: nothing was lost, and nothing waits for the Worker
to be up. (The job `second` copies the same table again; the final table has the same rows, because the load is an upsert.)

**The Worker stops in the middle of a batch.** It is hard to catch on a small table, so think it through. If it stops
before the first transaction ends, nothing was written, and the event comes again. If it stops between the two, the batch
is `landed` in the control tables and its rows are in staging: the event comes again, the Worker sees `landed`, does not even
read the payload, and goes straight to the second transaction. If it stops after the second, the batch is `done`, and the
event that comes again is only confirmed. In none of them is a row loaded twice.

**A copy of an event.** The broker drops an event that has an id it remembers. One that gets past it (a new id, the same
batch) finds the batch `done`, and the Worker replies `batch 1 of demo was already done`.

**Too many bad rows.** Make a third of the first page negative, with `sqlite3 source.db "UPDATE orders SET total = -1 WHERE
id <= 400"`, and copy the table as a job with a new name. The first batch has 40% of its rows rejected, more than the 20%
that `reject_share` allows: the Worker marks it `failed` with the code `TOO_MANY_REJECTS`, sends the event to the dead
letters, and does not write a single reject or a single row of it. The other batches go on, and the job stays `running`,
waiting for a person. Three batches in a row stopped this way pause the job (`paused`, `QUALITY`) and the next ones wait; when
the cause is solved, `metagente run worker.ag resume job=NAME` lets it go on.

**The broker is down.** Stop it (`docker stop nats`) and run the Extractor for a new job. It fails with a message that says it
may pass (a failure that may pass is one that a retry can fix: a broker that is down, a stream that is full). Look at what
the outbox kept:

```sh
sqlite3 outbox.db "SELECT seq, after_key, upto_key, state FROM outbox WHERE job_id = 'third'"
```

A batch that was planned and not confirmed is `planned`. Start the broker (`docker start nats`) and run the same line again: it
sends first that batch, built again from the same edges, and then goes on. A loop of your own can do the waiting:

```sh
until metagente run extractor.ag extract job=third --config extractor.toml; do sleep 30; done
```

**The destination is down.** A database error that may pass becomes a retry: the event is asked for again after 10 seconds,
1, 5 and 15 minutes. If three in a row fail this way, the circuit breaker of `consume` stops taking batches for a while, so
a broken destination is not hit by every event.

## 6. Changing it to your table

You change the description, and the agents stay as they are.

| You change | Where |
|---|---|
| The table, its columns and key | `table`, `columns` and `key` in `[elt.orders]`; the staging and final tables, and `set` |
| What must not leave the source | `mask` (the last N characters of a column), and `select` for a column that needs to be read in a certain way: `select = { total = "CAST(total AS double precision)" }` for a `numeric` of PostgreSQL |
| The size of a batch | `rows` (the most rows of a page) and `bytes` (a page is halved until its rows, written as JSON, fit) in `source` |
| The rules of the transformation | `reject` and `set` in `destination`; the Extractor does not change |
| The brakes | `reject_share` and `pause_after` |
| The source database | `[sql.source]`: PostgreSQL, MySQL, MariaDB, SQL Server and Oracle work, with `select` for the casts that each one needs ([`samples/async-elt/sources/`](../samples/async-elt/sources/) has examples that are tried against the real databases) |
| The destination database | `[sql.dest]`: SQLite, PostgreSQL, MySQL, MariaDB, SQL Server and Oracle (with the expressions of each) |

The key must be a whole number that grows. If your table has another key, read it with the name of the key:
`select = { id = "order_no" }` and `key = "id"`. A column that is called `job`, `seq`, `rows` or `version` (names that the
statements already use) is read under another name the same way.

A problem in the description is told with the line of the section, in words: a mask of a column that is not in `columns`, a
rule with a code that is not in capitals, a `set` that leaves out the `upsert_on`, a rule that is not a single
statement.

## 7. When to write the agent by hand

The description covers the copy of a table with rules that can be said in a line of SQL. Write the agents step by step, as
[`samples/async-elt`](../samples/async-elt/README.md) does, when you need:

- **a step with a language model** between landing and transforming, with a budget (the sample has it);
- **another kind of work** that the Worker does for each batch.

An agent written by hand and one made from a description speak the same events and use the same control tables, so they
work together: a hand-written Worker can take the batches of an Extractor made from a description, and the other way around.
The **sweeper**, which heals a job that lost an event and tells the team what needs a person, is in the sample too.

Before a job with real data, read section 10 of [the design](design-async-elt.md), on personal data, and measure with
[the pilot](../samples/async-elt/README.md#the-pilot).
