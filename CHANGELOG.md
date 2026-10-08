# Changelog

What changes in a way that a person who uses Metagente can notice is written here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the versions follow
[Semantic Versioning](https://semver.org/): while the first number is 0, anything may change between two
minor versions.

## [Unreleased]

### Added

- `samples/async-elt`: the message `purge_control days=N` of the Worker cleans the control tables (SQLite and PostgreSQL):
  the jobs that are `done`, ended more than N days ago and have nothing left in staging are deleted from all of them,
  in one transaction; a job that needs a person stays, and the final table is never touched. With it, every retention
  time of the design has a place to be set.
- `tool … from sql` reads and writes SQL Server (`driver = "sqlserver"`) and Oracle (`driver = "oracle"`) too, with
  drivers written in Go (no client to install). Statements are written in the dialect of each database; Oracle
  asks for a read only transaction before each read, and SQL Server, which has none, relies on the rights of the
  user. They have build tags that leave them out (`-tags nosqlserver`, `-tags nooracle`), and tests against real
  servers in `integration/`.
- `samples/async-elt`, the sweeper (phase 4 of `docs/design-async-elt.md`): `sweeper.ag`, run from time to time, which
  heals a job that lost an event and tells the team what needs a person. It posts alerts to a webhook once for each
  cause (a job paused by a brake, rows that do not add up, a budget at 80 percent, a batch stopped, an event refused,
  a batch stuck), always with codes and counts and never a row; it asks the Worker to transform again a batch that
  stayed landed or failed (`etl.<job>.retransform`, three times at most); and it asks the Extractor to send again a
  batch that never arrived (`etl.<job>.resend`, five times at most), which the Extractor builds again from the edges in
  its outbox. The Worker records the events it refuses (`etl_incidents`), the control tables have the columns and
  tables the sweeper needs (SQLite and PostgreSQL), and all this is tried at every change, and against real JetStream and
  PostgreSQL in `integration/`.
- `samples/async-elt`, the end of phase 3: the model step of the Worker, with its budget, and the brake of a whole
  job. A job that has a budget (the message `budget`: a number of requests and of tokens) has its notes labelled by
  a language model, in small groups, by an agent of its own (`enricher.ag`) that has no tool but the counting of
  its own use, so that what a note says cannot make the model do anything else. Only the key and the note leave
  the destination for the model, and only what has no answer yet, so a new try never pays twice; the answer is one
  of four words whatever the model writes. When the budget is spent the job is **paused** (not failed) with the
  reason `MODEL_BUDGET` and its events wait to be asked for again; `budget` raises the limit and `resume` lets it
  go on. 80 percent of the budget is recorded as a warning. Three batches in a row stopped by the brake of rejected
  rows pause the job with the reason `QUALITY`. The sample brings the columns, the tables and the statements for
  SQLite and PostgreSQL, and the tests of all this, also against a real PostgreSQL.
- `tool meter` (`meter.model`: the requests this conversation made to the language model, and their tokens), and
  `codec.try_parse` (`nothing` for text that is not JSON) and `codec.pick` (the records of a list that have the
  fields wanted), for an agent that reads what a model wrote and keeps the account of its cost.
- A failure of the language model that may pass (rate limit, busy server, dropped connection, too long to answer) is
  now marked as one that may pass, so `consume` asks for the event again later and `run` says so.

### Fixed

- `metagente consume` no longer stops taking events while one waits for its new delivery. The server counted the
  event that was asked for again later as still in progress, and with the limit of the server equal to the number of
  events worked on at once (one, by default) no other event came for the whole wait. The limit of the server is now
  only a bound on what can be waiting (1000), and the number worked on at once is still `--in-flight`.
- `samples/async-elt`, second half: the Worker of the asynchronous ELT (phase 3 of
  `docs/design-async-elt.md`), run with `metagente consume`. For each batch it checks the version, the encoding
  and the SHA-256, lands the rows in a staging table together with the mark of the batch (transaction 1),
  and transforms them (transaction 2): rows that break a rule go to a rejects table with their key and a code,
  never the content, the rest are written by an upsert, and the batch is marked done with its counts. A copy of
  an event is only confirmed, a batch that was landed goes straight to transaction 2, and a batch with more than
  20 percent of rejected rows is stopped and ends in a dead letter that can be sent again. A job is closed by one
  conditional `UPDATE` when every batch is done and the rows add up, and a `purge` message cleans staging. The
  sample brings the control tables for SQLite and PostgreSQL (migrations, and a configuration for PostgreSQL),
  and is tried at every change, and against real JetStream and PostgreSQL in `integration/`.
- `samples/async-elt`, first half: the Extractor of the asynchronous ELT (phase 3 of
  `docs/design-async-elt.md`). It reads a table by key in pages, halves a page that is too big for the
  size it aims at, saves the edges of every batch in a small outbox before publishing it, publishes each
  batch (rows packed with `tool codec`, with their hash and an id that lets the broker drop a copy), and
  ends with a control event with the totals. Started again with the same job, it sends again what was
  planned and not confirmed, and nothing twice. The sample brings the migrations of the outbox and of a
  demo source, and is tried at every change (a broker in memory in `internal/runtime`, a real JetStream
  server in `integration/`). `codec.count` was added to `tool codec` for it.
- `tool codec`, the small pieces that a pipeline needs to move rows between two programs (phase 3 of
  `docs/design-async-elt.md`): `codec.record` (a record made from the values given), `table` and `records`
  (rows as lists of values with the names of the columns said once, and back), `json` and `parse`, `size`,
  `gzip` and `gunzip` (as text in base64, with a limit on what is unpacked), `sha256` and `uuid` (version 7).
  It touches nothing outside its values, so it needs no approval; every text is limited by the new
  `limits.max_data_bytes` (8 MiB).
- `mode = "write"` in a `[sql.NAME]` section, for the side of the asynchronous ELT that lands data
  (phase 3 of `docs/design-async-elt.md`). Its named statements may begin with `INSERT`, `UPDATE` or
  `DELETE` and give how many rows they changed; `each` and `columns` run a statement once for each item of
  a list, in one transaction; `[sql.NAME.transactions]` groups statements that stand or fall together. An
  `UPDATE` or `DELETE` with no `WHERE` and any `CREATE`, `DROP` or `ALTER` are refused. `metagente trust`
  shows such a database as one that is changed, and it is another approval than for reading it. What a
  driver says is cleaned of what is between quotes, and a problem in a list names the place of the item,
  never its content. New limit `limits.max_sql_write_rows` (10000). A connection that only reads
  works as before and keeps its approval.
- `docs/tutorial.md`: your first agent in 15 minutes, adapted from the tutorial of the original project
  to this port (installing, `trust`, the token of `serve`, `[credentials]` for `remote`).
- `docs/LANGUAGE.md` has an index of every word of the language, a table of the commands, and the
  grammar of a `.ag` file (EBNF), taken from the lexer and the parser. A test now checks that every
  whole agent of `LANGUAGE.md` and of the tutorial passes `metagente check`.
- `repeat while condition`, a loop for what has no list to go through, such as the pages of an API or of
  a database that each say where the next one is. `up to N times` is a cap that the author chooses; with
  no cap, or one above the setup, a condition that is still true after `max_loop_turns` turns (10000 by
  default, in `[runtime]`) stops the run with a problem.
- `tool orders from sql "orders-db"`, to read a database in pages (first step of the asynchronous ELT of
  `docs/design-async-elt.md`). The statements are written by the person who runs the agents in a
  `[sql.orders-db]` section of `metagente.toml`, with `:name` parameters sent apart from the text, and the
  agent calls each one by name (`orders.next_page after: 0 size: 1000`). A statement gives `rows`, one
  `row` or one `value`. Read only, with limits (`limits.max_sql_rows`, `limits.max_sql_bytes`), a
  connection string only through `[credentials]`, and the database and the statements are approved by
  `metagente trust`. SQLite (pure Go, no C compiler) is the first driver; a build with `-tags nosqlite`
  leaves it out. The program grows by about 6 MB with it.
- `fail "message" retry` and `fail "message" retry in 60 seconds`: a failure that may pass. A `fail` with no
  `retry` is final, as before. `metagente run` prints a note under the problem; a linked agent keeps the
  mark when it fails inside another; over A2A the failed task carries `metadata.metagente.retry` and
  `retryAfterSeconds`, and a remote agent called by Metagente passes the mark on. `check` warns about a wait
  that is not above 0 or is longer than 3600 seconds. This is the first part of phase 2 of the asynchronous
  ELT (`docs/design-async-elt.md`); `metagente consume` will use it.
- `tool events from broker "main" publish "etl.orders.batch"`, to publish messages to a message broker
  (JetStream), only to the subjects the agent declared (`*` and `>` allowed). The broker is a
  `[broker.main]` section of `metagente.toml` (`url`, `user`, `tls`, `ca_file`, `stream`); the password or
  the token comes from `[credentials]`. `events.publish subject: … id: … data: …` needs an id, so the
  broker drops a copy of a message, and answers with the stream, the place and whether it was a copy. A
  full stream and a broker that cannot be reached are failures that may pass (`fail … retry`); `check`
  refuses a subject the tool did not declare; the broker, the subjects and the settings of the connection
  are approved by `metagente trust`. `limits.max_broker_bytes` (1 MiB) limits a message. JetStream can be
  left out of a build with `-tags nojetstream`; `driver = "memory"` is a broker in the process, for tests.
  Second part of phase 2 of `docs/design-async-elt.md`.
- `metagente consume FILE.ag --from BROKER --subject SUBJECT --dead SUBJECT`, the other half of `tool broker`:
  it gives the events of a JetStream stream to an agent, one call for each, and tells the broker what came of
  it. An agent that replies confirms the event; `fail "…" retry` (or a tool that says the failure may pass: a
  database or a broker that cannot be reached, a full stream) asks for it again after a wait (10 s, 1 min,
  5 min, 15 min, or what `retry in N seconds` suggested), up to 5 deliveries; any other failure, an event the
  agent does not accept, or the last delivery, is a dead letter with the reason in its headers. Several events
  at a time (`--in-flight`), a heartbeat to the broker while the agent works, a circuit breaker when the
  failures that may pass come one after the other, and a clean stop that gives back what it holds. What it
  reaches is approved with `metagente trust FILE.ag --from … --subject … --dead …`. Failures of a database
  (a connection that dropped, a deadlock, a server that is starting) now carry the mark of `retry` too.
  This closes phase 2 of `docs/design-async-elt.md`.
- `tool … from sql` reads PostgreSQL (`driver = "postgres"`), MySQL and MariaDB (`"mysql"`, `"mariadb"`) too.
  A network database is written with `host`, `port`, `database`, `user`, `tls` (`verify`, `require`,
  `disable`) and `ca_file`; the password comes from `[credentials]`. Every statement runs in a read only
  transaction, and the host, the port, the user and the TLS mode are part of what `metagente trust`
  approves. The drivers (pgx and go-sql-driver/mysql, both written in Go) can be left out of a build with
  `-tags nopostgres` and `-tags nomysql`.
- `integration/`, a module of its own with the tests that start PostgreSQL, MariaDB and MySQL in
  containers (Testcontainers, images pinned by digest) and run the compiled program against them; `make
  integration`, and a job of the CI.

## [0.5.0] - 2026-10-07

### Added

- `tool http` can go through the web proxy of a company, named in a new `[network]` section of
  `metagente.toml` (`http_proxy`). Its user and password come from the variable that
  `http_proxy_auth_env` names, as `user:password`, never from the file; agents and tool servers cannot
  read that variable, and it never reaches an error or the log. Through the proxy the guard still refuses
  internal addresses written as numbers, `localhost`, and names this computer finds at an internal
  address. A proxy from the environment (`HTTPS_PROXY`) is still never used.

## [0.4.3] - 2026-10-06

### Security

- Built with Go 1.26.8 (`toolchain go1.26.8` in `go.mod`), not 1.26.0. The program reached 20 known
  vulnerabilities of the standard library of 1.26.0, fixed in its later patches: among them `crypto/tls`,
  `crypto/x509` (the certificates of `--public`), `net/http` and `net/url` (the server and the `http`
  tool). `govulncheck` now runs in the CI.
- `samples/city-briefing/two-computers.sh` and its guide download the installer of `uv` over HTTPS only,
  also after a redirect (`curl --proto '=https' --tlsv1.2`): the script goes straight to `sh`.

## [0.4.2] - 2026-10-06

### Fixed

- On Windows, the programs that a tool server started by `npx` or `uvx` runs are ended when Metagente
  ends, however it ends: the process puts itself in a job object when it starts (E4). They were left
  running before.
- A token file caught in the middle of a change (half written, renamed over, or on Windows held for a
  moment while it is removed) no longer gives a `Problem:` line: a problem is told only once it has
  lasted, at least half a second and two looks.

## [0.4.1] - 2026-10-06

### Added

- `go install github.com/Ribeiro/metagente-go/cmd/metagente@latest` works: the module is now
  `github.com/Ribeiro/metagente-go`, and a program installed that way from a release says its version.

## [0.4.0] - 2026-10-06

### Added

- `--token-file` may hold a token for each client, a line `name token` each (`metagente token --name mac`
  writes one). Each is taken away without touching the others, and the access log says which client called
  (`client=mac`). A file with one token alone works as before.
- `serve` reads the token file again when it changes, without a restart: a token taken out of it stops
  opening the server for the next request. A file that is not good enough changes nothing, and the log says
  why.

## [0.3.2] - 2026-10-06

### Added

- With `--public` on a port that is not 443, the banner says which names of `--host` have no port, and
  what to write: a client that connects to that port sends it in `Host`, and was answered 421 without a
  word.

### Fixed

- On Windows, the file tool refuses to write to a file that has more than one name on disk (a hard link),
  as it already did on Linux and macOS: such a name could change a file outside the folder of the agent.
- A request with no token at all is refused with 401 but no longer counts as a wrong try. A client of MCP
  that looks for OAuth before it connects (the `mcp-remote` bridge of Claude Desktop sends six such requests
  each time it starts) stopped itself for a minute with 429, because on this computer every client is the
  same place. A wrong token, or a header of another scheme, still counts.

## [0.3.1] - 2026-10-06

### Fixed

- A key of a model with a line break, a space or another character that cannot be part of a key (more than
  the key copied into the variable) is refused before any request, with a message that says so. It used to
  be three tries and "I could not reach api.anthropic.com: the connection failed".

## [0.3.0] - 2026-10-05

### Added

- [The City Briefing sample](samples/city-briefing/): two agents that work together over A2A and MCP, with
  `think`. The tests run it offline at every change.
- The tests of the original project that were left are ported: the contract of A2A 1.0, the client of A2A,
  `serve` on this computer and with many requests at once, the times of start and of parsing, and the
  sample. The official A2A client in Python was tried against the server (check 9 of `validation/README.md`).

### Changed

- A message that lacks a value its skill takes, or carries one it does not, is refused with -32602 (a
  request that is not valid), before the agent hears of it. It used to be a task that failed.
- The errors of the A2A server carry the `ErrorInfo` of A2A 1.0 (the reason and the domain of the
  protocol), which the clients of the SDKs read to tell one error from another.
- A request that asks for a version of A2A that is not 1.x, in `A2A-Version`, is refused with -32009. A
  request that names none is served as before.
- Every skill of a card has tags, as A2A 1.0 asks: its name.
- With the token, a path that is not one of the server is a 404 whatever the method; a `GET` used to get a
  405.
- When the card of a remote agent is refused with 401 or 403, the message says what to do about the token
  (the variable named in `[credentials]`), not about the address.

## [0.2.0] - 2026-10-05

### Added

- `metagente serve --mcp`: the agents as MCP tools over HTTP too, at `/mcp`, behind the same door and the
  same token as A2A (S10). A session is a conversation with each agent; it ends when the client says so or
  after `task_retention_seconds` without use, and what its agents kept is let go. The answer comes in the
  answer to the POST, as JSON; a stream of the server (`GET`) is refused with `405`. There are no more
  sessions at once than `max_retained_tasks` (one more is a `503`).
- The access log says which agent, message and task an MCP call ran, and how it ended (`rpc=mcp`).
- `metagente serve --token-file FILE`: the token from a file instead of `METAGENTE_TOKEN`. On Linux and macOS
  a file that others may change is refused.
- [The reference of the language](docs/LANGUAGE.md), for people who write agents. It is in the archives too.
- A tag like `v0.2.0` publishes the release on GitHub by itself, with the archives, `SHA256SUMS` and the notes
  of that version from this file.
- `readonly` for tool servers: `tool x from mcp "..." readonly` offers, to the agent and to `think`, only the
  actions that the server marks as read only, and refuses the others. The clauses `env` and `readonly` may
  come in any order.
- `max_connections_per_address` in `[serve]` (32 by default): with `--public`, one place holds at most that many
  connections, and one more is closed at once.
- The chain of agents crosses MCP too: a call to a tool server that says it is Metagente carries the agents
  that are running, and that server refuses a call that goes too deep or in a circle, as over A2A (D2). No
  other tool server is told their names.

### Changed

- A2A and MCP share `max_running_tasks` and `max_retained_tasks`: they are limits of the server, not of
  each protocol.
- `metagente.toml` is read by a TOML library: anything TOML allows is read (lists over several lines, keys in
  quotes, every kind of text). The messages about a file that is not TOML are the ones of the library, with
  the line, except in `[credentials]`, where neither the line nor those words are shown.
- `max_state_bytes` is 256 KiB by default, not 1 MiB: with 1000 conversations, about 250 MiB at most.
- **A token needs at least 12 different characters.** A token that repeats a few (`kkkk...`) was accepted
  and is now refused, with a message that says how to make a good one (`metagente token`).

### Security

From [a review of the security of `serve`](docs/SECURITY-REVIEW.md):

- A place that is stopped for sending wrong tokens stays stopped for its minute. Before, it could get out by
  failing from a thousand other addresses.
- The server closes a connection that it turned away (401, 403, 421, 429), so a stranger cannot keep its
  connections open and idle.
- The addresses of one IPv6 network of 64 bits count as one place, for the wrong tokens and for the limit of
  connections: they all belong to whoever has the network.

## [0.1.0] - 2026-10-05

The first version: the interpreter of MetaAgent, written again in Go, for the agent files (`.ag`) that speak
MCP and A2A, with the security requirements of its hardening specification.

### Added

- The language and its commands: `check`, `new`, `run`, `trust`, `serve` and `token`.
- `link`, for agents that call agents of the same computer, and `remote`, for agents that run somewhere
  else (an A2A client).
- The tools `file`, `http`, `env`, `state` and `clock`, and tool servers over MCP, either programs or
  addresses, through the official Go SDK.
- `think`, with Anthropic and with any server that speaks the chat format of OpenAI (`openai-compatible`).
- `metagente serve`: the agents over A2A (JSON-RPC), behind a token, on this computer, with TLS of its own or
  behind a proxy; and `metagente serve --stdio`: the same agents as MCP tools.
- The approval of what starts a program or reaches an address (`trust`), bearer credentials
  (`[credentials]`), and a log of the failures inside Metagente.

### Security

Of the 47 requirements of the specification, 40 are done as written, 5 were changed on purpose and agreed,
and 2 are partial (see Known limits). The sources of the program, the tests and the record of what the
language does are the proof; the table is in the README.

Texts that come from outside (the answer of a model, of a tool server or of another agent) are cut, and
cleaned of control characters, before a message repeats them.

### Found by running it against programs of other people

The tests use doubles, so the program was also tried against software that others wrote, and what it
showed was fixed (the steps and the results are in `validation/README.md`):

- A model on this computer that needs no key (Ollama, for example) is approved with a sentence that says
  that no key is sent; it used to say that the key is sent.
- Behind a proxy, the banner says where the server listens, and the access log has the `Host` of each
  request, so a request that went around the proxy can be told from the others.
- A request with only `Sec-Fetch-Mode`, which is what the fetch of Node.js sends in every request, is no
  longer taken for a page in a browser. The official A2A client in JavaScript was refused with `403`.
- `remote` sends text to an agent whose card says that it takes only text. It used to send a block of
  data, and such an agent answered that it had received no message.
- `remote` asks the other side to answer at once, so that a task is known, and cancelled on the other
  side when the caller gives up. It used to wait in one request, and the task went on to the end for
  nothing.

### Known limits

- MCP over HTTP behind the same door as A2A (S10) is not done.
- On Windows the program builds and passes the tests that apply, and the protection against symbolic
  links is checked there, but the children of a tool server are not ended with it (E4), hard links are not
  detected, and the permissions of Unix have no check. Under WSL2 it works as on Linux.
- A model of small size may write a call to a tool as text and invent the result; the program cannot
  tell, and gives that text as the answer.
- The tools of an agent have no declared types for their values, so a client that shows a form for them
  shows a JSON editor.
- Not tried against the real thing yet: a desktop assistant as a client of `serve --stdio`, Azure as a
  provider, tool servers started with `uvx`, and `--public` with a certificate of its own.

[Unreleased]: https://github.com/Ribeiro/metagente-go/compare/v0.5.0...HEAD
[0.5.0]: https://github.com/Ribeiro/metagente-go/compare/v0.4.3...v0.5.0
[0.4.3]: https://github.com/Ribeiro/metagente-go/compare/v0.4.2...v0.4.3
[0.4.2]: https://github.com/Ribeiro/metagente-go/compare/v0.4.1...v0.4.2
[0.4.1]: https://github.com/Ribeiro/metagente-go/compare/v0.4.0...v0.4.1
[0.4.0]: https://github.com/Ribeiro/metagente-go/compare/v0.3.2...v0.4.0
[0.3.2]: https://github.com/Ribeiro/metagente-go/compare/v0.3.1...v0.3.2
[0.3.1]: https://github.com/Ribeiro/metagente-go/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/Ribeiro/metagente-go/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/Ribeiro/metagente-go/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/Ribeiro/metagente-go/releases/tag/v0.1.0
