# Asynchronous ELT with Metagente: design draft

**Status:** draft. Nothing in this page exists yet; it describes what could be built. The questions that
were open in the first draft have a decision in [section 17](#17-decisions); two of them (personal data
in the model step, and retention) are proposals until the owner of data protection confirms them. That confirmation is needed before the
first run with real data, not before the code (see [when each thing is needed](#when-each-thing-is-needed)).
**Date:** 2026-10-08.

## 1. What we want

Copy a large table from a **source** relational database to a **destination** relational database,
transforming it on the way, in batches, with agents on different machines. When a batch fails, it must
be possible to try it again later, and to run again a batch that already worked.

## 2. What we know about the use case

| # | Fact | Consequence |
|---|---|---|
| C1 | Only the machine of the Extractor reaches the source. | The Worker can never read the source. |
| C2 | The Extractor cannot write to the destination. | The data has to cross from one side to the other. |
| C3 | There is no storage that both sides can reach. | The data crosses inside the events of a broker. |
| C4 | The rows may hold personal data. | Treat every job as if they do (see [section 10](#10-personal-data)). |
| C5 | The destination may be any relational database. | SQL is written once for each dialect (see [section 12](#12-any-relational-database)). |
| C6 | The transformation may be only SQL, or may use a language model in volume. | The model step is optional for each job (see [section 11](#11-language-model-enrichment)). |
| C7 | The volume and the time allowed are not known. | The pipeline measures itself (see [section 14](#14-sizing-without-numbers)). |
| C8 | A failed batch must be tried again, also much later. | A broker that keeps the events (JetStream is the reference). |

## 3. Decisions in short

1. **Two agents and a broker.** The Extractor (zone of the source) and the Worker (zone of the
   destination) are separate, because no single machine reaches both databases.
2. **The data goes inside the event** (batches limited by size in bytes). The Worker never calls back the
   Extractor, so once a batch is published the Extractor is not needed again, not even to run it again.
3. **ELT, with a staging table in the destination.** The Worker lands the raw batch in a persistent table
   (not a session `TEMP` table), then transforms it with SQL in the same database.
4. **The control tables in the destination are the truth;** the events of the broker are a notice that
   there is work. The state of each batch is changed in the same transaction as the data.
5. **Idempotency in layers** (publish, outbox, staging key, final upsert), because the delivery of a
   broker is at least once.
6. **A batch whose rows are bad does not stop the job:** bad rows go to a quarantine table, with a
   code and the key of the row, never with its content. A brake stops the batch, and then the job, when
   too many rows are rejected.
7. **The language model is a separate, optional step** between landing and transforming, and its results
   are saved in the staging table, so a failure does not repeat the paid calls.
8. **Metagente gets generic pieces, not an ETL engine:** a `sql` tool with named statements, a `broker`
   tool, a `consume` command and a bounded loop. The pipeline itself is a sample made of two agents and
   SQL files.
9. **Personal data:** only the needed columns leave the source, masked there; TLS and rights for each
   subject on the broker; short retention; no row content in logs, errors or the quarantine.
10. **Measure first:** a pilot with a sample says how many batches, Workers and bytes the real job needs.

## 4. Architecture

```text
 ZONE OF THE SOURCE                 BROKER (JetStream)                 ZONE OF THE DESTINATION
┌────────────────────────┐      ┌───────────────────────┐       ┌──────────────────────────────────┐
│ Extractor              │      │ stream etl.<job>.*     │       │ Worker (N copies)                │
│  reads pages of the    │ ───► │  .batch   the data     │ ───►  │  1. check the hash               │
│  source (named SELECT, │ pub  │  .control job planned  │ pull  │  2. land in staging   (tx 1)     │
│  read only)            │      │  .dead    dead letters │       │  3. enrich (optional, model)     │
│  SQLite outbox: the    │      │ limits: age, bytes,    │       │  4. transform with SQL (tx 2)    │
│  edges of each batch   │      │ discard NEW            │       │  5. ack                          │
└──────────┬─────────────┘      └───────────────────────┘       └───────────────┬──────────────────┘
           │                                                                    │
       ┌───▼────┐                                                       ┌───────▼───────────────┐
       │ source │                                                       │ destination           │
       │   DB   │                                                       │ etl_* + staging + final│
       └────────┘                                                       └───────────────────────┘
```

Subjects (names are an example): `etl.<job>.batch`, `etl.<job>.control`, `etl.<job>.dead`.

## 5. The batch event

```json
{
  "v": 1,
  "event_id": "019271c4-8d2e-7b3a-9f10-4c5d6e7f8a9b",
  "job_id":   "019271c4-0000-7000-8000-aaaaaaaaaaaa",
  "seq": 4812,
  "source": { "table": "orders", "key": "id", "after": 4811000, "upto": 4812000, "as_of": "2026-10-08T03:00:00Z" },
  "columns": ["id", "customer", "total"],
  "rows": [[4811001, "…", 10.5], [4811002, "…", 7.0]],
  "row_count": 912,
  "encoding": "json+gzip",
  "sha256": "…"
}
```

- `event_id` and `job_id` are UUID v7 (ordered by time): good for the index and for deleting old
  records. The key that protects against duplicates is `(job_id, seq)`, not `event_id` (see section 9).
- The rows go as lists of values with the names of the columns once, not as a record each: it is much
  smaller.
- **The batch is cut by size in bytes,** not by number of rows: the target is about 256 KiB before the
  envelope, far from the 1 MiB that brokers like NATS accept by default. The number of rows varies.
- `sha256` is of the rows before compression; the Worker checks it before anything else.
- The Worker refuses a version `v` it does not know, and a payload that, once decompressed, passes a
  limit (a compressed message can hide a huge one).
- The edges `(after, upto]` of every batch are chosen once and saved in the outbox, so the same batch
  can be built again exactly the same.

## 6. The control tables in the destination

Types are shown in a generic way; each dialect has its own file.

```text
etl_meta     schema_version                                              -- the Worker checks it at start
etl_jobs     job_id (key), name, state, total_batches, total_rows, source_as_of, started_at, finished_at
etl_batches  job_id, seq (key together), state, rows_read, rows_loaded, rows_rejected, attempts,
             landed_at, done_at, transform_version, enrich_version, model_calls, model_tokens,
             last_error_code
etl_rejects  job_id, seq, source_key, reason_code                       -- never the content of the row
stg_<table>  job_id, seq, source_key (key together), the columns of the source, the columns of enrichment
```

State of a batch: `(none)` → `landed` → `done`. A failure writes or updates `failed` with the number of
attempts and a code. A batch given up goes to `dead`. `done` and `dead` are the only ones that end it.
A job is `running`, `paused` (by the budget of the model or by the brake of quality), `done` or `failed`.

- **Transaction 1 (land):** insert the rows in staging and the line of `etl_batches` as `landed`, if the
  batch is not yet there. Everything or nothing.
- **Transaction 2 (transform):** the SQL that writes the final table (an upsert by the business key),
  that sends the rejected rows to `etl_rejects`, and the update of the batch to `done` with its counts.
  Everything or nothing.
- A copy of the event that arrives later finds `done` and is only confirmed; one that finds `landed`
  goes straight to transaction 2, without needing the data of the event again.

## 7. What each agent does

**Extractor**
1. Read the job (which table, which columns, which masks, the size of the batch in bytes).
2. Ask the source for the next page by key: `WHERE key > :after ORDER BY key LIMIT :page`, in a
   read-only transaction with a fixed `as_of`.
3. Cut the page at the size limit; save `(job_id, seq, after, upto)` in the outbox (SQLite on this
   machine).
4. Publish the event with `Msg-Id = job_id:seq`, and wait for the confirmation of the broker.
5. At the end, publish `etl.<job>.control` with the totals (batches and rows).
6. If the broker refuses (it is full), wait: that is the back pressure.
7. After a restart, publish again what the outbox says was not confirmed, with the same ids.

**Worker** (one or more copies, all with the same file)

At start, check the version in `etl_meta`, and refuse to run if it is not the one expected.

1. Take a batch (pull, with a limit of batches in flight). If the destination does not answer, stop
   taking batches (see section 8).
2. Check the version, the size and the hash. A wrong hash is final: dead letter and an alert.
3. Look at `etl_batches`: `done` → confirm and stop; `landed` → go to 5.
4. Transaction 1.
5. If the job uses a model: enrich the rows that still have no answer, in small groups, saving each
   group in staging (see section 11). If the budget of the job is reached, pause the job and stop taking
   batches.
6. Transaction 2. If the share of rejected rows passes the limit (20% to start), it is undone and the
   batch fails instead; if several batches fail this way, the job is paused. Then confirm the event.
7. If the last batch of a job just ended, close the job with one conditional `UPDATE` (so two Workers
   do not close it twice) and compare the totals.

## 8. Failures and how they are fixed

| What fails | What happens | How it is fixed |
|---|---|---|
| The Extractor stops | the broker keeps what was published | it restarts, publishes what the outbox says is pending; `Msg-Id` drops copies |
| The source does not answer | the Extractor waits and tries again | the Workers keep emptying the broker meanwhile |
| The broker is down | the Extractor cannot publish and waits | the outbox keeps the batches; the events kept are not lost |
| The stream is full | the publication is refused (discard new) | the Extractor waits; no unprocessed event is thrown away |
| A Worker stops in transaction 1 | the transaction is undone | the event comes again |
| A Worker stops between 1 and 2 | the batch stays `landed` | the event comes again and goes straight to transaction 2 |
| The destination is down | the Worker **stops taking batches** (a circuit breaker) instead of failing each one | the batches wait, and the limit of deliveries is not used up by an outage |
| A copy of an event | the batch is `done` | only confirmed |
| A bad row | goes to `etl_rejects` with a code | the batch goes on; the key says which row. If more than a share of the batch (20% to start) is rejected, the batch fails, and if several do, the job pauses: it is probably a change in the source |
| A bug in the SQL | the batches fail, then become `dead` | fix the SQL, and send an event of kind `retransform` for those batches: the data is already in staging |
| A damaged event (wrong hash) | final failure, dead letter | the Extractor sends it again from the outbox (it reads the same edges from the source) |
| The model provider limits or fails | the Worker waits and tries again; the same circuit breaker | the groups already saved are not paid again |
| Events lost by the broker | the totals do not match | the Worker side asks for the missing `seq` on `etl.<job>.control`, and the Extractor rebuilds them |

A **sweeper** (an agent run from time to time) takes the batches that stayed `landed` or `failed` for too
long and sends them a `retransform` or `resend` notice. The control table is the truth, so the pipeline
heals itself even if the broker loses an event. This part can come after the first version.

The sweeper runs as a scheduled agent (`cron`, or a timer of `systemd`) on the side of the Worker, every
5 or 10 minutes. It is idempotent, so two runs at once do no harm. It sends alerts with `tool http` to the
channel of the team when a batch goes to the dead letters, when a job closes with totals that do not
match, when the circuit breaker of the destination stays open for long, and when a budget is reached. An
alert has codes and counts, never row data.

## 9. Idempotency in layers

1. **Publication:** `Msg-Id = job_id:seq` (fixed, not random). The broker drops a copy that arrives
   inside its window of duplicates.
2. **Outbox:** the edges of a batch are saved before the publication, so after a restart the same `seq`
   is never built with different rows. (A random id made at each attempt would break this.)
3. **Staging:** the key `(job_id, seq, source_key)` makes landing twice harmless.
4. **Final table:** an upsert by the business key.
5. **The batch marker in the same transaction** as the data. This is the one that makes the effect
   happen once, even if the delivery happens many times.
6. **Calls outside the database** (a model, an HTTP call) carry the `event_id` as an idempotency key
   when the other side accepts one. The delivery of a broker is "at least once", never "exactly once".

## 10. Personal data

The rows may have personal data, so the design treats every job as if they did.

- **Take only what is needed.** The Extractor's named `SELECT` lists the columns; those that are not
  needed do not leave the source.
- **Mask at the source.** What must not travel in clear (a document number, an e-mail) is hashed or
  cut in the `SELECT` itself, before the data leaves the zone. The mask is part of the job.
- **The broker:** TLS, a user for each role (the Extractor may only publish on `etl.*.batch`, a Worker
  may only read it), encrypted storage (the encryption of JetStream or of the disk), and a **short
  retention**. An event cannot be edited, so a request to erase a person is answered by retention and
  by deleting from staging and the final table; the retention chosen must be compatible with that.
- **The destination:** staging holds the raw data, so it has a **purge** (delete `done` batches after N
  days) and the same access care as the final table.
- **No content in logs, errors or the quarantine.** Database drivers put values in some messages (for
  example, a key that already exists): they are cleaned before they are shown or saved. `etl_rejects`
  has the key and a code only.
- **The language model is a transfer of data to a provider.** See the next section.
- **Retention, to start with (to confirm with the owner of data protection):** broker 7 days, with a
  limit of bytes and discard new; dead letters 14 days, then deleted (they hold data too); staging 7 days
  after the job ends for the `done` batches, and for `failed` and `dead` until they are solved plus 7
  days; the control tables 1 year (they hold no personal data). The time to answer a request to erase
  data has to be longer than the retention.

## 11. Language model enrichment

An optional step for each job, for the columns that need a model to be understood.

- It reads the rows of the batch that still have no answer (a named `SELECT` with a limit), asks the
  model by group, and saves the answer in staging columns (a named `UPDATE`). On a new try it only does
  what is missing, so the paid calls are not repeated.
- It saves the version of the prompt and of the model (`enrich_version`). The same row may get another
  answer when run again; the final upsert overwrites the old one.
- **The groups are small,** and a tool never gives the model more than its limit (32 KiB by default).
  The batch of the event is never sent whole to a model.
- **The concurrency is the one the provider allows:** number of Workers times the groups in parallel,
  with the limit of batches in flight as the brake.
- **Every job with the model step has a budget:** a maximum of calls and of tokens. When it is reached,
  the job **pauses** (it does not fail) and warns the operator, who raises the limit or ends the job.
  Each batch saves its calls and tokens (`model_calls`, `model_tokens`), so the cost is seen, and the
  pilot estimates it before the full load.
- **Personal data:** the job lists the columns that may go to the model (the named `SELECT` of the step
  selects only them), already masked. If they still hold personal data, use a provider with a suitable
  contract and no retention for training, or a server of your own through `openai-compatible`. For each
  batch, record (without content) which columns went, to which provider and with which version of the
  model. The first pilot uses synthetic or masked data.

## 12. Any relational database

The destination has to give: transactions, a unique key, and a way to insert a row only if it is not there.
Everything else changes with the dialect, so each dialect has its own SQL files for the statements the
pipeline uses (create the control tables, land, transform, purge, count):

| Need | PostgreSQL / SQLite | MySQL / MariaDB | SQL Server / Oracle |
|---|---|---|---|
| Upsert | `INSERT … ON CONFLICT (…) DO UPDATE` | `INSERT … ON DUPLICATE KEY UPDATE` | `MERGE` |
| Bulk landing | `COPY` or multi-row `INSERT` | multi-row `INSERT` / `LOAD DATA` | bulk copy / multi-row `INSERT` |
| Insert if absent | `ON CONFLICT DO NOTHING` | `INSERT IGNORE` | `MERGE` / test the key |

- The statements are **named, parameterised and written in files** of the project; the model or the
  agent never builds SQL. The names are listed in `metagente.toml`, the password in `[credentials]`.
- Version 1 covers PostgreSQL, MySQL/MariaDB and SQLite (SQLite for the tests and for the outbox of the
  Extractor). SQL Server and Oracle come after, by the same mechanism. Each engine added needs its own
  tests.
- Pure Go drivers exist for the three. They are behind build tags: the release is built with the three,
  so whoever uses it compiles nothing, and whoever wants a lean binary, or another set, builds with the
  tags chosen. Each driver goes through `govulncheck` and Dependabot like the other dependencies.

## 13. New pieces in Metagente

The pipeline is made of **generic pieces**, with their safety rules, and of an example that uses them.

**`tool sql`** (source, destination and local SQLite for the outbox)
- Only named statements, with parameters; DSN as a credential (the name of a variable, like the
  tokens today); TLS required when the database is not on this computer.
- Read-only transaction for the source; limits of rows and bytes for an answer, and a timeout.
- A bulk action that inserts a list of rows in one operation.
- The approval of `metagente trust` lists the host and the names of the statements, not the DSN.
- Error messages cleaned of values (section 10).
- Metagente does not run DDL on the destination: the control tables are created by migrations of the
  user (the sample brings the DDL files), and the Worker only checks their version.

**`tool broker`**
- `publish` on subjects that the agent **declares** (like `tool http allow "domain"` does for sites), so
  it cannot publish anywhere else.
- TLS for a broker that is not on this computer; credentials in `[credentials]`; approval by `trust`.
- Behind it there is a small interface: publish with an id against duplicates; pull with confirm, ask
  again later, end, and in progress; replay by sequence or by time. Only JetStream is built in version 1.
  The fake in memory of the tests uses the same interface.

**`metagente consume FILE.ag --from … --subject …`**
- Takes the events and runs the agent with them, as an A2A call would; the values are checked against
  `accepts` before the agent runs.
- Confirms after a good result, asks again later (`nak` with a wait) after a failure that can pass, ends
  an event with `term` and sends it to the dead letters after a failure that cannot. The failures are
  classified by where they come from (section 17, item 1); a `fail` of the agent is final, unless it
  carries `retry` (see below).
- Pulls batches (a limit in flight), tells the broker that a long batch is still in progress, and has
  the circuit breaker of section 8.

**A bounded loop**, `repeat while condition [up to N times]`, for paging. It is built: see
[the language](LANGUAGE.md#repeat). The ceiling of turns is `max_loop_turns`.

**`fail … retry`** (a change to the language)
- `fail "The destination is busy" retry in 60 seconds`. The time is optional and is only a suggestion,
  with a maximum. A `fail` without `retry` keeps the meaning it has today: final.
- Only `retry` is a new reserved word (`in` and `seconds` already are). No example or document uses it
  as a name.
- The caller sees it according to what it is: `metagente run` prints a note that it may pass; A2A marks
  the task as one that can be tried again and gives the suggested wait; `consume` asks the broker for a
  new delivery after the wait.
- `check` warns about a time that is not a positive number, or that is longer than the maximum.

**Not in Metagente:** the ETL itself. The two agents, the SQL files and the control tables are a sample
(`samples/async-elt`), so the core stays small and each user changes the pipeline without changing the
program.

## 14. Sizing without numbers

The pipeline writes the time of every state of every batch, so it measures itself.

| Knob | Effect |
|---|---|
| Size of the batch (bytes) | bigger: fewer events and less overhead; smaller: cheaper to try again |
| Workers | more throughput, more load on the destination and on the provider |
| Batches in flight | the brake that protects the destination and the provider |
| Limits of the stream (age, bytes) | how long a job can be tried again, and how much it can hold |
| Retention of staging | how long a transformation can be run again without the source |

Pilot: run a sample of about 100 thousand rows and read from `etl_batches` how long each step takes.
Then: total time ≈ batches × time of a batch ÷ Workers. The disk of the broker needs to hold the whole job
(for example, 20 million rows of 200 bytes are about 4 GB before compression) for as long as the
retention says.

### Pilot and starting values of the brakes

The pilot runs after the sample of phase 3 exists, and before the job goes to production.

- **Data:** 100 to 500 thousand rows, synthetic or masked, in a database of the same kind as the
  destination.
- **Runs:** with 1, 2 and 4 Workers. Measure reading, publishing, landing and transforming; in the model
  step, calls, tokens, latency and errors.
- **Failures to inject:** kill a Worker between transaction 1 and 2, stop the destination, stop the
  broker, and rows that are invalid in 1%, 5% and 25% of the batches.
- **To approve:** the reconciliation closes (read = loaded + rejected), there is no duplicate after a
  kill, and each failure is fixed as the table of section 8 says.

| Brake | Starting value | How to tune it |
|---|---|---|
| Rejected rows in a batch | 20% | 5 to 10 times the normal rate measured, never below 5% |
| Pause the job | 3 batches in a row above the limit | review with the real rate |
| Destination down | opens after 3 failures in a row; tries again every 30 s, waiting up to 5 min | the real time of recovery |
| Time to confirm a batch | 3 times the p99 of the time of a batch | the times of the pilot |
| Maximum deliveries | 5, waiting 10 s, 1, 5 and 15 min | the failures injected |
| Batches in flight | 2 times the number of Workers | raise it until the latency of the destination gets worse |
| Size of a batch | try 64, 128, 256 and 512 KiB | the best throughput with a p95 under a third of the time to confirm |
| Budget of the model | tokens per row × rows × 1.3 | pause at 100%, warn at 80% |

### Measured in the pilot of 9 October

First manual run of the pilot in the CI: 100 thousand synthetic rows, 1,000 rows per batch, PostgreSQL as
the destination, a small runner where the destination, the broker and the Workers share the same machine,
outages and freezes of 20 s, time to confirm of 20 s. All runs were approved: the reconciliation
closed, there was no duplicate, and no batch was lost to a failure that may pass.

| Workers | Rows per second | p95 of a batch | p99 of a batch |
|---|---|---|---|
| 1 | 8.7 thousand | 0.23 s | 0.24 s |
| 2 | 12.2 thousand | 0.34 s | 0.52 s |
| 4 | 13.0 thousand | 0.72 s | 0.75 s |

| Failure | What was seen |
|---|---|
| A Worker killed | the batches it held came back after the time to confirm; the cost was 15.4 s with 20 s to confirm |
| Destination frozen for 20 s | one batch took 20.3 s; no redelivery (the heartbeat kept it alive), no error, the breaker stayed closed |
| Destination refusing connections for 20 s | the breaker opened twice, 8 batches went back to the broker, no dead letter, the job finished; the Workers waited about 11 s more after the destination was back |
| Broker frozen for 20 s | the Extractor waited and finished; no redelivery |
| Invalid rows in 1% and 5% | exactly 1,000 and 5,000 rows rejected; the job finished |
| Invalid rows in 25% | the job paused with reason QUALITY after 3 failed batches in a row; the batches that were in flight at that moment went to the dead letters and need to be sent again once the cause is fixed |

Values to start with, from these numbers:

| Brake | Value | Why |
|---|---|---|
| Workers | 2 | going from 2 to 4 gave 6% more throughput and doubled the latency of a batch |
| Time to confirm a batch | 5 s | 3 times the p99 is 2.2 s; a Worker that dies costs about the time to confirm, and a slow batch is not sent again because the live Worker renews the confirmation |
| Maximum deliveries, quality brake (20%, 3 in a row) | unchanged | nothing in the pilot asked for a change |
| First wait of the breaker | 30 s, to be decided | after a short outage the Workers idle until the wait ends; 10 s (keeping the 5 min ceiling) would recover faster |

What is not measured yet: the size of a batch in bytes (the pilot cut batches by rows), the model step, and a
real destination. The numbers of a small shared runner give the shape, not the capacity: repeat the pilot
against the destination of production before trusting the throughput.

## 15. Phases

1. **`tool sql` and the bounded loop.** Useful alone: it answers the question of reading a large
   database in pages, with an agent.
   *Status:* `repeat while` is done. `tool sql` is done for SQLite, read only (named statements in
   `[sql.NAME]`, results `rows`/`row`/`value`), and for PostgreSQL, MySQL and MariaDB, with the
   `integration/` module that tests them in containers. Phase 1 is done; phase 2 is next.
2. **`tool broker` and `metagente consume`,** with JetStream and the rules of section 8.
   *Status:* `fail … retry` is done (see [the language](LANGUAGE.md#fail)), and so is `tool broker` for
   publishing (JetStream, with a broker in memory for tests), and `metagente consume` (pull in batches with
   a limit in flight, confirm, ask again later, end with a dead letter, progress, circuit breaker). Phase 2 is
   done; the sample of phase 3 is next.
3. **The sample `samples/async-elt`:** the Extractor, the Worker, the SQL files of one or two dialects,
   the control tables, the purge, the closing of a job, the budget of the model step and the brakes.
   *Status:* first step done: `tool sql` can change rows (see [the language](LANGUAGE.md#changing-a-database-mode--write)):
   `mode = "write"`, statements with `INSERT`, `UPDATE` and `DELETE`, a list of rows in one transaction
   (`each`/`columns`) and transactions of several statements, which is what the Worker needs to land and to
   transform. `tool codec` is done too (JSON, gzip, SHA-256, UUID v7 and records), for the batch event of
   section 5. The Extractor and the Worker of the sample are done (`samples/async-elt`: control tables, landing,
   transformation, rejects, the brake of rejected rows, the closing of a job and the purge, for SQLite and
   PostgreSQL), the model step with its budget (the job pauses when it is spent) and the brake of a whole job
   (three batches in a row stopped by the brake of rejected rows pause it). Phase 3 is done.
4. **The sweeper with its alerts and the resend of lost batches,** and more dialects (SQL Server, Oracle).
   *Status:* the sweeper is done (`samples/async-elt/sweeper.ag`: alerts once for each cause with codes and counts, the
   notice to transform a stuck batch again, the ask to the Extractor to send a lost batch again). It does not see the
   circuit breaker of `consume`, which is in that process. The `sql` tool now speaks SQL Server (`sqlserver`) and
   Oracle (`oracle`), tried against real servers in `integration/`, and accepts `MERGE` where the database has one
   (the sample does not use it: its upsert is an update and an insert). The sample has the SQL for these two engines too
   (`metagente.sqlserver.toml`, `metagente.oracle.toml` and their migrations), tried against real servers, and the Extractor
   reads a PostgreSQL, MySQL, MariaDB, SQL Server or Oracle source as well as SQLite (`samples/async-elt/sources/`).
   The pilot of section 14 is written (`integration/pilot_test.go`, run by hand from the workflow `Pilot`); its numbers
   are still to be read and the brakes tuned.
5. **The Extractor and the Worker made from a description.** The agents of the sample repeat, for every table, the same
   steps; what changes is the table, the columns, the masks and the rules. *Status:* done for the part that does not use
   a language model. `tool x from elt "name" extract` and `... load` (see [the language](LANGUAGE.md#an-asynchronous-elt-made-from-a-description-tool-from-elt))
   give an agent the tools, the messages and the handlers that the section `[elt.name]` of `metagente.toml` calls for, and
   the statements are made from it for the source (any database the `sql` tool reads), the outbox (SQLite) and the
   destination (SQLite and PostgreSQL). The events are the same as the ones of the sample. The step with a language
   model, and SQL Server and Oracle as the destination, are still written by hand, as in the sample.

## 16. Tests

- **Unit:** the batch cut by size, the hash, the limit of decompression, the state machine of a batch
  (a table of states and events), the outbox after a restart.
- **With a database:** SQLite in every run, with no container. PostgreSQL and MySQL/MariaDB through
  Testcontainers (below).
- **With a broker:** a fake in memory for the unit tests. The integration tests use a real JetStream
  server through Testcontainers, for redelivery, discard new, the circuit breaker and the replay.
- **The integration tests use Testcontainers** (`testcontainers-go`). This is a rule of the project:
  - PostgreSQL, MySQL/MariaDB and NATS with JetStream run in containers, with the images pinned by
    digest, like the versions pinned for tool servers.
  - They live in a module of their own (`integration/`, with its own `go.mod`), so the product, `go
    install`, `govulncheck` and Sonar do not take the dependencies of the tests. The module has its own
    `govulncheck` step.
  - They run the compiled binary as a black box, like `internal/acceptance`, with the build tag
    `integration` and a target of their own in the `Makefile`.
  - In the CI they run in the Linux job only (the runners of macOS and Windows do not run Linux
    containers). Without a container runtime they are skipped, not failed.
  - The tests of failure stop or pause the container of the database or of the broker in the middle of a
    job, and kill the Worker between transaction 1 and 2, then check that the result is the same.
  - Podman works through its compatible socket, with settings that depend on the version; it is checked
    when the tests are written.
- **Safety:** statements that are not named are refused, the DSN and the content of rows never appear in
  an error or a log, the `trust` list.

## 17. Decisions

The questions that were open in the first draft, with what was decided. The items marked *to confirm*
are proposals until the owner of data protection of the organization confirms them.

Reported by the maintainer on 2026-10-08: the team accepts personal data inside the broker, which is the
premise of the batch event in section 5. Also by the maintainer on 2026-10-08: for now no personal data has
to be masked or pseudonymized (item 2), and the retention times of item 3 are accepted, as long as each one
can be configured. Both stay open for the owner of data protection of the organization to confirm.

1. **What is a failure that can pass, and one that cannot?** *Decided.* The runtime classifies by where
   the error comes from. Infrastructure (a timeout, a connection, the destination or the broker down, the
   limit of rate of the provider, a deadlock) can pass: it is tried again with a growing wait. A problem
   in the event (an unknown version, a wrong hash, a message too large once decompressed) is final and
   goes to the dead letters. A `fail` of the agent is final by default, because it is a rule of the
   business that stopped the batch; the optional mark `retry` asks for a new try: `fail "message" retry`, or
   `fail "message" retry in 60 seconds` to suggest the wait (section 13). Every new try has a maximum
   number of deliveries, so none goes on forever.
2. **Personal data in the model step.** *Decided by the maintainer, to confirm.* Nothing goes to the model
   unless it is allowed, column by column: the job lists the columns. For now they need not be masked or
   pseudonymized, so they may carry personal data as they are: use a provider with a suitable contract
   (no training with the data, no retention), or a server of your own. Each batch records,
   without content, the columns, the provider and the version of the model. The first pilot uses
   synthetic or masked data. This also involves the owner of data protection, not only engineering.
3. **Retention.** *Accepted by the maintainer, to confirm; each time has to be configurable.* Short and each
   with a reason: broker 7 days, dead letters 14 days, staging 7 days after the end of the job, control tables
   1 year (section 10). These are the values to start with, not fixed ones: the age of the stream is set
   when the operator makes it (`--max-age`), the staging purge takes the number of days in its message, and
   the purge of the control tables (`purge_control` in the sample) takes its time the same way. The time to answer a request
   to erase data has to be longer than the retention.
4. **Bad rows.** *Decided.* Quarantine and go on, with a brake: if more than a share of a batch (20% to
   start) is rejected, the batch fails; if several do, the job pauses. A high rate of rejection is
   usually a change in the source, not a few bad rows.
5. **Engines in version 1, and the drivers.** *Decided.* PostgreSQL, MySQL/MariaDB and SQLite; SQL Server
   and Oracle later. The drivers are behind build tags, and the release is built with the three.
6. **A budget for the model step.** *Decided, comes with the sample (phase 3).* Required when the step is
   on: a maximum of calls and of tokens for each job. At the limit the job pauses and warns.
7. **Who creates the control tables.** *Decided.* The user, by migration. Metagente does not run DDL on
   the destination; the Worker checks the version of the tables at start and refuses to run if it is
   not the expected one.
8. **Other brokers.** *Decided.* A small interface, with only JetStream built in version 1. The tests
   already need the interface for a fake broker.
9. **The sweeper, and who is told.** *Decided, comes in phase 4.* A scheduled agent on the side of the
   Worker, every 5 or 10 minutes, idempotent. The alerts go through `tool http` to the channel of the
   team, with codes and counts only.

### When each thing is needed

| What | Blocks |
|---|---|
| Confirm items 2 and 3 with the owner of data protection | the first run with real data (not the code; phases 1 and 2 and the pilot with synthetic or masked data go on) |
| The syntax of `fail … retry` (item 1, decided) | phase 2 (`consume`) |
| The values of the brakes (section 14) | going to production: they can only be measured when the sample of phase 3 exists |

## Appendix A. Questions for the owner of data protection

1. Which columns hold personal data, and is any of it sensitive?
2. What is the legal basis to treat it, and to send part of it to a provider of a model?
3. Which provider is allowed, and with which contract: no training with the data, no retention, region,
   sub-processors?
4. Is masking or pseudonymizing enough for the columns that go to the model, and which technique is
   accepted?
5. Do the proposed retentions (broker 7 days, dead letters 14, staging 7 after the job) serve, and in how
   many days must a request to erase data be answered?
6. Is it acceptable to delete by key in staging and in the final table, and to let the events of the
   broker expire?
7. Who may reach staging, the broker and the dead letters, and is an audit trail required?
8. Are there demands on encryption and on where the data may be kept?
