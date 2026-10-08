# The language of Metagente

This is the reference for people who write agents: what a `.ag` file may hold, what each line means,
and what the program checks before it runs anything. Everything here was taken from the lexer, the
parser, the checks and the interpreter (`internal/lang`, `internal/runtime`, `internal/tools`), and
every example in this page passes `metagente check` (a test reads the whole agents of this page and
of [the tutorial](tutorial.md) and checks them).

How to run, serve and configure agents is in the [README](../README.md); the commands are listed in
[Commands](#commands). New to Metagente? Start with [your first agent in 15 minutes](tutorial.md).

Contents: [Index](#index), [A first agent](#a-first-agent), [The shape of a file](#the-shape-of-a-file),
[The lines of an agent](#the-lines-of-an-agent), [The lines of a section](#the-lines-of-a-section),
[Values](#values), [Calls](#calls), [Conditions](#conditions), [think](#think),
[What check looks at](#what-check-looks-at), [Limits](#limits),
[Words the language keeps](#words-the-language-keeps), [A larger example](#a-larger-example),
[Commands](#commands), [Grammar](#grammar).

## Index

Every word and call of the language, in alphabetical order, with where it is explained.

| Word | What it is | Where |
|---|---|---|
| `accepts` | a message the agent understands, and its values | [accepts](#accepts) |
| `agent` | begins an agent | [The shape of a file](#the-shape-of-a-file) |
| `allow` | the domains `tool http` may reach, or `allow private` | [tool](#tool) |
| `and` | both are true | [Conditions](#conditions) |
| `at` | the address of a `remote` | [remote](#remote) |
| `broker` | a message broker: `tool name from broker "connection" publish "subject"` | [A message broker](#a-message-broker-tool-from-broker) |
| `clock.now`, `clock.wait` | the time, and waiting | [tool](#tool) |
| `contains` | a text has a piece, or a list has an item | [Conditions](#conditions) |
| `env` | `tool env "NAME"`, or the variables given to a tool server | [tool](#tool) |
| `env.get` | reads a variable that `tool env` names | [tool](#tool) |
| `fail` | ends the section with a failure; `fail "..." retry [in N seconds]` says that it may pass | [fail](#fail) |
| `retry` | `fail "..." retry`: the failure may pass | [fail](#fail) |
| `file.read`, `file.write` | reads and writes texts | [tool](#tool) |
| `for` ... `in` | repeats lines for each item of a list | [for](#for) |
| `from` | `link Name from "file.ag"`, `tool name from mcp "..."`, `tool name from sql "..."` | [link](#link), [tool](#tool) |
| `goal` | what the agent is for | [goal](#goal) |
| `http.get`, `http.post` | web requests | [tool](#tool) |
| `if`, `otherwise` | choose | [if and otherwise](#if-and-otherwise) |
| `is`, `is not`, `is more than`, `is less than` | compare | [Conditions](#conditions) |
| `link` | another agent of this computer | [link](#link) |
| `mcp` | a tool server: `tool name from mcp "..."` | [tool](#tool) |
| `name = value` | keeps a value | [Keeping a value](#keeping-a-value) |
| `not` | the opposite | [Conditions](#conditions) |
| `nothing` | the absence of a value | [Values](#values) |
| `on`, `on start` | the lines to run for a message, or once at the start | [on](#on) |
| `or` | at least one is true | [Conditions](#conditions) |
| `private` | `tool http allow private` | [tool](#tool) |
| `publish` | `tool name from broker "..." publish "subject"`, and `name.publish` | [A message broker](#a-message-broker-tool-from-broker) |
| `readonly` | only the actions that change nothing | [tool](#tool) |
| `remote` | an agent served somewhere else (A2A) | [remote](#remote) |
| `repeat while` | repeats lines while a condition is true | [repeat](#repeat) |
| `reply` | ends the section with the answer | [reply](#reply) |
| `sql` | a database: `tool name from sql "connection"` | [A database](#a-database-tool-from-sql) |
| `state.get`, `state.set` | the memory of a conversation | [tool](#tool) |
| `target.action key: value` | a call to a tool, a `link` or a `remote` | [Calls](#calls) |
| `think`, `using` | asks a language model | [think](#think) |
| `tool` | a tool the agent may use | [tool](#tool) |
| `up to N times` | the most turns of a `repeat`, chosen by the author | [repeat](#repeat) |
| `within N seconds` | the time a call may take | [Calls](#calls) |
| `yes`, `no` | true and false | [Values](#values) |
| `{name}` | a value inside a text | [Values](#values) |
| `#` | a comment, or the description of a message | [The shape of a file](#the-shape-of-a-file) |

## A first agent

```text
agent Greeter
  goal "Say hello to someone"
  accepts greet name
  on greet
    reply "Hello, {name}!"
```

```text
metagente check greeter.ag
metagente run greeter.ag greet name=Maria          # Hello, Maria!
```

An agent has a name, a goal, the messages it accepts, and for each message a section of lines that
says what to do with it. It may also declare the tools it uses, the agents it calls on this computer
(`link`) and the agents it calls somewhere else (`remote`).

## The shape of a file

- A file holds one or more agents. Each begins with `agent Name` at the start of a line.
- **Indentation is two spaces for each level**, never a tab. The lines of an agent are indented once;
  the lines of an `on` section twice; the lines under an `if` or a `for` one more each time. Up to 32
  levels.
- `#` begins a comment, to the end of the line. On an `accepts` line the comment is the description
  of the message (see [accepts](#accepts)).
- Blank lines do not count.
- A file may have up to 1 MiB, a line up to 64 KiB.

Names (of agents, messages, values, tools) are made of letters, digits, `_` and `-`, and begin with a
letter or `_`. Upper and lower case are different names.

## The lines of an agent

They may come in any order. Each name (of a tool, a `link` or a `remote`) may be declared once.

### goal

```text
goal "Answer questions about the weather"
```

What the agent is for, in one fixed text (no `{names}` in it). Every agent needs one. It is shown to
a language model by `think`, written in the card of the agent when it is served, and in the
description of its tools over MCP.

### accepts

```text
accepts ask city            # The forecast of a city
accepts forecast city days
accepts ping
```

A message the agent accepts, and the names of the values it takes. A message is called with exactly
those values: none missing and none other (`run`, `serve` and a `link` all check that before the
agent runs). The comment at the end of the line describes the message to whoever calls it: a
language model, a client of A2A, a client of MCP.

Every `accepts` needs its `on` section, and every `on` section its `accepts`, except `on start`.

### on

```text
on ask
  forecast = weather.forecast city: city
  reply "In {city}: {forecast.summary}"
```

The lines to run when the message arrives. The values of the message are names that can be used in
them (`city` above). A section must have at least one line.

`on start` is special: it runs once, before the first message of a conversation, with no values. A
conversation is one `metagente run`, or one conversation of `serve` (see the README). It is the place
to set up what the agent keeps with `state`.

### tool

The tools an agent may use. An agent can only call what it declared.

| Line | What it gives |
|---|---|
| `tool file` | read and write texts in the project folder |
| `tool file "data/"` | the same, only inside that folder of the project |
| `tool file readonly` | only `read` |
| `tool http` | fetch from the web: any public address |
| `tool http allow "api.example.com" "*.example.org"` | only those domains (`*.` is any subdomain, not the domain itself) |
| `tool http allow private` | also this computer and private networks; link-local addresses (where clouds keep their metadata) stay refused |
| `tool http readonly` | only `get` |
| `tool env "HOME" "LANG"` | read those environment variables, and no other |
| `tool state` | remember values within a conversation |
| `tool clock` | the time, and waiting |
| `tool weather from mcp "npx -y weather-mcp@1.2.0"` | a tool server (MCP) started by that command |
| `tool search from mcp "https://mcp.example.com/mcp"` | a tool server at that address |
| `tool weather from mcp "..." env "HTTPS_PROXY"` | give the program these variables too |
| `tool weather from mcp "..." readonly` | only the actions that the server marks as read only |
| `tool events from broker "main" publish "etl.>"` | publish to a message broker, only to those subjects (see [A message broker](#a-message-broker-tool-from-broker)) |
| `tool orders from sql "orders-db"` | read a database, with the statements that `[sql.orders-db]` of `metagente.toml` names (see [A database](#a-database-tool-from-sql)) |

The clauses of `http` (`allow`, `readonly`) and of a tool server (`env`, `readonly`) may come in any
order and more than once.

`http` never uses a proxy from the environment (`HTTPS_PROXY`). Where the web is only reached through
the proxy of a company, the person who runs the agents names it in `metagente.toml`, not the agent:

```toml
[network]
http_proxy = "http://proxy.example.com:3128"
http_proxy_auth_env = "PROXY_AUTH"   # only if the proxy asks for a user and password
```

The user and password go in the variable that `http_proxy_auth_env` names, as `user:password`
(`export PROXY_AUTH='ana:...'`), never in the file, and no agent or tool server can read that variable.
Through the proxy the guard still refuses an address written as numbers that is internal, `localhost`,
and a name that this computer finds at an internal address (`allow private` lets private networks
through, never link-local addresses). A name this computer cannot find is left to the proxy, since in
many companies only the proxy finds the names of the internet; what the proxy itself may reach is then
its own rule. A proxy that refuses, asks for a password, or does not answer is told as such.

`readonly` on a tool server offers, to the agent and to `think`, only the actions that the server marks
as read only (`readOnlyHint` in MCP); calling another one is a problem that says why. That mark is the
word of the server: it keeps a model or a mistake from using an action that changes things, not a
server that lies about its own actions.

A tool server started by a command gets a minimal environment (`PATH`, `HOME` and a few more) plus
the variables named with `env`; a variable that holds a secret (the key of the model, the token of
the server, a credential) is refused even when named. `check` warns when the command runs `npx`,
`uvx`, `pipx run` or `bunx` with a package that has no pinned version, and `check --strict` refuses
it. A tool server is started, or an address reached, only after the person approved it with
`metagente trust` (see the README).

### A database (`tool from sql`)

`tool orders from sql "orders-db"` lets the agent read a database. The agent never writes SQL: the
statements are written by the person who runs the agents, in `metagente.toml`, each with a name, and the
agent calls a statement as an action of the tool, with values for its parameters:

```toml
[sql.orders-db]
driver = "sqlite"
path = "data/orders.db"            # relative to the folder of the project

[sql.orders-db.statements]
next_page = "SELECT id, customer, total FROM orders WHERE id > :after ORDER BY id LIMIT :size"
by_id     = { sql = "SELECT id, customer FROM orders WHERE id = :id", result = "row" }
newest    = { sql = "SELECT max(id) FROM orders", result = "value", description = "the last id" }
```

```
agent Pager
  goal "Read the orders in pages"
  tool orders from sql "orders-db"
  accepts go start
  on go
    after = 0
    repeat while after is not nothing
      page = orders.next_page after: after size: 1000
      after = nothing
      for row in page
        after = row.id
    reply "done"
```

- A `:name` in a statement is a parameter. It is sent to the database apart from the text, never pasted
  into it, so a value cannot change the statement. A call must give every parameter and no other.
- `result` says what comes back: `rows` (the default) is a list with a record for each row; `row` is the
  first row as a record, or `nothing` when there is none; `value` is the one value of a statement with one
  column, or `nothing`. `row` and `value` are a problem when the statement gives more than one row, so a
  statement that was meant to give one does not silently give the wrong one.
- Only `SELECT` and `WITH`, one statement at a time. The database is opened read only, and the engine
  is told so as well: SQLite is opened with `mode=ro` and `query_only`; PostgreSQL, MySQL and MariaDB
  get every statement in a read only transaction, so even a `SELECT` that calls a function that writes is
  refused by the server. Still, give the user of the connection only the right to read: that is the
  protection that does not depend on this program.
- The columns become the fields of the records, so they need names that are valid fields and are not
  repeated (`AS` gives one). A number beyond 2^53 comes back as text, so it keeps every digit; a blob
  must be text in UTF-8; a time comes as text in RFC 3339.
- A secret does not go in the file: `[credentials]` names the variable that holds it, by the name of the
  tool (`orders = "ORDERS_DB"`). For SQLite the value replaces `path`; for PostgreSQL, MySQL and MariaDB
  it is the password. A problem never shows it, nor the values of a call.
- The database, the connection, and the text of every statement are approved with `metagente trust`
  before the first connection; changing a statement asks again.
- The most rows and bytes of an answer are `max_sql_rows` (10000) and `max_sql_bytes` (5 MiB) in
  `[limits]`; an answer that passes either is a problem, not a cut answer. Use `LIMIT` and a `repeat`.
- `driver` is `sqlite`, `postgres`, `mysql` or `mariadb`. A build made with `-tags nosqlite`,
  `nopostgres` or `nomysql` (which also leaves out MariaDB) leaves the driver out; the releases do not.

A database reached over the network says where it is, and who reads it, instead of a `path`:

```toml
[credentials]
orders = "ORDERS_DB_PASSWORD"          # the password, from the environment

[sql.orders-db]
driver   = "postgres"                  # or "mysql" or "mariadb"
host     = "db.example.com"            # a name or an address, with no port
port     = 5432                        # optional: 5432 for postgres, 3306 for mysql and mariadb
database = "orders"
user     = "reader"
tls      = "verify"                    # optional: "verify" (the default), "require" or "disable"
ca_file  = "certs/ca.pem"              # optional: the certificates to trust, from the folder of the project

[sql.orders-db.statements]
next_page = "SELECT id, customer FROM orders WHERE id > :after ORDER BY id LIMIT :size"
```

- `tls = "verify"` encrypts and checks the certificate and the name of the server (with `ca_file` for the
  certificates of a company, or else those of the system). `require` encrypts and does not check;
  `disable` sends everything in the clear, which only belongs on a private network that you trust.
- Host, port, database, user and TLS are what `metagente trust` shows and approves, with the statements.
- `result = "value"` and the like work the same in every database. What the server sends as a decimal
  (`numeric`, `DECIMAL`), as a UUID or as JSON comes as text; a `datetime` as text in RFC 3339 in UTC.

### Changing a database (`mode = "write"`)

A connection only reads, unless its section says `mode = "write"`. Then its statements may also begin
with `INSERT`, `UPDATE` or `DELETE`, and they give how many rows they changed (`result = "count"`, the
only answer they have). It is meant for the side of a pipeline that lands data in a database that the
person who runs the agents owns. The agent still never writes SQL, and the file never holds a secret.

```toml
[sql.warehouse]
driver   = "postgres"
host     = "db.example.com"
database = "warehouse"
user     = "loader"
mode     = "write"

[sql.warehouse.statements]
# runs once for each item of the list "rows", all in one transaction; an item gives id and name
land   = { sql = "INSERT INTO stg (job, seq, id, name) VALUES (:job, :seq, :id, :name)", each = "rows", columns = ["id", "name"] }
mark   = "INSERT INTO batches (job, seq, state) VALUES (:job, :seq, 'landed')"
finish = "UPDATE batches SET state = 'done' WHERE job = :job AND seq = :seq"
purge  = "DELETE FROM stg WHERE job = :job"
seen   = { sql = "SELECT state FROM batches WHERE job = :job AND seq = :seq", result = "value" }

# statements that run as one: if one fails, none of them changed anything
[sql.warehouse.transactions]
land_batch = ["land", "mark"]
```

```
agent Lander
  goal "Land a batch"
  tool warehouse from sql "warehouse"
  accepts land job seq rows
  on land
    state = warehouse.seen job: job seq: seq
    if state is "done"
      reply "already done"
    done = warehouse.land_batch job: job seq: seq rows: rows
    warehouse.finish job: job seq: seq
    reply "landed {done.land} rows"
```

- `each` and `columns` go together. `each` names a value of the call that holds a list; the statement runs
  once for every item, inside one transaction. `columns` are the parameters that come from the item (a
  record with those names as fields, or a list with the values in that order); the other parameters are
  given once and are the same for every item. A list has at most `max_sql_write_rows` items (10000).
- A call of a statement that changes rows is a transaction of its own: if the list stops halfway, nothing
  stays. `[sql.NAME.transactions]` groups several of them so that they stand or fall together; a call of
  one gives a record with how many rows each statement changed. A name that two steps share is one value.
- An `UPDATE` or a `DELETE` without a `WHERE` is refused, so that a slip cannot change every row of a
  table. `CREATE`, `DROP`, `ALTER` and the like are refused too: Metagente does not change the shape of a
  database, the tables are made by the migrations of the user. Give the user of the connection only the
  rights that the statements need: that is the protection that does not depend on this program.
- A statement that only reads still runs in a read only transaction, on a connection that writes.
- What the driver says is cleaned of what is between quotes before a problem shows it (a key that
  already exists, a text that is not a number), and a problem in a list says the place of the item,
  never its content: the rows may hold personal data. A failure that may pass (a connection that
  dropped, a deadlock) is marked, as for a read.
- `metagente trust` shows a connection that writes as one that **changes** the database, with the names
  of the statements and of the transactions; it is another approval than the one for reading the same
  database.

### A message broker (`tool from broker`)

`tool events from broker "main" publish "etl.orders.batch"` lets the agent publish messages to a message
broker (JetStream, a NATS server with persistence). The broker is described in `metagente.toml`, in a
`[broker.main]` section, and the agent can publish **only to the subjects it wrote after `publish`**, as
`tool http allow` does for sites. A `*` stands for any one name of a subject and a `>` at the end for all
the names that follow: `"etl.*.batch"`, `"etl.>"`.

```toml
[credentials]
events = "BROKER_PASSWORD"            # the password (or the token, with no user), from the environment

[broker.main]
driver  = "jetstream"                 # or "memory", see below
url     = "tls://broker.example.com:4222"
user    = "etl"                       # optional; with no user, the secret is a token
tls     = "verify"                    # optional: "verify" (the default), "require" or "disable"
ca_file = "certs/ca.pem"              # optional: the certificates to trust, from the folder of the project
stream  = "ETL"                       # optional: the stream that has to take what is published
```

```
agent Sender
  goal "Send the batches"
  tool events from broker "main" publish "etl.orders.batch" "etl.orders.control"
  accepts go start
  on go
    for n in [1, 2, 3]
      events.publish subject: "etl.orders.batch" id: "job7:{n}" data: [n, "x"]
    sent = events.publish subject: "etl.orders.control" id: "job7:end" data: "done"
    reply "the last message is number {sent.seq} of the stream"
```

- `events.publish subject: ... id: ... data: ...` waits until the broker says that it keeps the message,
  and gives a record with `stream`, `seq` and `duplicate`. `data` is a text, which goes as it is, or any
  other value, which goes as JSON.
- **Every message needs an `id`**, and the same message has to have the same id every time it is sent (for
  example `job:number`). The broker drops a copy of an id it has seen lately, and says `duplicate: yes`: a
  server answers "at least once", and this is how the effect happens once. A JetStream stream remembers the
  ids for its window of duplicates (2 minutes unless the stream says otherwise), so make the window longer
  than the time between a try and the next.
- `check` refuses a subject written in the call that the tool did not declare; a subject built from values
  is checked again when the agent runs.
- A message may have `max_broker_bytes` (1 MiB) in `[limits]`; a larger one is a problem, and nothing is
  sent. The server has a limit of its own too (1 MiB unless it is changed). Cut the batches by their size.
- Metagente does not create streams: they belong to whoever runs the server. A subject that no stream takes
  is a final failure. A **full stream** (the stream refuses the new messages, so nothing that was not
  processed is thrown away) and a broker that **cannot be reached** are failures that may pass: they carry
  the mark of `fail ... retry`, so `metagente consume` and any caller can ask again later.
- The password never appears in a problem. `tls = "disable"` sends everything in the clear, and belongs
  only on a computer you trust (`localhost`). Where the broker is, the user, the TLS mode, the stream and
  the subjects are what `metagente trust` shows and approves; changing any of them asks again.
- `driver = "memory"` is a broker in the memory of the process, with no url: it serves the tests, and the
  trying of agents without a server (what is published stays in the memory of the process). A build made with
  `-tags nojetstream` leaves JetStream out (the releases do not).

The actions of the built in tools:

| Call | Values | What comes back |
|---|---|---|
| `file.read` | `path` | the text of the file |
| `file.write` | `path`, `text` | the file is written whole, through a temporary file |
| `http.get` | `url` | a record: `status` (a number), `text`, `json` (the answer read as JSON, or `nothing`) |
| `http.post` | `url`, `body` (optional) | the same record. A text body is sent as it is; a record or a list as JSON |
| `env.get` | `name` | the text of the variable, or `nothing` |
| `state.set` | `key`, `value` | remembers the value for the rest of the conversation |
| `state.get` | `key` | the value, or `nothing` if it was never set |
| `clock.now` | none | a record: `text` (the time in UTC, RFC 3339) and `unix` (seconds) |
| `clock.wait` | `seconds` | waits, up to `max_wait_seconds` |

The actions of a tool server are the ones the server offers, with the values it asks for.

Paths of `file` are relative to the folder of the tool: `..` or a link that leads out of it is
refused, and so are names that do not work everywhere (`CON`, `a:b`, a name that ends in a dot or a
space). `http` never reaches this computer or a private network, also after a redirect, unless
`allow private` says so.

### link

```text
link Weather
link Weather from "agents/weather.ag"
```

Another agent of this computer, called like a tool: `Weather.ask city: "Lisbon"`. Without `from`, it
is looked for in the same file, then next to this file, then in `agents/`. It is read again at each
call, so a change to it applies without restarting the caller. `check` refuses a link to an agent
that is not found, and a call with a message or values that the agent does not accept. A `from` that
leaves the project is warned about (refused with `--strict`). A circle of agents calling each other
is stopped, and so is a chain deeper than `max_call_depth`. That holds across processes too: over A2A
(`remote`), and over MCP when the tool server is another Metagente (`metagente serve --stdio` or
`--mcp`), which is the only kind of tool server that is told the names of the agents running.

### remote

```text
remote Bob at "https://agents.example.com/agents/Weather"
```

An agent that runs somewhere else and speaks A2A, called like a tool: `Bob.ask city: "Lisbon"`. Its
actions are the skills that its card lists. The token that goes with the calls is named in
`[credentials]` of `metagente.toml`, never in the agent file. Like a tool server, the address is used
only after it was approved.

## The lines of a section

### Keeping a value

```text
total = 0
forecast = weather.forecast city: city
```

`name = value`. The name can be used in the lines that follow, in the same section. Giving it a value
again replaces the one it had.

### reply

```text
reply "In {city} it will be {forecast.summary}"
```

Ends the section and answers with the value. A section that ends without `reply` answers `nothing`.

### fail

```text
fail "I need a city"
```

Ends the section with a failure, whose message is the value. The caller sees it as a problem, and a
failure like this is **final**: whoever called is not told to try again.

When the failure may pass (the destination is busy, a service is down for a moment), say so with `retry`,
and, if you know it, how long to wait:

```text
fail "The destination is busy" retry
fail "The destination is busy" retry in 60 seconds
```

The wait is only a suggestion, a number of seconds (`second` is accepted after 1), and no more than 3600 is
suggested; `check` warns about a wait that is not above 0 or that is longer. `metagente run` prints a note
under the problem ("This may pass: it can be tried again in 60 seconds."); a linked agent that fails this
way makes the failure of its caller one that may pass too; over A2A the task that failed carries
`metadata.metagente.retry` and `retryAfterSeconds`, so a caller that is a program can ask again, and
`metagente consume` asks the broker to deliver the event again after the wait. `retry` is a word only
right after the value of a `fail`; anywhere else it is a name like any other.

### if and otherwise

```text
if forecast.rain is more than 50
  reply "Take an umbrella"
otherwise
  reply "No umbrella today"
```

`otherwise` is optional, comes right after the lines of its `if`, at the same indentation, and has
no condition: to choose between more than two ways, put an `if` under the `otherwise`.

What counts as true: `yes`, a number that is not 0, a text that is not empty, a list or a record that
is not empty. `no`, `0`, `""`, `[]`, an empty record and `nothing` are false.

### for

```text
for city in cities
  forecast = weather.forecast city: city
  state.set key: city value: forecast.summary
```

Runs the lines once for each item of a list, with the item in the name (`city`). A `reply` inside
ends the whole section. Anything that is not a list is a problem.

### repeat

```text
agent Pager
  goal "Read every page of a list on the web"
  tool http
  accepts all first
  on all
    url = first
    repeat while url is not nothing
      page = http.get url: url
      url = page.json.next
    reply "done"
```

Runs the lines under it again and again while the condition is true. The condition is the same as
that of `if`, and it is looked at before each turn, so when it is false at the start the lines never run.
What changes the condition has to be among the lines: here `url` gets the address of the next page, and
the last page says `null`, which is `nothing` and ends the loop. As with `for`, a `reply` inside ends the
whole section, and a name given a value inside is known after the loop.

A loop cannot go on for ever:

- `repeat while condition up to 100 times` is a cap that you choose. After 100 turns the loop ends, even
  if the condition is still true, and the lines after it run. Use it when "enough tries" is a normal way
  to end, for example asking again up to 3 times. The number is a whole number from 1 to 1000000000
  (`up to 1 time` is also accepted).
- Without it, or with a number above what the setup allows, the loop ends with a problem if the condition
  is still true after `max_loop_turns` turns (10000 by default, in `[runtime]` of `metagente.toml`). This
  is on purpose: a loop that goes on that long is almost always one whose condition nothing changes, and
  it is better that the problem says so than that the work is cut short in silence.

### A call on its own

```text
state.set key: "seen" value: yes
clock.wait seconds: 1
```

A call whose answer is not kept.

## Values

| Kind | Written as | Notes |
|---|---|---|
| text | `"Hello, {name}!"` | `{name}` and `{forecast.summary}` are filled in with values. `\n` is a line break, `\t` a tab, `\"` a quote, `\{` a brace, `\\` a backslash |
| number | `42`, `2.5` | up to 15 digits. No minus sign is written: a negative number can only come from a tool |
| yes and no | `yes`, `no` | |
| nothing | `nothing` | the absence of a value |
| list | `["Lisbon", "Porto", 3]` | of values that are written or names; up to 10000 items |
| record | (only from tools and agents) | has fields: `forecast.summary`, `answer.json.items` |

The values of a message that comes from `metagente run` are texts: `metagente run a.ag ask count=5`
gives the text `"5"`. Comparing it with a number still works (see below). A message from A2A or MCP
may carry any value that JSON has: texts, numbers, yes and no, lists and records.

A name that holds a record reads its fields with dots: `forecast.summary`, `result.city.name`. A field
that is not there is a problem, and the message names the ones that are.

## Calls

```text
forecast = weather.forecast city: city days: 3
answer = Weather.ask city: "Lisbon" within 60 seconds
time = clock.now
```

`target.action` and then `name: value` pairs, where the target is a tool, a `link` or a `remote`, and
the values are texts, numbers, `yes`, `no`, `nothing`, lists or names (a call cannot be a value of
another call: keep its answer in a name first). A call that takes no values is just `target.action`.

Each call may take `timeout_seconds` (30 by default, `[runtime]` in `metagente.toml`), or what
`within N seconds` says, and never more than `max_wait_seconds`. A call that runs out of time is a
problem that says so.

## Conditions

| Written as | True when |
|---|---|
| `a is b` | they are the same value. A text that reads as a number equals that number (`"5" is 5`) |
| `a is not b` | they are not |
| `a is more than b`, `a is less than b` | both are numbers (or texts that read as numbers), and the order says so |
| `a contains b` | `a` is a text that has `b` in it, or a list that has an item equal to `b` |
| `not x`, `x and y`, `x or y` | as usual. `and` and `or` do not look at the right side when the left one decides |

`not` binds closest, then `and`, then `or`. Parentheses are not part of the language: keep a part in a
name first.

```text
if count is more than 3 and not done
  reply "Many"
```

## think

```text
answer = think "Which of these cities is warmest today? {cities}"
answer = think "Plan the day in {city}" using weather clock
```

Asks a language model, which may use the tools of the agent to answer: every tool, `link` and `remote`
it declared, or only the ones after `using`. The answer is a text. The model is told the goal of the
agent, and what a tool returns is given to it as data, never as orders.

The model is chosen in `[llm]` of `metagente.toml` (Anthropic, or any server that speaks the chat
format of OpenAI); the agent file never names it. A question is limited in steps, tokens and time
(`think_max_steps`, `think_max_total_tokens`, `think_timeout_seconds`).

`check` warns about a `think` without `using` in an agent that has `file` without `readonly`, or
`http`: a model that reads something hostile could be led to write files or send data. Name only the
tools the question needs, or declare the tool `readonly`.

## What check looks at

`metagente check FILE.ag` reads the file and reports the first problem it finds, with its line, its
column and what to do. Before an agent runs, `run` and `serve` do the same checks. They refuse:

- an agent without `goal`; an `accepts` or an `on` that appears twice; an `accepts` without its `on`
  section, or an `on` without its `accepts` (except `on start`);
- a name of a tool, `link` or `remote` declared twice;
- a call to something the agent did not declare (`weather.forecast` without `tool weather ...`), or
  to an action that a built in tool does not have, or without a value that the action needs;
- a call to an action that changes things (`file.write`, `http.post`) on a tool declared `readonly`;
- a `using` that names something the agent did not declare, an `allow` that is not a domain, a tool
  server with an empty command;
- a `link` to an agent that is not found, a call to a message it does not accept, or with values it
  does not take.

And they warn, which `--strict` turns into refusals:

- a tool server started by `npx`, `uvx`, `pipx run` or `bunx` without a pinned version;
- a `link from` that leaves the project;
- a `think` without `using` in an agent with `file` (not `readonly`) or `http`.

Words that look like a mistake get a suggestion: `acepts` gets "did you mean `accepts`?".

## Limits

| What | Limit | Where it is set |
|---|---|---|
| a file, a line | 1 MiB, 64 KiB | fixed |
| indentation | 32 levels | fixed |
| a list written in the file | 10000 items | fixed |
| a number written in the file | 15 digits | fixed |
| a call | `timeout_seconds` (30), at most `max_wait_seconds` (3600) | `[runtime]` |
| agents calling agents | `max_call_depth` (8) | `[runtime]` |
| turns of a `repeat` | `max_loop_turns` (10000) | `[runtime]` |
| a file read or written | `max_file_bytes` (1 MiB) | `[limits]` |
| an answer of `http` | `max_http_bytes` (5 MiB) | `[limits]` |
| a message to a broker | `max_broker_bytes` (1 MiB) | `[limits]` |
| an answer of `sql` | `max_sql_rows` (10000), `max_sql_bytes` (5 MiB) | `[limits]` |
| a list given to a statement that changes rows | `max_sql_write_rows` (10000) | `[limits]` |
| what `state` keeps in a conversation | `max_state_entries` (1000), `max_state_bytes` (256 KiB) | `[limits]` |
| what a tool gives to `think` | `max_tool_result_bytes` (32 KiB), cut and marked | `[limits]` |

## Words the language keeps

`agent`, `goal`, `tool`, `link`, `remote`, `accepts`, `on`, `from`, `mcp`, `sql`, `broker`, `publish`, `env`, `at`, `allow`,
`private`, `readonly`, `reply`, `fail`, `if`, `otherwise`, `for`, `in`, `repeat`, `while`, `think`, `using`,
`within`, `seconds`, `is`, `not`, `more`, `less`, `than`, `contains`, `and`, `or`, `yes`, `no`, `nothing`.

`repeat` is a loop only when `while` comes right after it, `retry` has a meaning only right after the value
of a `fail`, and `up`, `to`, `times` and `time` have a
meaning only in `up to N times`; anywhere else they are names like any other.

## A larger example

```text
agent Trip
  goal "Help plan a short trip"
  tool weather from mcp "npx -y weather-mcp@1.2.0"
  tool state
  link Packer from "packer.ag"
  accepts plan city days          # Plan a trip to a city for some days
  accepts last                    # The last city that was planned
  on start
    state.set key: "last" value: nothing
  on plan
    if days is more than 14
      fail "I plan trips of two weeks at most"
    forecast = weather.forecast city: city days: days within 60 seconds
    state.set key: "last" value: city
    list = Packer.pack rain: forecast.rain
    reply "In {city}: {forecast.summary}. Take: {list}"
  on last
    city = state.get key: "last"
    if city is nothing
      reply "No trip yet"
    reply city
```

```text
agent Packer
  goal "Say what to pack"
  accepts pack rain
  on pack
    if rain is more than 50
      reply ["umbrella", "boots"]
    reply ["sunglasses"]
```

## Commands

| Command | What it does |
|---|---|
| `metagente new NAME` | creates `NAME.ag`, a starter agent, and a `metagente.toml` |
| `metagente check [--strict] FILE.ag` | looks for problems without running; `--strict` turns the warnings into problems |
| `metagente run FILE.ag [MESSAGE] [key=value ...]` | runs an agent. `--agent NAME` picks one of a file with several; `--config FILE` a `metagente.toml` |
| `metagente trust FILE.ag [--yes]` | approves the programs and addresses the agents of the file use (and the agents they link to) |
| `metagente trust --list`, `metagente trust --revoke` | shows what is approved; removes the approvals of this project |
| `metagente token [--name NAME]` | makes a token for a server; with `--name`, a line of a `--token-file` |
| `metagente serve FILE.ag ... [--port N]` | serves the agents over A2A on this computer, behind a token. `--mcp` serves them as MCP tools too, at `/mcp`; `--agent NAME` serves only some; `--public-card`, `--quiet`, `--config FILE` |
| `metagente serve FILE.ag ... --stdio` | the agents as MCP tools on standard input and output |
| `metagente serve ... --public --tls-cert FILE --tls-key FILE --host NAME` | open to the network, with TLS of its own; `--token-file FILE` for a token for each client |
| `metagente serve ... --behind-proxy --host NAME --public-url https://NAME` | behind a proxy on this computer, which does the TLS |
| `metagente consume FILE.ag --from BROKER --subject SUBJECT --dead SUBJECT` | gives the events of a stream to an agent, each as a call (see [Consuming the events of a stream](#consuming-the-events-of-a-stream-metagente-consume)); `trust` takes the same `--from`, `--subject` and `--dead` |
| `metagente --version` | the version |

```text
metagente new hello
metagente check --strict hello.ag
metagente run hello.ag greet name=World
metagente run team.ag ask city=Lisbon --agent Weather
metagente trust trip.ag
metagente serve weather.ag --port 8080 --mcp
metagente serve weather.ag --stdio
```

The exit code is 0 when it worked, 1 for a problem in the agent or the files, and 2 for a mistake in
how the command was written. Serving, tokens and TLS are explained in the [README](../README.md#serving-agents).

### Consuming the events of a stream (`metagente consume`)

`metagente consume` gives the events of a message broker to an agent, one call for each event, and tells the
broker what came of it. It is the other half of `tool broker`: an agent publishes, and another one, usually on
another computer, consumes.

```text
metagente consume worker.ag --from main --subject etl.orders.batch --dead etl.orders.dead --message batch
```

- `--from` is a `[broker.NAME]` of `metagente.toml`; `--subject` is a subject or a pattern to read; `--dead` is
  the subject where the events that are given up on are put. The stream has to exist and take both subjects.
  `--message` is the message the agent is sent (it can be left out when the agent accepts only one); `--agent`
  picks one of a file with several.
- **What an event becomes.** A JSON object gives one value for each of its fields: `{"n": 3, "text": "..."}`
  calls `on batch` with `n` and `text`. Anything else (a list, a text, a number) is the one value of a message
  that takes exactly one. The values are checked against `accepts` before the agent runs; an event that does
  not fit is a dead letter, and the agent never sees it. Each event is a conversation of its own, so `tool
  state` does not carry anything from one event to the next.
- **What comes of it:**

  | The agent... | The event is... |
  |---|---|
  | replies | confirmed, and not delivered again |
  | fails with `fail "..." retry`, or a tool says that the failure may pass (a broker, a database that cannot be reached, a full stream) | asked for again after a wait, up to `--max-deliver` deliveries (5); after the last one, a dead letter |
  | fails with `fail "..."` | a dead letter at once |
  | is stopped (Ctrl+C or SIGTERM) | given back to the broker at once, to be delivered to whoever reads next |

  The wait is the larger of what `retry in N seconds` suggested and `--backoff` (10 s, 1 min, 5 min, 15 min by
  default, by the delivery that failed; the last repeats).
- **Dead letters** are messages on the `--dead` subject with the content of the event, an id (`dead:` and the id
  of the event, so a copy is dropped) and the headers `Metagente-Dead-Reason`, `-Subject`, `-Event` and
  `-Attempts`. The reason is what the agent said, cut to 300 characters, never the content. If the dead
  letters cannot take the event, it is tried again a few times, and then left in the stream; nothing is thrown away.
- **Circuit breaker.** After `--breaker-after` failures that may pass in a row (3), no more events are taken for
  30 seconds, growing to 5 minutes; then one event is let through to test, and the rest follow when it
  works. An outage of the destination does not use up the deliveries of the events that wait.
- **At the same time:** `--in-flight N` events (1). While an agent works the broker is told every third of
  `--ack-wait` (60 s) that the work goes on, so a long batch is not delivered to someone else.
- **It ends** with Ctrl+C or SIGTERM, after `--idle-exit SECONDS` with nothing to do (to drain a stream), or
  after `--max-events N`. `--durable NAME` names the consumer on the broker (the default is made from the
  agent and the message): several `consume` with the same name share the work, and a name is how the broker
  remembers the place after a stop. `--stream NAME` says the stream when `[broker.NAME]` does not.
- **Approval.** What `consume` reaches is approved like a tool is: `metagente trust worker.ag --from main
  --subject etl.orders.batch --dead etl.orders.dead`, once for each subject. The password or token comes from
  `[credentials]`, under the name of the broker (`main = "BROKER_PASSWORD"`). The lines it writes tell what
  happens to each event, with its number and the number of the delivery, and never what is in it.

## Grammar

The grammar of a `.ag` file, taken from the lexer and the parser (`internal/lang/lexer.go`,
`internal/lang/parser.go`). It says what is read; what `check` refuses after that (an agent without
`goal`, an `on` without its `accepts`, a call to a tool that was not declared, ...) is in
[What check looks at](#what-check-looks-at).

Notation: `=` defines a rule, `|` is a choice, `[ x ]` is optional, `{ x }` is zero or more times,
`( x )` groups, `"x"` is a word or a sign written as it is. `NEWLINE` ends a line. `INDENT` and
`DEDENT` come from the indentation: two spaces more than the line above, or less. A line that is blank
or only a comment is not read at all, and `#` ends any other line (see `COMMENT`).

```ebnf
(* A file *)
file          = agent { agent } ;
agent         = "agent" NAME NEWLINE [ INDENT agent_line { agent_line } DEDENT ] ;
agent_line    = goal | tool | link | remote | accepts | handler ;      (* in any order *)

(* The lines of an agent *)
goal          = "goal" TEXT NEWLINE ;                                   (* a TEXT without {names} *)
link          = "link" NAME [ "from" TEXT ] NEWLINE ;
remote        = "remote" NAME "at" TEXT NEWLINE ;
accepts       = "accepts" NAME { NAME } [ COMMENT ] NEWLINE ;          (* the comment describes it *)
handler       = "on" NAME NEWLINE block ;                               (* "on start" runs first *)

tool          = "tool" ( file_tool | http_tool | env_tool | "state" | "clock" | server_tool | sql_tool | broker_tool ) NEWLINE ;
file_tool     = "file" [ TEXT ] { "readonly" } ;
http_tool     = "http" { "readonly" | "allow" ( "private" | TEXT { TEXT } ) } ;
env_tool      = "env" TEXT { TEXT } ;
server_tool   = NAME "from" "mcp" TEXT { "readonly" | "env" TEXT { TEXT } } ;
sql_tool      = NAME "from" "sql" TEXT ;                            (* TEXT is a name of [sql.NAME] *)
broker_tool   = NAME "from" "broker" TEXT "publish" TEXT { TEXT } { "publish" TEXT { TEXT } } ;
                                                     (* the first TEXT is a name of [broker.NAME]; the others are subjects *)
                                                     (* NAME is not file, http, env, state or clock *)

(* The lines of a section *)
block         = INDENT statement { statement } DEDENT ;
statement     = reply | fail | if | for | repeat | assignment | expression_line ;
reply         = "reply" expression NEWLINE ;
fail          = "fail" expression [ "retry" [ "in" NUMBER ( "seconds" | "second" ) ] ] NEWLINE ;
if            = "if" expression NEWLINE block [ "otherwise" NEWLINE [ block ] ] ;
for           = "for" NAME "in" expression NEWLINE block ;
repeat        = "repeat" "while" expression [ "up" "to" NUMBER ( "times" | "time" ) ] NEWLINE block ;
assignment    = NAME "=" expression NEWLINE ;
expression_line = expression NEWLINE ;      (* begins with "think", with NAME ".", or with no NAME *)

(* Expressions, from the loosest to the tightest *)
expression    = and_test { "or" and_test } ;
and_test      = not_test { "and" not_test } ;
not_test      = "not" not_test | comparison ;
comparison    = operand [ ( "is" [ "not" | "more" "than" | "less" "than" ] | "contains" ) operand ] ;
operand       = call | think | value ;

call          = path { argument } [ within ] ;          (* at least one argument or a within *)
argument      = NAME ":" value ;
within        = "within" NUMBER ( "seconds" | "second" ) ;
think         = "think" value [ "using" NAME { NAME } ] ;

value         = TEXT | NUMBER | "yes" | "no" | "nothing" | list | path ;
list          = "[" [ value { "," value } ] "]" ;
path          = NAME { "." NAME } ;

(* Words and signs *)
NAME          = ( letter | "_" ) { letter | digit | "_" | "-" } ;
NUMBER        = digit { digit } [ "." digit { digit } ] ;              (* at most 15 digits *)
TEXT          = '"' { character | escape | hole } '"' ;
escape        = "\" character ;            (* \n a line break, \t a tab; any other stands for itself *)
hole          = "{" path "}" ;                (* spaces around the names are allowed *)
COMMENT       = "#" { character } ;           (* to the end of the line, outside a TEXT *)
```

Reading notes:

- **A call and a path look alike.** `clock.now` has no values, so it is read as a `path`; when the
  agent runs, a path whose first name is a tool, a `link` or a `remote` of the agent is a call, and
  otherwise the fields of a value (`forecast.summary`). With values or `within` it is always a call,
  and it needs the form `target.action`. The action may have more names (`weather.Weather.ask`) and
  dashes (`github.create-issue`), since `-` belongs to names.
- **`repeat`** is a loop only when `while` comes right after it, so a value or a tool may still be called
  `repeat`. `up`, `to`, `times` and `time` are read only in the clause `up to N times`, where N is a whole
  number from 1 to 1000000000; they stay free as names.
- **A call is not a value.** The values of a call, the items of a list and the question of `think`
  are a `value`: a call or a `think` there has to be kept in a name first. A `comparison` takes calls
  on both sides.
- **There are no parentheses, no arithmetic and no minus sign.** `-` is part of a name (`a-b` is one
  name), and a negative number can only come from a tool. Keep the parts of a long condition in names.
- **The signs** outside a text are only `.`, `:`, `=`, `,`, `[` and `]`. Any other character there is
  a problem that says which one.
- **`using`** takes names until the line ends or one of `and`, `or`, `is`, `contains` and `within`
  comes.
- **Names are letters of any language**, digits, `_` and `-`, and upper and lower case are different.
  The words in [Words the language keeps](#words-the-language-keeps) have a meaning where they appear,
  so do not use them as names.
