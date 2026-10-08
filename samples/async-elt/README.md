# Asynchronous ELT: the Extractor and the Worker

Copy a large table from a **source** database to a **destination** database, in batches, with agents on
different machines, so that a batch that fails can be tried again, also much later. The design, the failures
it covers and the decisions are in [`docs/design-async-elt.md`](../../docs/design-async-elt.md).

```text
 ZONE OF THE SOURCE                 BROKER (JetStream)                 ZONE OF THE DESTINATION
┌────────────────────────┐      ┌───────────────────────┐       ┌──────────────────────────────┐
│ Extractor              │ ───► │ stream etl.>          │ ───►  │ Worker                       │
│  reads pages by key    │ pub  │  etl.<job>.batch      │ pull  │  lands, transforms, confirms │
│  outbox (SQLite)       │      │  etl.<job>.control    │       │                              │
└──────────┬─────────────┘      └───────────────────────┘       └───────────────┬──────────────┘
       source DB                                                          destination DB
```

This folder has **the Extractor** (`extractor.ag`) and **the Worker** (`worker.ag`), their configuration
(`metagente.toml`, and `metagente.postgres.toml` for a Worker with a PostgreSQL destination), and the
migrations of the databases. The optional step with a language model, its budget and the brake of a whole
job come in the next part; the sweeper that heals a job after that.

## What the Extractor does

1. Reads the next page of the table **by key** (`WHERE id > :after ORDER BY id LIMIT :size`), through a
   named statement of the source. The statement masks what must not travel in clear (here, the document
   number), so it never leaves the source.
2. If the rows, written as JSON, are bigger than the size it aims at (`bytes`), it **halves the page** until
   they fit.
3. Saves the edges of the batch, `(job, seq, after, upto)`, in the **outbox** before anything is sent. The
   same batch can then be built again exactly the same.
4. Packs the rows with `tool codec` (a list of lists of values with the names of the columns said once, gzip,
   the SHA-256 of the rows before they were packed) and publishes the event on `etl.<job>.batch` with the
   id `<job>:<seq>`, so that the broker drops a copy.
5. Marks the batch as published in the outbox and goes on. When the table ends, it publishes a **control**
   event with the totals on `etl.<job>.control`.

If it stops for any reason (the broker is full or down, the machine restarts), **run it again with the same
job**: it first sends what the outbox says was planned and not confirmed, with the edges it was planned
with, and then goes on from the last key. Nothing is sent twice that the broker would not drop, and nothing
is skipped. A failure that may pass (a broker that is down, a stream that is full) ends the run with a note
that it may pass; a loop of your own does the waiting:

```sh
until metagente run extractor.ag extract job=orders-2026-10 size=2000 bytes=262144; do sleep 30; done
```

## The batch event

```json
{ "v": 1, "event_id": "019271c4-…", "job_id": "orders-2026-10", "seq": 4, "table": "orders", "key": "id",
  "after": 6000, "upto": 8000, "read_at": "2026-10-08T03:00:00Z",
  "columns": ["id", "customer", "document", "total"], "row_count": 2000,
  "encoding": "json+gzip", "payload": "H4sIAAAA…", "sha256": "…" }
```

`payload` is the rows, as a JSON list of lists, packed with gzip and written in base64. `sha256` is the hash
of the rows **before** they were packed, so the Worker checks it after unpacking. The key that protects
against a copy is `(job_id, seq)`.

## Try it

1. **Metagente**, and a NATS server with JetStream and a stream called `ETL` that takes `etl.>`, with the
   limits you want (it should discard new messages when it is full, so nothing unprocessed is thrown away):

   ```sh
   docker run -d --name nats -p 4222:4222 nats:2.10-alpine -js
   nats stream add ETL --subjects 'etl.>' --discard new --max-msgs 100000 --storage memory --defaults   # the nats CLI
   ```

2. **The two databases**, with the migrations of this folder (Metagente makes no tables):

   ```sh
   mkdir -p demo state
   sqlite3 demo/source.db < migrations/demo-source.sql     # 2500 demo orders
   sqlite3 state/outbox.db < migrations/outbox.sql
   ```

3. **Approve and run**, from this folder (Metagente finds `metagente.toml` where you start it):

   ```sh
   metagente trust extractor.ag
   metagente run extractor.ag extract job=demo size=1000 bytes=262144
   # job demo: 3 batches, 2500 rows
   ```

   `trust` shows both databases (the outbox as one that is **changed**), the broker and the subjects it may
   publish to. Run the last command again: it finds everything done and sends nothing new.

## The Worker

Run it with `metagente consume`, one copy for each kind of event (and as many copies of each as you want:
they share the work by the name of the consumer):

```sh
metagente trust worker.ag --from main --subject 'etl.*.batch'   --dead etl.dead
metagente trust worker.ag --from main --subject 'etl.*.control' --dead etl.dead
metagente consume worker.ag --from main --subject 'etl.*.batch'   --dead etl.dead --message batch   --in-flight 2
metagente consume worker.ag --from main --subject 'etl.*.control' --dead etl.dead --message control
metagente run worker.ag purge days=7        # from time to time, from cron or a timer
```

For **each batch** (`on batch`) it does this, and the state of the batch is changed in the same transaction as
its data:

1. Checks that the control tables have the version it knows, and that the event is version 1, `json+gzip`,
   with the columns it lands. Otherwise the event is a final failure: a dead letter.
2. Looks at `etl_batches`. **`done`**: the event is a copy, and it is only confirmed. **`landed` or `failed`**:
   the data is in staging already, so it goes straight to step 5 and does not even read the payload.
3. Not there yet: unpacks the payload, checks the **SHA-256** (a wrong hash is a final failure and nothing
   lands), and parses the rows.
4. **Transaction 1, `land_batch`:** opens the job if it is new, puts the rows in the staging table and marks the
   batch `landed`. All or nothing; a row that is there already is harmless.
5. **The brake:** if more than 20 percent of the batch would be rejected, the batch is marked `failed` with the
   code `TOO_MANY_REJECTS` and ends in a dead letter. A high rate is usually a change in the source, not a few
   bad rows, and it should not be loaded in silence.
6. **Transaction 2, `transform_batch`:** the rows that break a rule go to `etl_rejects` with their **key and a
   code, never the content** (`TOTAL_NEGATIVE`, `CUSTOMER_EMPTY`); the others are written to the final table
   by an **upsert** on the business key; the batch is marked `done` with its counts. All or nothing.
7. Tries to close the job (below), and replies. The event is confirmed.

For the **control event** (`on control`) it registers the totals the Extractor announced and tries to close
the job. **A job closes** with one conditional `UPDATE`, so that two Workers that try at once close it once: it
becomes `done` when every announced batch is done and the rows read are the rows loaded plus the rows
rejected, and `mismatch` when the batches are done and the rows do not add up. Whichever comes last, the
last batch or the control event, closes it.

What happens when something goes wrong, with the commands above:

| What fails | What happens |
|---|---|
| The destination is down or deadlocked | The failure may pass: the event is asked for again later (10 s, 1, 5, 15 min), and the circuit breaker of `consume` stops taking batches while it lasts |
| The Worker stops after transaction 1 | The batch stays `landed`; the event comes again and goes straight to transaction 2 |
| A copy of an event | The batch is `done`: only confirmed |
| A damaged event (wrong hash) | A dead letter; nothing lands |
| Too many rejected rows | The batch is `failed`, a dead letter; the rows stay in staging |
| A bug in the SQL | The batches fail and become dead letters. Fix the statements and send the dead letters again (see below) |

**Sending a dead letter again:** its data is the event, with the reason in the header `Metagente-Dead-Reason` (a
reason, never content). Publish the data again with a new id to the same subject (`nats pub` or an agent with
`tool broker`); a batch that is `failed` or `landed` is transformed again from staging, with no need for the
source. A fix of the data in staging, or of the rules in `metagente.toml`, comes first.

**The staging table** holds the rows as the Extractor masked them, so `purge` deletes the batches that were
done more than N days ago. Keep it short (the design proposes 7 days after the end of the job).

**A PostgreSQL destination:** make the tables with `migrations/destination.postgres.sql`, put the password in
the variable `DEST_DB_PASSWORD`, and run the commands above with `--config metagente.postgres.toml`. The file
has the same statements in the dialect of PostgreSQL, and everything else a machine that only has the Worker
needs: it never reaches the source.

## Personal data

The rows may hold personal data, so the sample treats every job as if they did (section 10 of the design):

- Only the columns the statement lists leave the source, and the ones that must not travel in clear are
  masked **in the statement itself**. Keep `columns` in `extractor.ag` equal to the list of the statements.
- The source is read with a user that may only read. The broker should have TLS, a user for each role (the
  Extractor may only publish on `etl.*.batch` and `etl.*.control`; a Worker may only read those and write
  `etl.dead`), encrypted storage and a short retention. The dead letters hold data too: give them the same
  care, and a retention (the design proposes 14 days).
- The user of the Worker in the destination needs only select, insert, update and delete on the tables of the
  migration. The control tables hold keys and codes, no personal data; staging and the final table do.
- A problem of the databases or of the broker names the place, never the content of a row, and what a
  driver says is cleaned of what is between quotes before it is shown or kept in a dead letter.

## Changing it

- **Another table:** change the two statements `page` and `range` in `metagente.toml` (the same columns, in
  the same order) and `columns` and `table` in `extractor.ag`. The key must be a whole number called `id`.
- **Another source:** PostgreSQL, MySQL and MariaDB work the same way; the notes at the end of
  `metagente.toml` say what to change.
- **The size of a batch:** `size` is the most rows of a page and `bytes` the size the rows aim at. Try 64,
  128, 256 and 512 KiB, and look for the best throughput. The broker takes a message up to `max_broker_bytes`
  (1 MiB) of `[limits]`.

> **How this is tested:** `internal/runtime/sample_async_elt_test.go` runs the Extractor and
> `internal/consume/sample_worker_test.go` runs the Extractor and the Worker at every change, with the files of
> this folder, the migrations and a broker in memory: the whole table, a page that is halved, a stop in the
> middle and a restart, bad rows, the brake and a retransform, a copy of an event, a damaged event, a batch
> that was landed and stopped, a version of the tables that the Worker does not know, the close of a job and
> the purge. The integration tests (`integration/sample_async_elt_test.go` and `sample_worker_test.go`) run the
> Extractor against a real JetStream server, and the Extractor and the Worker through real JetStream and
> PostgreSQL, with the PostgreSQL configuration.
