# Tutorial: an Extractor and a Worker for the asynchronous ELT

In this tutorial you build, from an empty folder, the two agents of the asynchronous ELT: an **Extractor**, that reads
a table of a source database and sends it in batches, and a **Worker**, that receives the batches, loads them into
another database and checks that nothing was lost. When you finish, you will have copied 2,500 orders from one SQLite
file to another, through a broker, and seen the books close.

You should have done [your first agent](tutorial.md) first. The reasons behind each choice are in
[the design](design-async-elt.md); a bigger and complete version of what you build here, with a brake for bad data, a step
with a language model, a sweeper that heals a job, and PostgreSQL, SQL Server and Oracle, is the
[sample](../samples/async-elt/README.md).

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
- `sqlite3`, to make the two demo databases (any SQLite tool will do).
- A NATS server with JetStream, and a stream called `ETL` that takes `etl.>`. With Docker and the
  [`nats` command](https://github.com/nats-io/natscli):

  ```sh
  docker run -d --name nats -p 4222:4222 nats:2.10-alpine -js
  nats stream add ETL --subjects 'etl.>' --discard new --max-msgs 100000 --storage file --defaults
  ```

  `--discard new` makes a full stream refuse new events instead of throwing away old ones nobody has processed. A real
  stream also has a maximum age and size: see [the design](design-async-elt.md#14-sizing-without-numbers).

Make a folder and go into it. Everything below happens there.

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

`migrations/outbox.sql`:

```sql
-- The outbox of the Extractor: the edges of every batch, saved before the batch is sent.
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
  PRIMARY KEY (job_id, seq)
);
```

```sh
sqlite3 outbox.db < migrations/outbox.sql
```

### 3.3 The settings of the Extractor

The agent file names no database, no broker and no secret. Everything that depends on the place is in a settings file,
so the same agent runs in a test and in production. Make `extractor.toml`:

```toml
[runtime]
timeout_seconds = 60
max_loop_turns = 1000000          # one turn for each page, and a big table has many pages

[sql.source]
driver = "sqlite"
path = "source.db"                # no `mode`, so it can only be read

[sql.source.statements]
page = "SELECT id, customer, '***' || substr(document, -4) AS document, total FROM orders WHERE id > :after ORDER BY id LIMIT :size"
range = "SELECT id, customer, '***' || substr(document, -4) AS document, total FROM orders WHERE id > :after AND id <= :upto ORDER BY id"

[sql.outbox]
driver = "sqlite"
path = "outbox.db"
mode = "write"

[sql.outbox.statements]
version = { sql = "SELECT schema_version FROM outbox_meta", result = "value" }
int = { sql = "SELECT CAST(:n AS INTEGER)", result = "value" }
next_seq = { sql = "SELECT coalesce(max(seq), 0) + 1 FROM outbox WHERE job_id = :job", result = "value" }
last_upto = { sql = "SELECT coalesce(max(upto_key), 0) FROM outbox WHERE job_id = :job", result = "value" }
next_planned = { sql = "SELECT seq, after_key, upto_key FROM outbox WHERE job_id = :job AND state = 'planned' ORDER BY seq LIMIT 1", result = "row" }
plan = "INSERT INTO outbox (job_id, seq, after_key, upto_key, row_count, state) VALUES (:job, :seq, :after_key, :upto_key, :row_count, 'planned')"
confirm = "UPDATE outbox SET state = 'published' WHERE job_id = :job AND seq = :seq AND state = 'planned'"
totals = { sql = "SELECT count(*) AS batches, coalesce(sum(row_count), 0) AS rows FROM outbox WHERE job_id = :job", result = "row" }

[broker.main]
driver = "jetstream"
url = "nats://127.0.0.1:4222"
stream = "ETL"
```

Read it in four parts:

- **`[sql.source]`** is the table you copy. It has no `mode`, so Metagente opens it **read only**: an agent that
  reads a source cannot change it. The two statements are the only SQL the Extractor can run on it. `page` is a page by
  key; `range` is the *same rows again*, by the edges saved in the outbox, which is how a batch is built again after a
  restart. Both give the columns in the same order.
- **Masking happens in the statement.** `'***' || substr(document, -4) AS document` keeps only the last four digits.
  What the `SELECT` does not give never leaves the source, so personal data is cut here and not later.
- **`[sql.outbox]`** is a database the Extractor writes (`mode = "write"`). Its statements are small and named; `int`
  turns a value that came from the command line (a text) into a number.
- **`[broker.main]`** is the connection to JetStream, and `stream` is the stream you made. For a broker with a user and a
  password, add `user = "extractor"` and, in `[credentials]`, `events = "BROKER_PASSWORD"`: the file says the *name* of the
  variable, never the password.

### 3.4 The Extractor

Make `extractor.ag`:

```text
agent Extractor
  goal "Copy the orders table into batch events on the broker, page by page, and start again where it stopped"
  tool source from sql "source"
  tool outbox from sql "outbox"
  tool events from broker "main" publish "etl.*.batch" "etl.*.control"
  tool codec

  accepts extract job size  # copy the table, and say how many batches and rows went out
  on extract
    version = outbox.version
    if version is not 1
      fail "the outbox has version {version}, and this Extractor works with version 1"
    page_size = outbox.int n: size
    more = yes
    repeat while more
      todo = outbox.next_planned job: job
      if todo is nothing
        # Nothing waits: plan the next batch.
        seq = outbox.next_seq job: job
        after = outbox.last_upto job: job
        rows = source.page after: after size: page_size
        upto = nothing
        for row in rows
          upto = row.id
        if upto is nothing
          more = no
        otherwise
          planned = codec.count value: rows
          saved = outbox.plan job: job seq: seq after_key: after upto_key: upto row_count: planned
        batch_seq = seq
        batch_rows = rows
      otherwise
        # A batch was planned and never confirmed: build it again from the same edges.
        batch_seq = todo.seq
        upto = todo.upto_key
        batch_rows = source.range after: todo.after_key upto: upto
      if upto is not nothing
        count = codec.count value: batch_rows
        table = codec.table rows: batch_rows columns: ["id", "customer", "document", "total"]
        body = codec.json value: table
        hash = codec.sha256 text: body
        payload = codec.gzip text: body
        event = codec.record v: 1 job_id: job seq: batch_seq columns: ["id", "customer", "document", "total"] row_count: count payload: payload sha256: hash
        events.publish subject: "etl.{job}.batch" id: "{job}:{batch_seq}" data: event
        confirmed = outbox.confirm job: job seq: batch_seq
    totals = outbox.totals job: job
    control = codec.record v: 1 job_id: job batches: totals.batches rows: totals.rows
    events.publish subject: "etl.{job}.control" id: "{job}:control" data: control
    reply "job {job}: {totals.batches} batches, {totals.rows} rows"
```

Read it from the top:

- `tool source from sql "source"` gives the agent the statements of `[sql.source]` as if they were commands:
  `source.page`, `source.range`. The same for `outbox`. `tool events from broker "main" publish …` lets it publish **only**
  on the two subjects named; it cannot read the broker or write anywhere else.
- `tool codec` has no setting: it counts rows, writes them as JSON, makes the SHA-256, packs them with gzip and builds
  the record of the event.
- The loop `repeat while more` makes one batch for each turn. When there is no batch waiting (`todo is nothing`) it
  plans the next one: the key where the last one ended is `after`, a page of `size` rows is read, and the key of the last row
  is `upto`. **The edges are saved in the outbox before anything is sent** (`outbox.plan`). A page with no rows ends the loop.
- Otherwise a batch was planned and never confirmed (the machine stopped, the broker was down): the rows are read again with
  `source.range`, from the same edges.
- `payload` is the rows as JSON, packed with gzip; `sha256` is the hash of the rows *before* they were packed, so the Worker
  can check them after unpacking. The event `id` is `<job>:<seq>`: if the same batch is published twice, the broker keeps one.
- `outbox.confirm` marks the batch as published only after the broker took it. At the end, the **control event** says how
  many batches and rows were planned.

### 3.5 Check, approve and run

```sh
metagente check extractor.ag
# No problems found in extractor.ag (1 agent).
metagente trust extractor.ag --config extractor.toml
```

`trust` lists what the agent will do: it connects to the broker and may publish on two subjects, it **reads** `source.db`
(with the statements `page` and `range`) and it **changes** `outbox.db`. You approve it once for the project. If what the agent can
reach changes, `metagente` asks again.

```sh
metagente run extractor.ag extract job=demo size=1000 --config extractor.toml
# job demo: 3 batches, 2500 rows
```

Three batches went to the broker: 1,000, 1,000 and 500 rows. Run the same line again: it finds everything done, plans no
new batch, and publishes the control event once more with the same id (the broker drops it while it still remembers the id). **Run it with the same job
name to go on after a stop; use a new name for a new copy.**

## 4. The destination and the Worker

### 4.1 The destination database

`migrations/destination.sql` has the control tables (what the Worker knows about each job and each batch), the staging
table (where a batch lands as it came) and the final table.

```sql
-- The destination of the Worker: control tables, staging and the final table.
CREATE TABLE etl_meta (
  schema_version INTEGER NOT NULL
);
INSERT INTO etl_meta (schema_version) VALUES (1);

-- A job is `running` until every batch it announced is done; then `done`, or `mismatch` if the rows do not add up.
CREATE TABLE etl_jobs (
  job_id        TEXT PRIMARY KEY,
  state         TEXT NOT NULL DEFAULT 'running' CHECK (state IN ('running', 'done', 'mismatch')),
  total_batches INTEGER,
  total_rows    INTEGER
);

-- The state of a batch changes in the same transaction as its data: landed, then done.
CREATE TABLE etl_batches (
  job_id        TEXT    NOT NULL,
  seq           INTEGER NOT NULL,
  state         TEXT    NOT NULL CHECK (state IN ('landed', 'done')),
  rows_read     INTEGER NOT NULL,
  rows_loaded   INTEGER,
  rows_rejected INTEGER,
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

-- Where a batch lands, as it came.
CREATE TABLE stg_orders (
  job_id     TEXT    NOT NULL,
  seq        INTEGER NOT NULL,
  source_key INTEGER NOT NULL,
  customer   TEXT,
  document   TEXT,
  total      REAL,
  PRIMARY KEY (job_id, seq, source_key)
);

-- The final table, written by an upsert on the business key.
CREATE TABLE orders_final (
  id            INTEGER PRIMARY KEY,
  customer      TEXT    NOT NULL,
  document_tail TEXT    NOT NULL,
  total_cents   INTEGER NOT NULL,
  loaded_job    TEXT    NOT NULL
);
```

```sh
sqlite3 warehouse.db < migrations/destination.sql
```

Two things to see. The control tables hold **keys and codes, never the content of a row**: a rejected row is its key and a
reason (`TOTAL_NEGATIVE`). And the Worker checks `etl_meta` before it takes a batch, so a Worker never works on tables of a
version it does not know.

### 4.2 The settings of the Worker

The Worker may live on another machine, which never reaches the source. Its settings are in a file of its own,
`worker.toml`:

```toml
[runtime]
timeout_seconds = 60

[sql.dest]
driver = "sqlite"
path = "warehouse.db"
mode = "write"

[sql.dest.statements]
version = { sql = "SELECT schema_version FROM etl_meta", result = "value" }
batch_state = { sql = "SELECT state FROM etl_batches WHERE job_id = :job AND seq = :seq", result = "value" }
batch_counts = { sql = "SELECT rows_loaded, rows_rejected FROM etl_batches WHERE job_id = :job AND seq = :seq", result = "row" }
job_state = { sql = "SELECT state FROM etl_jobs WHERE job_id = :job", result = "value" }

# Transaction 1, "land": the rows go to staging and the batch is marked, all or nothing.
open_job = "INSERT INTO etl_jobs (job_id) VALUES (:job) ON CONFLICT (job_id) DO NOTHING"
land = { sql = "INSERT INTO stg_orders (job_id, seq, source_key, customer, document, total) VALUES (:job, :seq, :id, :customer, :document, :total) ON CONFLICT (job_id, seq, source_key) DO NOTHING", each = "rows", columns = ["id", "customer", "document", "total"] }
mark_landed = "INSERT INTO etl_batches (job_id, seq, state, rows_read) VALUES (:job, :seq, 'landed', :row_count) ON CONFLICT (job_id, seq) DO NOTHING"

# Transaction 2, "transform": the rejects, the upsert into the final table, and the batch marked done, all or nothing.
reject_negative = "INSERT INTO etl_rejects (job_id, seq, source_key, reason_code) SELECT job_id, seq, source_key, 'TOTAL_NEGATIVE' FROM stg_orders WHERE job_id = :job AND seq = :seq AND total < 0 ON CONFLICT DO NOTHING"
load_orders = "INSERT INTO orders_final (id, customer, document_tail, total_cents, loaded_job) SELECT source_key, trim(customer), substr(document, -4), CAST(round(total * 100) AS INTEGER), job_id FROM stg_orders WHERE job_id = :job AND seq = :seq AND total >= 0 ON CONFLICT (id) DO UPDATE SET customer = excluded.customer, document_tail = excluded.document_tail, total_cents = excluded.total_cents, loaded_job = excluded.loaded_job"
finish_batch = "UPDATE etl_batches SET state = 'done', rows_rejected = (SELECT count(DISTINCT source_key) FROM etl_rejects WHERE job_id = :job AND seq = :seq), rows_loaded = rows_read - (SELECT count(DISTINCT source_key) FROM etl_rejects WHERE job_id = :job AND seq = :seq) WHERE job_id = :job AND seq = :seq"

# The end of a job: the totals the Extractor announced, and the one UPDATE that closes it.
set_totals = "UPDATE etl_jobs SET total_batches = :batches, total_rows = :rows WHERE job_id = :job"
try_close = "UPDATE etl_jobs SET state = CASE WHEN (SELECT coalesce(sum(rows_loaded + rows_rejected), 0) FROM etl_batches WHERE job_id = :job AND state = 'done') = total_rows THEN 'done' ELSE 'mismatch' END WHERE job_id = :job AND state = 'running' AND total_batches IS NOT NULL AND (SELECT count(*) FROM etl_batches WHERE job_id = :job AND state = 'done') = total_batches"

[sql.dest.transactions]
land_batch = ["open_job", "land", "mark_landed"]
transform_batch = ["reject_negative", "load_orders", "finish_batch"]
register_totals = ["open_job", "set_totals"]

[broker.main]
driver = "jetstream"
url = "nats://127.0.0.1:4222"
stream = "ETL"
```

What matters here:

- **Every statement is named.** The agent says `dest.land_batch …`; it cannot send any other SQL.
- **`land`** has `each = "rows"`: the statement is run once for each row of the list the agent passes, with the columns named in
  `columns`. `ON CONFLICT … DO NOTHING` makes a row that is there already harmless, so a copy of a batch changes nothing.
- **`[sql.dest.transactions]`** groups statements: `land_batch` runs `open_job`, `land` and `mark_landed` in **one**
  transaction, all or nothing. That is what makes "the data and the state of the batch" change together.
- **`load_orders` is an upsert** on the business key (`id`): loading a batch twice gives the same table. It also does the
  transformation: the document is cut to its last four digits and the total is turned into cents.
- **`reject_negative`** is a rule: a row with a negative total goes to `etl_rejects` with a code, and the batch goes on.
  The rule has two sides, because `load_orders` loads only `total >= 0`.
- **`try_close`** is one conditional `UPDATE`. If two Workers try to close the job at the same moment, only one changes the row.
  It closes as `done` when the rows add up and as `mismatch` when they do not.

### 4.3 The Worker

Make `worker.ag`:

```text
agent Worker
  goal "Land the batches of an ETL job in the destination, transform them, and close the job"
  tool dest from sql "dest"
  tool codec

  accepts batch v job_id seq columns row_count payload sha256  # land one batch and transform it
  accepts control v job_id batches rows  # register the totals of a job, and close it if everything is done

  on batch
    version = dest.version
    if version is not 1
      fail "the destination has version {version} of the control tables, and this Worker works with version 1"
    if v is not 1
      fail "the event has version {v}, and this Worker reads version 1"
    if columns is not ["id", "customer", "document", "total"]
      fail "the columns of the event are not the ones this Worker lands"
    state = dest.batch_state job: job_id seq: seq
    if state is "done"
      reply "batch {seq} of {job_id} was already done"
    if state is nothing
      # Not landed yet: check the event, and land it (transaction 1).
      body = codec.gunzip text: payload
      check = codec.sha256 text: body
      if check is not sha256
        fail "the hash of batch {seq} of {job_id} does not match: the event is damaged"
      rows = codec.parse text: body
      landed = dest.land_batch job: job_id seq: seq row_count: row_count rows: rows
    # Landed (now, or by an earlier copy of this event): transaction 2.
    transformed = dest.transform_batch job: job_id seq: seq
    closed = dest.try_close job: job_id
    counts = dest.batch_counts job: job_id seq: seq
    reply "batch {seq} of {job_id}: {counts.rows_loaded} loaded, {counts.rows_rejected} rejected"

  on control
    registered = dest.register_totals job: job_id batches: batches rows: rows
    closed = dest.try_close job: job_id
    state = dest.job_state job: job_id
    reply "job {job_id} is {state}"
```

Each `accepts` is one kind of event, and its words are the fields of the event. The agent and the Extractor have to agree on
the record: compare `accepts batch …` with the `codec.record` of the Extractor.

For each batch, the Worker:

1. **Checks what it can read**: the version of the tables, the version of the event, and the columns. A `fail` with no
   `retry` is a final failure: the event goes to the dead letters, because trying again would give the same answer.
2. **Looks at the state of the batch.** `done`: it is a copy, so it only replies. Nothing there: it is new. `landed`: the
   rows are in staging already, so it goes straight to the transform.
3. **Lands the batch** (transaction 1) after it unpacks the payload and checks the hash. A wrong hash is a final failure, and
   nothing is landed.
4. **Transforms the batch** (transaction 2), tries to close the job, and replies with the counts. `reply` is what
   confirms the event.

For the control event, the Worker saves the totals and tries to close the job. Whichever comes last, the last batch or the
control event, closes it.

### 4.4 Approve and run

```sh
metagente check worker.ag
metagente trust worker.ag --from main --subject 'etl.*.batch' --dead etl.dead --config worker.toml
metagente trust worker.ag --from main --subject 'etl.*.control' --dead etl.dead --config worker.toml
```

`consume` runs an agent as a service. You start **one copy for each kind of event** (and as many copies of each as you want:
copies of the same one share the work). In two terminals:

```sh
metagente consume worker.ag --from main --subject 'etl.*.batch'   --dead etl.dead --message batch   --in-flight 2 --config worker.toml
metagente consume worker.ag --from main --subject 'etl.*.control' --dead etl.dead --message control --config worker.toml
```

- `--from main` is the connection of the settings, and `--subject` says which events this copy takes.
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
sqlite3 warehouse.db "SELECT id, customer, document_tail, total_cents FROM orders_final WHERE id = 1"
# 1|Customer 1|7919|1150
```

2,500 rows read = 2,495 loaded + 5 rejected, and the job is `done`. The document in the final table is only its last four
digits, because that is all that ever left the source.

## 5. Break it on purpose

The point of the design is that a failure costs a retry and not a rebuild. Try these.

**The Worker is not there.** Stop the `batch` copy with Ctrl-C, and run the Extractor for a new job:

```sh
metagente run extractor.ag extract job=second size=1000 --config extractor.toml
```

The events wait in the broker. Start the copy again and it takes them: nothing was lost, and nothing waits for the Worker
to be up. (The job `second` copies the same table again; the final table has the same rows, because `load_orders` is an
upsert.)

**The Worker stops in the middle of a batch.** It is hard to catch on a small table, so think it through. If it stops
before the first transaction ends, nothing was written, and the event comes again. If it stops between the two, the batch
is `landed` in the control tables and its rows are in staging: the event comes again, the Worker sees `landed`, does not even
read the payload, and goes straight to the second transaction. If it stops after the second, the batch is `done`, and the
event that comes again is only confirmed. In none of them is a row loaded twice.

**A copy of an event.** The broker drops an event that has an id it remembers. One that gets past it (a new id, the same
batch) finds the batch `done`, and the Worker replies `batch 1 of demo was already done`.

**The broker is down.** Stop it (`docker stop nats`) and run the Extractor for a new job. It fails with a message that says it
may pass (a failure that may pass is one that a retry can fix: a broker that is down, a stream that is full). Look at what
the outbox kept:

```sh
sqlite3 outbox.db "SELECT seq, after_key, upto_key, state FROM outbox WHERE job_id = 'third'"
```

A batch that was planned and not confirmed is `planned`. Start the broker (`docker start nats`) and run the same line again: it
sends first that batch, built again from the same edges, and then goes on. A loop of your own can do the waiting:

```sh
until metagente run extractor.ag extract job=third size=1000 --config extractor.toml; do sleep 30; done
```

**The destination is down.** The Worker's `fail` with a database error that may pass becomes a retry: the event is asked for
again after 10 seconds, 1, 5 and 15 minutes. If three in a row fail this way, the circuit breaker of `consume` stops taking
batches for a while, so a broken destination is not hit by every event.

## 6. Changing it to your table

| You change | Where |
|---|---|
| The table, its columns and key | the two statements `page` and `range`, the `columns` lists in both agents, the staging and final tables, and `land`, `load_orders` |
| The size of a batch | `size` is the most rows of a page. The sample also has `bytes`: it halves a page until the rows, written as JSON, fit |
| The rules of the transformation | the statements that reject and load, in the settings; the agent does not change |
| The source database | `[sql.source]`: PostgreSQL, MySQL, MariaDB, SQL Server and Oracle are in [`samples/async-elt/sources/`](../samples/async-elt/sources/), with `page` and `range` in the dialect of each; the password goes in `[credentials]` as `source = "SOURCE_DB_PASSWORD"` |
| The destination database | `[sql.dest]`: the same statements for PostgreSQL, SQL Server and Oracle are in the [sample](../samples/async-elt/) |

The key must be a whole number that grows, called `id`, in this version of the Extractor. If your table has another key, rename it
in the `SELECT` (`SELECT order_no AS id, …`).

## 7. What this version leaves out

The agents above are the smallest that work. The [sample](../samples/async-elt/README.md) adds, in the same shape:

- **A brake for bad data.** If more than 20% of a batch would be rejected, the batch is stopped (`TOO_MANY_REJECTS`); three in a row
  pause the job, because a rate that high is usually a change in the source.
- **A size in bytes** for each batch, and a `resend` message so that the Extractor can send again, from its outbox, a batch the
  broker lost.
- **The sweeper**, an agent you run every few minutes: it tells the team (with a code and a place, never a row), asks the Worker
  to transform again a batch that stayed stuck, and asks the Extractor for a batch that never arrived.
- **A step with a language model** between landing and transforming, with a budget, and **purges** of staging and of the control
  tables.
- **Personal data**: who may read what, TLS and users of the broker, and retention. Read section 10 of
  [the design](design-async-elt.md) before a job with real data.
- **Numbers to start with** for the workers, the time to confirm a batch and the circuit breaker, measured by the
  [pilot](../samples/async-elt/README.md#the-pilot).
