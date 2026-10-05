# The language of Metagente

This is the reference for people who write agents: what a `.ag` file may hold, what each line means,
and what the program checks before it runs anything. Everything here was taken from the lexer, the
parser, the checks and the interpreter (`internal/lang`, `internal/runtime`, `internal/tools`), and
every example in this page passes `metagente check`.

How to run, serve and configure agents is in the [README](../README.md).

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

The clauses of `http` (`allow`, `readonly`) may come in any order and more than once.

A tool server started by a command gets a minimal environment (`PATH`, `HOME` and a few more) plus
the variables named with `env`; a variable that holds a secret (the key of the model, the token of
the server, a credential) is refused even when named. `check` warns when the command runs `npx`,
`uvx`, `pipx run` or `bunx` with a package that has no pinned version, and `check --strict` refuses
it. A tool server is started, or an address reached, only after the person approved it with
`metagente trust` (see the README).

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
is stopped, and so is a chain deeper than `max_call_depth`.

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
| a file read or written | `max_file_bytes` (1 MiB) | `[limits]` |
| an answer of `http` | `max_http_bytes` (5 MiB) | `[limits]` |
| what `state` keeps in a conversation | `max_state_entries` (1000), `max_state_bytes` (256 KiB) | `[limits]` |
| what a tool gives to `think` | `max_tool_result_bytes` (32 KiB), cut and marked | `[limits]` |

## Words the language keeps

`agent`, `goal`, `tool`, `link`, `remote`, `accepts`, `on`, `from`, `mcp`, `env`, `at`, `allow`,
`private`, `readonly`, `reply`, `fail`, `if`, `otherwise`, `for`, `in`, `think`, `using`, `within`,
`seconds`, `is`, `not`, `more`, `less`, `than`, `contains`, `and`, `or`, `yes`, `no`, `nothing`.

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
