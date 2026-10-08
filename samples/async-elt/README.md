# Asynchronous ELT: the Extractor

Copy a large table from a **source** database to a **destination** database, in batches, with agents on
different machines, so that a batch that fails can be tried again, also much later. The design, the failures
it covers and the decisions are in [`docs/design-async-elt.md`](../../docs/design-async-elt.md).

```text
 ZONE OF THE SOURCE                 BROKER (JetStream)                 ZONE OF THE DESTINATION
┌────────────────────────┐      ┌───────────────────────┐       ┌──────────────────────────────┐
│ Extractor   (this part)│ ───► │ stream etl.>          │ ───►  │ Worker (the next part)       │
│  reads pages by key    │ pub  │  etl.<job>.batch      │ pull  │  lands, transforms, confirms │
│  outbox (SQLite)       │      │  etl.<job>.control    │       │                              │
└──────────┬─────────────┘      └───────────────────────┘       └───────────────┬──────────────┘
       source DB                                                          destination DB
```

This folder has **the Extractor** (`extractor.ag`), the configuration (`metagente.toml`), and two
migrations. The Worker, the control tables of the destination and the rest come in the next parts.

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

## Personal data

The rows may hold personal data, so the sample treats every job as if they did (section 10 of the design):

- Only the columns the statement lists leave the source, and the ones that must not travel in clear are
  masked **in the statement itself**. Keep `columns` in `extractor.ag` equal to the list of the statements.
- The source is read with a user that may only read. The broker should have TLS, a user for each role (the
  Extractor may only publish on `etl.*.batch` and `etl.*.control`), encrypted storage and a short retention.
- A problem of the databases or of the broker names the place, never the content of a row.

## Changing it

- **Another table:** change the two statements `page` and `range` in `metagente.toml` (the same columns, in
  the same order) and `columns` and `table` in `extractor.ag`. The key must be a whole number called `id`.
- **Another source:** PostgreSQL, MySQL and MariaDB work the same way; the notes at the end of
  `metagente.toml` say what to change.
- **The size of a batch:** `size` is the most rows of a page and `bytes` the size the rows aim at. Try 64,
  128, 256 and 512 KiB, and look for the best throughput. The broker takes a message up to `max_broker_bytes`
  (1 MiB) of `[limits]`.

> **How this is tested:** `internal/runtime/sample_async_elt_test.go` runs the files of this folder at every
> change, with the migrations and a broker in memory: the whole table, a page that is halved, a stop in the
> middle and a restart, a job started again with another size, and an outbox of another version. The
> integration tests (`integration/sample_async_elt_test.go`) run it against a real JetStream server.
