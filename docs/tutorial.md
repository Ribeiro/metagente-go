# Your first agent in 15 minutes

You do not need to know how to program. If you can write a list, you can write an agent.

This tutorial is adapted from the one of the [original project](https://github.com/cleuton/MetaAgent),
in Rust, to this port in Go. Every command and every message below was run with Metagente 0.5.0.

## 1. Get Metagente

Metagente is one file, `metagente` (`metagente.exe` on Windows). If you have it, skip to step 2.

The easiest way is to download it from the [releases on GitHub](https://github.com/Ribeiro/metagente-go/releases):
there is an archive for macOS, Linux and Windows, and a `SHA256SUMS` to check it (see
[Installing](../README.md#installing)). Unpack it and put `metagente` somewhere on your PATH, or type its
full path.

If you have Go 1.26 or newer, this installs it in `$(go env GOPATH)/bin`:

```text
go install github.com/Ribeiro/metagente-go/cmd/metagente@latest
```

To build it from a copy of the sources, run `make build` in the project folder; the result is
`bin/metagente`.

Check that it works:

```text
metagente --version
```

## 2. Make an agent

An agent is a small text file that ends in `.ag`. Let Metagente make one for you:

```text
metagente new hello
```

This creates `hello.ag` and a settings file `metagente.toml`. Open `hello.ag`:

```text
agent Hello
  goal "Say hello to someone"
  accepts greet name
  on greet
    reply "Hello, {name}!"
```

Read it out loud. That is all it does:

- `agent Hello` names the agent.
- `goal` says what it is for.
- `accepts greet name` says what other people can ask it: a message called `greet` that comes
  with a `name`.
- `on greet` says what to do when that message arrives.
- `reply` sends the answer back. Names inside `{ }` are filled in.

Lines that belong to something are moved in by **two spaces**.

## 3. Run it

```text
metagente run hello.ag greet name=World
```

You should see `Hello, World!`. After the file come the message (`greet`) and its values, each as
`name=value`. A value with spaces goes in quotes: `name="Ana Maria"`.

## 4. Check it without running it

```text
metagente check hello.ag
```

It answers `No problems found in hello.ag (1 agent).` Try breaking it: change `goal` to `gaol` and
check again. Metagente points at the line and tells you what to do:

```text
Problem on line 2 of hello.ag: I do not know the word `gaol` here
  2 |   gaol "Say hello to someone"
    |   ^
Fix: did you mean `goal`?
```

Errors always say where, what, and how to fix it. Put `goal` back before you go on.

## 5. Give it a tool

Agents get things done with **tools**. An agent can only use the tools it says it wants. Make a
file called `notes.txt` with some text, then create `reader.ag`:

```text
agent Reader
  goal "Read a file and show what is in it"
  tool file
  accepts show
  on show
    text = file.read path: "notes.txt"
    reply text
```

```text
metagente run reader.ag show
```

`tool file` is the permission. Delete that line and run again: Metagente refuses and tells you which
line to add back. That is how you stay in control of what an agent may do.

```text
Problem on line 5 of reader.ag: agent Reader uses `file` but never declared it
  5 |     text = file.read path: "notes.txt"
    |            ^
Fix: add the line `tool file` under `agent Reader`
```

The built in tools:

| Tool | Lines you can write |
|------|---------------------|
| `tool file` | `file.read path: "a.txt"`, `file.write path: "a.txt" text: "hi"` |
| `tool http` | `http.get url: "https://..."`, `http.post url: "https://..." body: "hi"` |
| `tool env "NAME"` | `env.get name: "NAME"` (only the names you list) |
| `tool state` | `state.set key: "k" value: "v"`, `state.get key: "k"` (the agent's memory in a conversation) |
| `tool clock` | `clock.now`, `clock.wait seconds: 5` |

Some details that keep you safe:

- `tool file "data/"` lets the agent use only the `data` folder. A path that leads out of it, such as
  `../notes.txt`, is refused.
- `tool http` reaches only public addresses: never this computer or a private network, unless you write
  `tool http allow private`. `tool http allow "api.example.com"` limits it to that domain. Its answer is
  a record: `status`, `text`, and `json` when the answer is JSON (`page.json.name`).
- `tool env` never gives the key of the model or a token, even when you list its name.
- Where the web is only reached through the proxy of a company, the proxy is set in `metagente.toml`,
  never in the agent: see [Through the proxy of a company](../README.md#through-the-proxy-of-a-company).

## 6. Use a tool someone else made (MCP)

Many programs offer tools through a standard called MCP. Use one with a single line:

```text
agent Weather
  goal "Answer questions about the weather"
  tool weather from mcp "npx -y weather-mcp@1.2.0"
  accepts ask city
  on ask
    forecast = weather.forecast city: city within 60 seconds
    reply "In {city} it will be {forecast.summary}"
```

`tool weather from mcp "..."` starts the program and finds its tools. You call them like any other
tool: `weather.forecast city: city`. If the tool takes long, add `within 60 seconds` at the end of the
**call**, not of the `tool` line. A tool server may also be an address:
`tool search from mcp "https://mcp.example.com/mcp"`.

(The `weather-mcp` package above is only an example. Use any MCP server you have, and write its
version after `@`: `check` warns about a package that has no fixed version, because it could change
under you.)

**Before the first run you approve it.** An agent that starts a program or connects to an address does
nothing until you say yes, once for each project:

```text
metagente trust weather.ag
```

It lists what the agent wants (`starts the program: npx -y weather-mcp@1.2.0`) and asks. `run` asks
too, when there is a terminal. `metagente trust --list` shows what you approved, and
`metagente trust --revoke` takes it back. If the line changes, it is asked again.

Tool names from other programs may contain dashes or dots, and you write them as they are:
`github.create-issue title: "Hi"`, or `weather.Weather.ask city: "Lisbon"` for a tool called
`Weather.ask`.

**Try it without installing anything.** Metagente itself is an MCP server: `metagente serve --stdio`
offers the agents of a file as tools named `Agent__message`. In the folder of `hello.ag`, create
`greeter.ag`:

```text
agent Greeter
  goal "Greet through another program"
  tool hello from mcp "metagente serve --stdio hello.ag"
  accepts greet name
  on greet
    answer = hello.Hello__greet name: name within 60 seconds
    reply answer
```

```text
metagente trust greeter.ag
metagente run greeter.ag greet name=Ana
```

You should see `Hello, Ana!`, answered by a second Metagente that the first one started. If you call an
action the server does not have, the message lists the ones it has.

## 7. Let the agent think

Some jobs need a language model. Add `think`:

```text
agent Helper
  goal "Answer questions"
  accepts ask question
  on ask
    reply think "{question}"
```

The model and your key are settings, never part of the agent. Open `metagente.toml` and remove the
`#` in front of the `[llm]` lines:

```toml
[llm]
provider = "anthropic"               # or "openai-compatible"
model = "claude-sonnet-5-5"          # the model name your provider gives you
api_key_env = "ANTHROPIC_API_KEY"    # the NAME of the variable that holds your key
```

`api_key_env` is the **name** of the environment variable, never the key itself. Put the key in the
variable, then run:

```text
export ANTHROPIC_API_KEY=...
metagente run helper.ag ask question="What is the capital of Portugal?"
```

`openai-compatible` works with OpenAI and any server that speaks its format (a local model too), with
`base_url` set to its address. The agent file does not change when you switch providers. When the
model needs a tool, it can use only the tools the agent declared, and only with the same limits.

Without the `[llm]` section, Metagente says so and how to fix it, instead of failing in silence.

## 8. Agents that call agents

Put `weather.ag` and `planner.ag` in the same folder. To try it without a tool server, make the weather
agent simple:

```text
agent Weather
  goal "Answer questions about the weather"
  accepts ask city
  on ask
    reply "sun in {city}"
```

In the planner:

```text
agent Planner
  goal "Plan a trip using the weather"
  link Weather
  accepts plan city
  on plan
    answer = Weather.ask city: city
    reply "Pack for this: {answer}"
```

```text
metagente run planner.ag plan city=Lisbon
```

You should see `Pack for this: sun in Lisbon`. `link Weather` finds `Weather` when the line runs (in
the same file, next to it, or in `agents/`), so you can change `weather.ag` without touching the
planner. `check` makes sure that `Weather` really accepts `ask`:

```text
Problem on line 6 of planner.ag: agent Weather does not accept the message `forecast`
  6 |     answer = Weather.forecast city: city
    |              ^
Fix: Weather accepts: ask
```

And if two agents call each other in a circle, Metagente stops and says who asked whom
(`A -> B -> A`).

## 9. Let others reach your agent

```text
metagente serve weather.ag --port 8080
```

Every request to the server needs a **token**. On your own computer, in a terminal, Metagente makes
one for that run and shows it (`Token for this run, kept nowhere else: ...`). To keep the same one,
make it once and give it to the server:

```text
export METAGENTE_TOKEN="$(metagente token)"
metagente serve weather.ag --port 8080
```

Each agent has its own address, shown when the server starts, and its card at
`http://127.0.0.1:8080/agents/Weather/.well-known/agent-card.json`. Other agents send it tasks over
A2A. If you edit the file, stop the server (Ctrl-C) and start it again to serve the change.

The same agents can be tools for programs that speak MCP: `--mcp` serves them at `/mcp` too, behind
the same token, and `--stdio` serves them on standard input and output, as in step 6. See
[Serving agents](../README.md#serving-agents).

To call an agent served elsewhere, declare it with `remote`, at the address the server shows:

```text
agent Trip
  goal "Ask a remote weather agent"
  remote Bob at "http://127.0.0.1:8080/agents/Weather"
  accepts plan city
  on plan
    forecast = Bob.ask city: city
    reply "Bob says: {forecast}"
```

The token for Bob is never written in the agent. In the `metagente.toml` of the caller, name the
variable that holds it:

```toml
[credentials]
Bob = "BOB_TOKEN"
```

Then, in another terminal:

```text
export BOB_TOKEN=...          # the token of the server
metagente trust trip.ag       # it connects to an address, so you approve it once
metagente run trip.ag plan city=Lisbon
```

You should see `Bob says: sun in Lisbon`.

The server listens only on your own computer unless you open it to the network with `--public`, which
needs TLS (a certificate and its key) and the names it answers to (`--host`). A server on one computer
and an agent that calls it from another is shown, step by step, in
[samples/city-briefing/TWO-COMPUTERS.md](../samples/city-briefing/TWO-COMPUTERS.md). A server can also
hold a token for each client in a file (`--token-file`), so one can be taken away without the others.

## Where next

- All the words of the language, decisions (`if`), loops (`for`) and the limits: [LANGUAGE.md](LANGUAGE.md)
- Serving, tokens, TLS, MCP over HTTP, and the choices behind them: [the README](../README.md)
- Two agents that work together with `think`, over A2A and MCP: [samples/city-briefing](../samples/city-briefing/)
- Ideas to try: change the goal, add `if` and `for`, remember things with `tool state`.
