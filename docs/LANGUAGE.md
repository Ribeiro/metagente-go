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
| `clock.now`, `clock.wait` | the time, and waiting | [tool](#tool) |
| `contains` | a text has a piece, or a list has an item | [Conditions](#conditions) |
| `env` | `tool env "NAME"`, or the variables given to a tool server | [tool](#tool) |
| `env.get` | reads a variable that `tool env` names | [tool](#tool) |
| `fail` | ends the section with a failure | [fail](#fail) |
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

Ends the section with a failure, whose message is the value. The caller sees it as a problem.

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
| an answer of `sql` | `max_sql_rows` (10000), `max_sql_bytes` (5 MiB) | `[limits]` |
| what `state` keeps in a conversation | `max_state_entries` (1000), `max_state_bytes` (256 KiB) | `[limits]` |
| what a tool gives to `think` | `max_tool_result_bytes` (32 KiB), cut and marked | `[limits]` |

## Words the language keeps

`agent`, `goal`, `tool`, `link`, `remote`, `accepts`, `on`, `from`, `mcp`, `sql`, `env`, `at`, `allow`,
`private`, `readonly`, `reply`, `fail`, `if`, `otherwise`, `for`, `in`, `repeat`, `while`, `think`, `using`,
`within`, `seconds`, `is`, `not`, `more`, `less`, `than`, `contains`, `and`, `or`, `yes`, `no`, `nothing`.

`repeat` is a loop only when `while` comes right after it, and `up`, `to`, `times` and `time` have a
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

tool          = "tool" ( file_tool | http_tool | env_tool | "state" | "clock" | server_tool | sql_tool ) NEWLINE ;
file_tool     = "file" [ TEXT ] { "readonly" } ;
http_tool     = "http" { "readonly" | "allow" ( "private" | TEXT { TEXT } ) } ;
env_tool      = "env" TEXT { TEXT } ;
server_tool   = NAME "from" "mcp" TEXT { "readonly" | "env" TEXT { TEXT } } ;
sql_tool      = NAME "from" "sql" TEXT ;                            (* TEXT is a name of [sql.NAME] *)
                                                     (* NAME is not file, http, env, state or clock *)

(* The lines of a section *)
block         = INDENT statement { statement } DEDENT ;
statement     = reply | fail | if | for | repeat | assignment | expression_line ;
reply         = "reply" expression NEWLINE ;
fail          = "fail" expression NEWLINE ;
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
