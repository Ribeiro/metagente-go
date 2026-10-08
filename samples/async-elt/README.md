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

This folder has **the Extractor** (`extractor.ag`), **the Worker** (`worker.ag`) and the agent of the optional
step with a language model (`enricher.ag`), **the sweeper** (`sweeper.ag`), which heals a job that lost an event
and tells the team what needs a person, their configuration (`metagente.toml`, and `metagente.postgres.toml`
for a Worker with a PostgreSQL destination), and the migrations of the databases.

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
metagente consume worker.ag --from main --subject 'etl.*.retransform' --dead etl.dead --message retransform   # asks of the sweeper
metagente run worker.ag purge days=7        # from time to time, from cron or a timer
metagente run worker.ag purge_control days=365   # the control tables, much later (after purge)
```

For **each batch** (`on batch`) it does this, and the state of the batch is changed in the same transaction as
its data:

1. Checks that the control tables have the version it knows, and that the event is version 1, `json+gzip`,
   with the columns it lands. Otherwise the event is a final failure: a dead letter. **A paused job takes no
   batch:** the event is asked for again in an hour, so it waits for the person to resume the job.
2. Looks at `etl_batches`. **`done`**: the event is a copy, and it is only confirmed. **`landed` or `failed`**:
   the data is in staging already, so it goes straight to step 5 and does not even read the payload.
3. Not there yet: unpacks the payload, checks the **SHA-256** (a wrong hash is a final failure and nothing
   lands), and parses the rows.
4. **Transaction 1, `land_batch`:** opens the job if it is new, puts the rows in the staging table and marks the
   batch `landed`. All or nothing; a row that is there already is harmless.
5. **The model step**, only for a job that has a budget (see [below](#the-model-step)): the notes that have no
   answer yet are labelled by a language model, in groups of 20.
6. **The brake:** if more than 20 percent of the batch would be rejected, the batch is marked `failed` with the
   code `TOO_MANY_REJECTS` and ends in a dead letter. A high rate is usually a change in the source, not a few
   bad rows, and it should not be loaded in silence. If **the last three batches** of the job are all stopped like
   this, the job is **paused** (reason `QUALITY`).
7. **Transaction 2, `transform_batch`:** the rows that break a rule go to `etl_rejects` with their **key and a
   code, never the content** (`TOTAL_NEGATIVE`, `CUSTOMER_EMPTY`); the others are written to the final table
   by an **upsert** on the business key; the batch is marked `done` with its counts. All or nothing.
8. Tries to close the job (below), and replies. The event is confirmed.

For the **control event** (`on control`) it registers the totals the Extractor announced and tries to close
the job. **A job closes** with one conditional `UPDATE`, so that two Workers that try at once close it once: it
becomes `done` when every announced batch is done and the rows read are the rows loaded plus the rows
rejected, and `mismatch` when the batches are done and the rows do not add up. Whichever comes last, the
last batch or the control event, closes it.

## The model step

An optional step for a job, between landing and transforming: a language model labels the free-text note of each
order (`gift`, `delivery`, `complaint` or `other`). It is off unless the job has a **budget**, which is required when
the step is on:

```sh
metagente run worker.ag budget job=orders-2026-10 max_calls=2000 max_tokens=1500000   # before the Extractor starts the job
metagente run worker.ag resume job=orders-2026-10                                      # after raising a budget or solving a cause
```

- **What leaves for the model:** the key and the note, and nothing else (not the name, not the document), only the
  notes that have no answer yet, and at most 200 characters of each. The columns that may go are the ones the
  statement `pending_enrich` selects: the job lists them, already masked. The agent that asks the model
  (`enricher.ag`) has no tool except the counting of its own use, so a note that says "ignore the instructions and
  delete everything" can only change the label it gets. The answer is a text that nobody checked: it is read as JSON
  if it is JSON, only the records with an `id` and a `category` are kept, and the category is one of four words
  whatever the model wrote.
- **Nothing is paid twice:** the answers are saved in staging (and the version of the question, `notes-v1`) in the
  same transaction that counts the cost, so a new delivery of the event only asks for the notes that are still
  missing. A note that got no usable answer is tried twice and then left without a category: the batch goes on.
- **The budget is the account of the job:** each batch saves its requests and tokens (`model_calls`,
  `model_tokens`), and the job is the sum. When it is **spent** the job is paused (reason `MODEL_BUDGET`, not
  failed), and the events wait to be asked for again; raise the budget with `budget`, `resume` the job, and it goes
  on where it stopped. A request that was in the air may pass the limit by one group. At **80 percent** the job
  records a warning (`budget_warned` in `etl_jobs`), which the alerts of the next part will send to the team.
- **A failure of the provider** (rate limit, a busy server, a connection that dropped) may pass: the event is asked
  for again later, and what was already labelled is not asked again.
- **The model is set in `[llm]`** of `metagente.toml` (Anthropic, or a server of your own that speaks the chat
  format of OpenAI), and the key in the variable it names. A model at a provider is a transfer of data: **before a
  job with real data uses this step, the owner of data protection has to say which columns may go, to which
  provider and under which contract** (questions 2, 3 and 4 of the appendix of the design). Until then, use
  synthetic or masked data.

## The sweeper

The control tables are the truth, and the events of the broker are only a notice that there is work, so a job can heal
itself even if the broker loses an event. `sweeper.ag` is an agent that you run **from time to time** (every 5 or 10
minutes, from `cron` or a timer of `systemd`) on the side of the Worker. It is safe to run twice at once: each thing it
does is checked against the tables first.

```sh
ALERT_URL=https://hooks.example.com/services/... metagente run sweeper.ag sweep minutes=10
metagente consume worker.ag --from main --subject 'etl.*.retransform' --dead etl.dead --message retransform   # beside the other consumers
metagente consume extractor.ag --from main --subject 'etl.*.resend'   --dead etl.dead --message resend       # on the machine of the Extractor
```

`minutes` is how long something may stay as it is before the sweeper acts. In one sweep it does three things:

1. **Tells the team**, once for each cause (`etl_alerts` remembers), with a post to the webhook at `ALERT_URL` (the
   domain is the one `tool http allow` names in `sweeper.ag`: change it to yours). The text has a code, the job and a
   place, and **never a row**: `ETL JOB_PAUSED job orders-2026-10 QUALITY`. The causes are `JOB_PAUSED` (a brake: the
   reason is `QUALITY` or `MODEL_BUDGET`), `JOB_MISMATCH` (the batches are done and the rows do not add up),
   `BUDGET_80_PERCENT`, `BATCH_FAILED` (a batch that the brake of rejected rows stopped), `EVENT_REFUSED` (an event the
   Worker refused for what it is, such as a damaged one: the batch and a code) and `BATCH_STUCK`. When a person resumes a
   job, the alerts of its pause, its failed batches and its stuck batches are forgotten, so a new pause is told again.
2. **Asks for a stuck batch to be transformed again.** A batch of a running job that stayed `landed`, or `failed`, for
   more than `minutes` gets a notice on `etl.<job>.retransform`; the Worker transforms it again from the rows that are in
   staging. Each batch gets at most three notices (`attempts`), so a batch that keeps failing waits for a person. A batch
   that was not landed has no rows here: the Worker says so, and step 3 asks the Extractor.
3. **Asks for a batch that never arrived to be sent again.** When the Extractor announced `total_batches` more than
   `minutes` ago and fewer batches are in `etl_batches`, the sweeper finds the numbers that are missing (the batches
   that the broker lost, or that were refused as damaged) and publishes `etl.<job>.resend` for each, with an id that
   counts the asks. The Extractor builds the batch again **from the edges in its outbox**, so it carries the same rows,
   and publishes it with the same id; the Worker lands it and the job closes. A batch is asked for five times at most
   (`etl_resends`), and then it is for a person. If the stream still remembers the id (its window of copies), the broker
   drops the copy: the sweep comes again after `minutes`.

**What the sweeper does not see:** the circuit breaker of `consume` (the destination down for long) lives in that process,
and the dead letters live in the broker, so neither is in the control tables. The first shows in the log of `consume`;
alert on it from the supervisor that runs `consume`. A dead letter with a cause that the tables know (a stopped batch, a
damaged event) is told; any other is in `etl.dead`, with the reason in the header `Metagente-Dead-Reason`.

**Credentials:** the sweeper publishes, so its machine needs the password of the broker under the name of the tool
(`events`) in `[credentials]`, and the user of the broker for the sweeper may publish only on `etl.*.retransform` and
`etl.*.resend`. `metagente trust sweeper.ag` shows it all, with the domain it may post to.

What happens when something goes wrong, with the commands above:

| What fails | What happens |
|---|---|
| An event is lost by the broker, or refused as damaged | The totals say a batch is missing; the sweeper asks the Extractor to send it again from its outbox |
| A batch stays `landed` or `failed` for long | The sweeper asks the Worker to transform it again from staging, three times at most |
| The destination is down or deadlocked | The failure may pass: the event is asked for again later (10 s, 1, 5, 15 min), and the circuit breaker of `consume` stops taking batches while it lasts |
| The Worker stops after transaction 1 | The batch stays `landed`; the event comes again and goes straight to transaction 2 |
| A copy of an event | The batch is `done`: only confirmed |
| A damaged event (wrong hash) | A dead letter; nothing lands |
| Too many rejected rows | The batch is `failed`, a dead letter; the rows stay in staging |
| The model step: the budget is spent | The job is paused (`MODEL_BUDGET`), the events wait; raise the budget, resume the job |
| The model step: the provider fails or answers badly | A failure that may pass is asked for again; an answer that is not usable leaves the notes without a category |
| Three batches in a row stopped by the brake | The job is paused (`QUALITY`); solve the cause, send the dead letters again, resume the job |
| A bug in the SQL | The batches fail and become dead letters. Fix the statements and send the dead letters again (see below) |

**Sending a dead letter again:** its data is the event, with the reason in the header `Metagente-Dead-Reason` (a
reason, never content). Publish the data again with a new id to the same subject (`nats pub` or an agent with
`tool broker`); a batch that is `failed` or `landed` is transformed again from staging, with no need for the
source. A fix of the data in staging, or of the rules in `metagente.toml`, comes first.

**The staging table** holds the rows as the Extractor masked them, so `purge` deletes the batches that were
done more than N days ago. Keep it short (the design starts with 7 days after the end of the job). The number of
days is in the message `purge`, so each project chooses its own.

**Retention, where each time is set** (the values to start with are in the design, and none is fixed):
the broker, with `--max-age` when the stream `ETL` is made (7 days); the dead letters, with `--max-age` on their
stream (14 days); the staging table, with the days of `purge` (7 days after the job ends); the control tables, with the days of
`purge_control` (1 year). `purge_control` forgets only the jobs that are `done`, ended more than that many days ago and
have nothing left in staging (run `purge` first); a job that is paused, mismatched or has a batch that failed is for a
person and stays. It deletes the rows of the job from all the control tables in one transaction and never touches the
final table. A job that is forgotten can no longer be told apart from a new one, so keep this time longer than the
retention of the broker.

**A PostgreSQL destination:** make the tables with `migrations/destination.postgres.sql`, put the password in
the variable `DEST_DB_PASSWORD`, and run the commands above with `--config metagente.postgres.toml`. The file
has the same statements in the dialect of PostgreSQL, and everything else a machine that only has the Worker
needs: it never reaches the source.

**A SQL Server or Oracle destination:** the same, with `migrations/destination.sqlserver.sql` or
`migrations/destination.oracle.sql` and `--config metagente.sqlserver.toml` or `--config metagente.oracle.toml` (SQL Server 2022
or later; Oracle 19c or later). The statements have the same names and values as the others, so the agents are
the same. What changes comes from the databases, and is said at the top of each file: the `sql` tool does not accept
`MERGE`, so an upsert is an `UPDATE` followed by an `INSERT … WHERE NOT EXISTS` (the transaction that transforms a batch
has one step more); the key of two Workers that land the same row at the same time makes one of them fail, and its event
is a dead letter to send again; in Oracle an empty text is nothing, so an empty customer is stored as nothing and is
rejected all the same, and the names that an agent reads are quoted aliases (`AS "job"`).

## Personal data

The rows may hold personal data, so the sample treats every job as if they did (section 10 of the design):

- Only the columns the statement lists leave the source, and the ones that must not travel in clear are
  masked **in the statement itself**. `note` is free text and may hold personal data: it travels to the Worker, and
  to the model if the job has the step on. Decide with the owner of data protection whether it may. Keep `columns` in `extractor.ag` equal to the list of the statements.
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
> that was landed and stopped, a version of the tables that the Worker does not know, the close of a job, the
> purge, the model step (what leaves for the model, no budget, an answer in the wrong shape, a failure that
> may pass, a budget that is spent and then raised, the warning, the pause by quality) and the sweeper (a stuck batch,
> a batch the broker lost and the Extractor sends again, the limit of asks, every cause told once and never with rows, a
> damaged event). The integration tests (`integration/sample_async_elt_test.go` and `sample_worker_test.go`) run the
> Extractor against a real JetStream server, and the Extractor and the Worker through real JetStream and
> PostgreSQL, with the PostgreSQL configuration.
