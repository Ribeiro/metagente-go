# Metagente (Go port)

A port of [Metagente / MetaAgent](https://github.com/cleuton/MetaAgent), an interpreted
language for AI agents, from Rust to Go, with the hardening described in the
specification ("Metagente em Go: inventário de testes e especificação de
endurecimento").

All code, comments and tests are written in English.

## Status

| Slice | What | State |
| --- | --- | --- |
| 1 | The language front end: `check`, `new` | Compiled; `go vet` and `go test ./...` pass |
| 2 | The interpreter, the built in tools, configuration, `run` | Compiled; unit tests and the acceptance suite pass. The symbolic link and hard link tests were confirmed as `PASS`, not `SKIP` |
| 3 | `link`: agents calling local agents | Compiled; unit tests and the acceptance suite pass |
| 3.1 | F4 in full: atomic writes in the `file` tool | Compiled; unit tests and the acceptance suite pass |
| 4 | Tool servers (MCP client) and the approval of what an agent starts or reaches | Compiled; unit tests and the acceptance suite pass, and a real tool server was run end to end |
| 5 | `think` and the language model providers (Anthropic and OpenAI compatible) | Compiled; tests pass, and it was run against the real Anthropic API, with a tool, and resisted an order hidden in a file |
| 6 | `remote`: calling agents that run somewhere else (A2A client) | Compiled; tests pass, including the one against the server of the SDK |
| 7 | Credentials (E5): a bearer token for remote agents and for tool servers that are addresses | Compiled; tests pass |
| 8 | The log of failures inside Metagente (P2, P3) | Compiled; tests pass |
| 9 | The building blocks of `serve`: the door (S1 to S4, S6), the token, the conversations (S7, S9), `metagente token` | Compiled; tests pass, also with `-race` |
| 10 | The A2A server: JSON-RPC, the cards, the agents of the interpreter behind it | Compiled; tests pass, including the client of the SDK against it |
| 11 | The `serve` command: listener and its limits, TLS, the flags, the banner, the access log, a clean stop | Compiled; tests pass, also with `-race`; run by hand with two processes |
| 12 | The agents as MCP tools: `metagente serve --stdio` | Compiled; tests pass, also with `-race` |
| 13 | The differences from the specification closed: E3, E4, S4, S6, S8, P5 | Compiled; the tests pass, also with `-race` |

Every slice compiles and its tests pass. The only system they were run on is macOS; nothing
was tried on Linux or Windows yet (see [Not verified yet](#not-verified-yet)).

The project needs **Go 1.26 or newer** (`go.mod` says `go 1.26.0`, because the A2A SDK asks
for it). The tests keep the log in a folder of their own, never in yours. How to run them:
[Running the tests](#running-the-tests).

## Serving agents

```text
export METAGENTE_TOKEN=$(metagente token)     # or let a terminal on this computer make one
metagente serve hello.ag                      # http://127.0.0.1:8080, agents at /agents/NAME
curl -H "Authorization: Bearer $METAGENTE_TOKEN" http://127.0.0.1:8080/agents/Hello/.well-known/agent-card.json
```

| Where | How | What it needs |
|---|---|---|
| This computer (the default) | `metagente serve FILE.ag` | a token: `METAGENTE_TOKEN`, or one is made and shown once if a person is at the terminal |
| The network | `--public --tls-cert F --tls-key F --host NAME` | the token in `METAGENTE_TOKEN` (never made), TLS 1.2 or newer, the names it answers to |
| Behind a proxy on this computer | `--behind-proxy --host NAME --public-url https://NAME` | the token in `METAGENTE_TOKEN`; the proxy does the TLS and has to limit the rate |

Other options: `--port N`, `--agent NAME` (repeatable: serve only those; by default all the
agents of the files), `--public-card` (a card with the names only, for anyone), `--quiet` (no
access log), `--config FILE`. Files and options may come in any order.

### Serving to a program that speaks MCP

```text
metagente serve hello.ag --stdio
```

Each message an agent accepts becomes a tool named `Agent__message` (`Hello__greet`), with the
values it takes as required parameters. A conversation belongs to a session: what an agent keeps
in `state` is known in that session only. A failure reaches the program as an error of the tool,
with the problem and what to do, never the file or the lines.

It uses **standard input and output**: no port is opened and no token is asked, because the
program that starts it is who talks to it, and who decides who may. Nothing but the protocol is
written to the output; what `serve` says goes to the error output. The network options
(`--port`, `--public`, `--host`, ...) are refused with `--stdio`.

For an assistant that starts the tool servers itself, such as a desktop client, the settings are
like these. Use full paths: the program does not start in your folder.

```json
{
  "mcpServers": {
    "hello": {
      "command": "metagente",
      "args": ["serve", "--stdio", "--config", "/full/path/metagente.toml", "/full/path/hello.ag"],
      "env": { "BOB_TOKEN": "..." }
    }
  }
}
```

The approvals belong to the folder of that `metagente.toml`, so run `metagente trust hello.ag`
in that folder first: like every `serve`, this one never asks, and stops when something is not
approved. Variables for `[credentials]` go in `env`, since the client gives the program a
minimal environment.

`serve` **never asks to approve** a tool server or a remote agent: if something is not
approved it stops and says so, as `run` does when nobody is at the terminal. The settings
in `[serve]` that it uses: `bind`, `a2a_port`, `public_url`, `allowed_hosts`,
`max_connections`, `max_running_tasks` (requests at once; the next gets a 503),
`max_retained_tasks` (conversations), `task_retention_seconds` (how long an idle conversation
is kept), `max_body_bytes`, `read_header_timeout_seconds`, `read_timeout_seconds`,
`idle_timeout_seconds`, `max_header_bytes` and `auth_failures_per_minute` (wrong tokens from one
place before it waits a minute). `allowed_origins` is **ignored**: no page in a browser can call this
server, and `serve` says so when it is set.

**Memory.** A conversation may keep `max_state_bytes` of `state` (1 MiB by default) and the
server keeps up to `max_retained_tasks` of them (1000 by default). Together that is up to
about 1 GiB that someone who holds the token could make the server use. On a small machine,
lower one of the two.

## What exists

- `metagente check [--strict] FILE.ag`: lexer, parser, static checks, warnings.
- `metagente new NAME`: a starter agent and a `metagente.toml`.
- `metagente run FILE.ag [MESSAGE] [key=value ...] [--agent NAME] [--config FILE]`.
- `link`: an agent calls another agent of the same machine like a tool
  (`Weather.ask city: "Lisbon"`). The agent is found in the same file, next to the
  caller, in `agents/`, or by a quoted path; it is looked up again at each call, so
  editing a linked agent applies without restarting the caller. `check` and `run`
  refuse a missing target and a call the target does not accept, before anything runs;
  a cycle stops with the circle shown.
- Tool servers (`tool weather from mcp "command"` or `from mcp "https://..."`), through
  the official Go SDK. One session per server is shared by the whole process, and
  calls on it run at the same time up to `max_mcp_calls`. The program gets a minimal
  environment plus the variables the agent file names with `env "VAR"`.
- `metagente trust FILE.ag [--yes]`, `--list` and `--revoke`: nothing that starts a
  program or connects to an address runs before the person approved it, for this
  project. `run` asks when there is a terminal and refuses when there is not.
- The built in tools: `file`, `http`, `env`, `state`, `clock`.
- Configuration: `metagente.toml`, `--config`, and the environment overrides.
- `think "..." [using a b]`: the agent asks a language model, which may use the tools it
  declared. The provider is chosen only in `metagente.toml`: `anthropic`, or
  `openai-compatible` for OpenAI and for any server that speaks its chat format. The
  key is read from the variable named in `api_key_env`. An error of a tool goes back to
  the model, not to the person.
- `remote Bob at "https://host"`: an agent that runs somewhere else, called like a tool
  (`Bob.ask city: "Lisbon"`) over A2A. The skills of its card are its actions, and the
  model sees them too. A task that takes time is followed until it ends, and cancelled
  on the other side if the call is given up.
- `metagente serve FILE.ag ...`: the agents over A2A, on this computer, behind a token; open to
  the network with TLS of its own, or behind a proxy (see [Serving agents](#serving-agents)).
- `metagente serve FILE.ag --stdio`: the same agents as MCP tools on standard input and output.
- `metagente token`: makes a token to keep and give to the server.
- `[credentials]` in `metagente.toml`: the bearer token for a `remote` agent or for a tool server
  that is an address, named by the variable that holds it.
- A log of failures inside Metagente, in the folder of the user, that holds the cause and the stack
  of what a person or a remote caller was only told in one sentence.

## Requirements

The specification has 47. The state of each one:

- **done**: as the specification says;
- **done, changed (agreed)**: it differs on purpose, and the change was decided;
- **differs (open)**: the port does something else than the specification says, and nobody decided it yet (none today);
- **partial**: part of it is missing.

Today: 40 done, 5 done, changed (agreed), 2 partial.

| ID | State | What | Where |
| --- | --- | --- | --- |
| D1 | done | `within` and `clock.wait` are limited by `max_wait_seconds` | `internal/runtime`, `internal/tools/clock.go` |
| D2 | done | the depth of calls between agents is limited by `max_call_depth`: inside one process (`link`), and across processes in the metadata of the message, checked by the client before the call leaves and by the server when it arrives. A circle is found and shown | `internal/runtime/link.go`, `internal/runtime/remote.go`, `internal/serve/rpc.go` |
| D3 | done | ceiling on entries and bytes of `state` | `internal/tools/state.go` |
| D4 | done | limits on the size of a file, of a line, on nesting and on the length of a list | `internal/lang/limits.go` |
| E1 | done | a program receives `PATH`, `HOME` and a few more, plus only the variables the agent file names; a secret is refused even when it is named | `internal/mcp/pool.go` |
| E2 | done | a warning for `npx`, `uvx`, `pipx run` and `bunx` without a pinned version; an error with `--strict` | `internal/lang/command.go`, `check.go` |
| E3 | done | calls on a tool server run at the same time, up to `max_mcp_calls` **for each server**, so a server that is slow does not hold back the others (the spike showed a shared session needs no serialising) | `internal/mcp/pool.go` |
| E4 | partial | closing the runtime ends every program it started and, on Linux and macOS, the whole group of processes it led: the SDK closes its input, signals it and kills it after 5 s, and what is left of the group is asked to end and killed after 2 s. A server that died is started again on the next call. **On Windows the children of the program are not reached, and that was decided, not forgotten**: a job object holds only the processes that are born after the parent joined it, and the SDK starts the program inside `Connect`, so the children of `npx` would already exist; doing it right means replacing the command transport of the SDK on Windows, and it was not judged worth it. Under WSL2 the group of processes works as on Linux. `run` and `serve` end the group on SIGINT and SIGTERM | `internal/mcp/pool.go`, `group_unix.go`, `group_other.go` |
| E5 | done, changed (agreed) | a bearer token for `remote` agents and for tool servers that are addresses, named in `[credentials]`; never in a file, never in an error, never readable by an agent. Named in `[credentials]` of `metagente.toml`, not in a `token env` clause of the `.ag` file | `internal/remote`, `internal/mcp`, `internal/config`, `internal/trust` |
| E6 | done | `env` never reads the key of the model, `METAGENTE_TOKEN` or the usual key names | `internal/tools/env.go`, `config` |
| F1 | done | file access through `os.Root`: `..` and symbolic links that leave the folder are refused | `internal/tools/file.go` |
| F2 | done | ceiling on the size of files read and written | `internal/tools/file.go` |
| F3 | done | names that are not portable (Windows device names, `:`, trailing dot or space) are refused everywhere | `internal/tools/file.go` |
| F4 | done | writing to a hard link is refused, and a write goes to a temporary file in the same folder that is then renamed over the target, so a failure never leaves a half written file. A link inside the folder is still written through, in place | `internal/tools/file.go` |
| H1 | done | `http` refuses internal addresses after name resolution, on every connection and redirect | `internal/tools/http.go` |
| H2 | done | `tool http allow "domain"` and `allow private` | `internal/tools/http.go` |
| H3 | done | ceiling on the answer, at most 5 redirects, 10 s to connect, no proxy from the environment | `internal/tools/http.go` |
| L1 | done | `max_tokens` is configurable (default 4096) and an answer cut by it is an error, never a short answer | `internal/runtime/think.go`, `internal/llm` |
| L2 | done | tools asked in the same answer run together up to `think_max_parallel`, and come back in the order asked | `internal/runtime/think.go` |
| L3 | done | a result is cut at `max_tool_result_bytes` (default 32 KiB) and says so | `internal/runtime/think.go` |
| L4 | done | prompt cache: the system prompt and the end of the conversation are marked, so each step reuses what came before (Anthropic) | `internal/llm/anthropic.go` |
| L5 | done | a question is limited in steps, tokens and time (`think_max_steps`, `think_max_total_tokens`, `think_timeout_seconds`) | `internal/runtime/think.go` |
| L6 | done | what a tool returns is wrapped in markers with a random tag and the model is told it is data, not orders | `internal/runtime/think.go` |
| L7 | done | `using` limits the tools; a `readonly` tool offers only what changes nothing (for tool servers, what they mark as read only) | `internal/runtime/think.go`, `internal/mcp/pool.go` |
| L8 | done | the key is taken out of every error from a provider; it is never in a message | `internal/llm/transport.go` |
| P1 | done | what a remote caller (A2A or MCP) or a model reads about a failure is the short message and its fix, with no file, line, source or path | `internal/diag`, `internal/serve`, `internal/runtime/think.go` |
| P2 | done | the log of failures is in the folder of the user (folder `0700`, file `0600`, never through a link, one file of history) | `internal/applog` |
| P3 | done | every key, token and credential variable is taken out of the log and of every error; the access log never holds a header, a query or a body | `internal/applog`, `internal/secret`, `internal/serve/accesslog.go` |
| P4 | done | a panic ends only that task or call and shows one plain sentence; the cause goes to the log | `internal/cli`, `internal/runtime`, `internal/serve` |
| P5 | done | the access log is structured (`log/slog`, `key=value`): time, remote address, method, path, status, bytes and duration; and, for a request that ran an agent, the agent, the method of the protocol, the message, the task and how it ended (`ok`, `failed`, `error`, `busy`, `left`). Never a header, a query or a body. It is the log of `serve` over HTTP; the MCP over standard input and output has none | `internal/serve/accesslog.go`, `rpc.go` |
| S1 | done, changed (agreed) | every route that runs an agent needs `Authorization: Bearer`; constant time comparison; the same short 401 for every failure; the token is never in a log. Changed: the Agent Card is also for those who have the token (`--public-card` gives a minimal one to anyone), and the token comes from `METAGENTE_TOKEN`, or is made only for a person at a terminal on this computer. **There is no `--token-file`** | `internal/serve/guard.go`, `token.go` |
| S2 | done | a POST needs `application/json` (415 otherwise, before the body is read); no `OPTIONS`, no CORS header | `internal/serve/guard.go` |
| S3 | done | the `Host` has to be a name the server answers to (421 otherwise, token or not): the ones of this computer by default, or `--host` | `internal/serve/guard.go`, `options.go` |
| S4 | done, changed (agreed) | a request made by a browser is refused (403). Stricter than the specification, on purpose: **any** `Origin`, and the `Sec-Fetch-Site`, `Sec-Fetch-Dest` and `Sec-Fetch-User` that a browser adds to every request, are refused, and `allowed_origins` is ignored (the server says so). `Sec-Fetch-Mode` alone is not: the fetch of Node.js sends it in every request, and the official SDK in JavaScript is made on it, so refusing it kept that client out. Kept because it is safer; the specification is to be changed to say so | `internal/serve/guard.go` |
| S5 | done, changed (agreed) | a server only listens beyond this computer with `--public`, which needs the names it answers to. Changed: instead of a warning about the lack of TLS, `--public` needs TLS of its own (1.2 or newer), and `--behind-proxy` only counts when the server listens on this same computer | `internal/serve/options.go`, `listen.go` |
| S6 | done | read of the header 5 s, read 30 s, idle 60 s, headers 16 KiB, body 1 MiB (413), 256 connections, 64 tasks at the same time (the 65th gets 503 with `Retry-After`), 1000 conversations kept, and 10 wrong tokens a minute from one place (then 429 for a minute). **All of them are settings of `[serve]`.** Tested on a real port: a body of 2 MiB, 100 slow connections beside a normal request, the 65th task, and 50 requests at once | `internal/serve/guard.go`, `listen.go`, `server.go`, `load_test.go` |
| S7 | done | `state` is kept per agent and per conversation, and the server issues the id of a conversation; an idle conversation expires after `task_retention_seconds` (600 s; the specification says 1 h) and its memory is let go | `internal/tools/state.go`, `internal/serve/contexts.go` |
| S8 | done | the address in the Agent Card never comes from a request: it is `public_url`, else the first of the names the server answers to, else the address it listens on | `internal/serve/options.go`, `server.go` |
| S9 | done | no task is kept, so there is nothing to sweep; conversations that are not used are swept in the background (`task_retention_seconds`) | `internal/serve/contexts.go` |
| S10 | partial | the agents as MCP tools over standard input and output are done (no token: the program that starts it is who talks to it). **MCP over HTTP, with S1 to S4 and S6, is not done** | `internal/serve/mcp.go` |
| T1 | done | a program is started or an address reached only after the person approved it for this project; the approved set carries a SHA-256 that is checked on every read. `run` asks in a terminal, otherwise it refuses and says how to approve. The same for `remote` agents: the address, and the token that goes there | `internal/trust`, `internal/runtime/trust.go`, `internal/cli/trust.go` |
| T2 | done | the approvals live in the folder of the user (`0600`, folder `0700`, written through a temporary file), never inside the project, also when the path is reached through a symbolic link or does not exist yet | `internal/trust` |
| T3 | done | the key goes only to the address in the configuration; a non default address needs approval, plain `http` is allowed only on this machine, and a redirect is never followed | `internal/llm`, `internal/runtime/trust.go` |
| T4 | done | the search for `metagente.toml` stops at the repository root or the home folder | `internal/config` |
| T5 | done | a `link from` that leaves the project is warned about in `check` (an error with `--strict`) | `internal/runtime/link.go` |
| X1 | done | `check` refuses a call to a name that was never declared (`link`, `remote` or a tool server) | `internal/lang/check.go` |
| X2 | done, changed (agreed) | `check` validates `using`, `readonly`, `allow` and the empty MCP command. The `token` clause is not parsed: credentials are in `metagente.toml` | `internal/lang/check.go` |

## Choices to review

- **The programs of the tool servers lead a group of processes of their own** (Linux and macOS).
  So `npx x`, which starts the real server, can be ended with all it started. The price: a
  Ctrl-C at the terminal no longer reaches those programs by itself, only `metagente`, which
  ends them. `run` and `serve` handle SIGINT and SIGTERM for that. If `metagente` is killed
  without a chance to react (SIGKILL), the group is left running, as before; on Linux this could
  be improved with the signal that a parent sends to its children when it dies, but that one is
  tied to a thread of the process and ends the child at the wrong moment, so it was left out.
- **The card says where the server is, and a request cannot change it.** A request may use any
  name the server answers to; the card says the same thing to all of them. The consequence is
  that a client has to reach the server by the name the card gives: a server bound to
  `127.0.0.1` is not reached as `localhost` by a `remote` agent, which says that the card sends
  the calls to another address and names it. Use `--host` to choose the name, or write the
  address the card gives.
- **Too many requests at the same time is a 503, and not a message of the protocol.** A proxy or a
  client that knows nothing of A2A understands it, and the client of this project says that the
  other side is busy.
- **TOML.** The standard library has no TOML reader, so `internal/config/toml.go` reads
  the small subset that `metagente.toml` uses: sections, texts, whole numbers,
  true/false, lists of texts on one line, comments. Anything else is refused with a
  plain message. Swap it for a real TOML library whenever you prefer; only
  `parseTOML` has to change.
- **`allow private` is narrower than the specification.** It opens loopback and
  private networks, but link-local addresses (where cloud metadata services live),
  multicast and unspecified addresses stay refused. The specification (H2) said
  "non-public destinations"; I judged that the metadata address should never open.
- **The folder of `tool file "dir/"` is trusted.** It is written by the author of the
  agent, so it is not checked against the project folder.
- **Timeouts.** A call runs in its own goroutine and the interpreter stops waiting at
  the deadline. A tool that ignores its context would keep running in the background.
  All tools here honour it.
- **The providers are written here, on `net/http`, and not taken from an SDK.** The
  surface is small (messages, tools, the reason to stop, the tokens used), and the
  rules that matter are ours: where the key goes, no redirects, no key in an error. A
  new feature of a provider is ours to add.
- **Anthropic's `pause_turn` and an empty `end_turn` are not handled as special cases.**
  `pause_turn` only appears with tools the provider runs itself, which are not used;
  it is reported as "stopped without an answer". An empty answer comes back as empty
  text.
- **`max_tokens_field`.** OpenAI retired `max_tokens` and its newer models refuse it,
  while servers people run themselves still expect it. By default the name is chosen
  by the address (`api.openai.com` and Azure get `max_completion_tokens`); the setting
  `max_tokens_field` in `[llm]` overrides it.
- **The A2A client is written by hand, and the SDK is kept for the server.** The client
  is about 400 lines on `net/http`, in the wire format of A2A 1.0 (`SendMessage`,
  `GetTask`, `CancelTask`, `ROLE_USER`, `TASK_STATE_*`). The API of the SDK client was
  not confirmed, and writing it here gives control of what matters: no redirects, a
  size limit, the card tied to the approved address. The interoperation test is the
  safety net, and replacing the client by the one of the SDK later is a local change.
- **A skill has no declared values.** An A2A card names what an agent handles, not the
  values each skill takes, so a remote action accepts any values and the model is told
  only its description. `serve` writes the values of each `accepts` into the
  description of the skill (`Takes: name.`), so Metagente agents describe themselves well.
- **`serve` listens as little as it can.** Only HTTP/1.1 (not HTTP/2), nothing older than
  TLS 1.2, a header read timeout, a limit of connections and of the size of a header, and
  a structured access log (`log/slog`) that never holds a token, a header, a query or a body
  (the values of the request are quoted when they need it, so a request cannot forge a line). Stopping it (Ctrl-C or SIGTERM) gives a running request
  a few seconds, cuts what is left, and lets go of every conversation.
- **The A2A server is written by hand, like the client, and answers inside the request.**
  `SendMessage` runs the agent and answers with a Message when it worked, and with a Task
  that has already failed when it did not (so the reason reaches the caller, and nothing
  else does). The server keeps **no tasks**: `GetTask` and `CancelTask` find none, and
  there is nothing to fill up. Streaming, push notifications and the extended card are
  not offered and say so. Batches are refused. An agent is served at `/agents/NAME`, its
  card at `/agents/NAME/.well-known/agent-card.json`, and a stranger cannot tell which
  agents exist (without the token every other path is 401, with it, 404).
- **The card is for those who have the token.** `--public-card` gives anyone a
  minimal one: names only, never the goal or the values a message takes. Both say that a
  token is needed. The client sends the token with the card too, to the address that was
  approved and to no other.
- **What a caller from outside is told about a failure is the problem and what to do, not
  where it is.** No file name, no line, no source, no related lines (P1).
- **An agent is told who is calling.** The chain of agents running goes in the metadata
  of the message; a call that goes deeper than the limit, or comes back to an agent that is
  already in the chain, is stopped by the server too (D2). The chain is the word of a
  caller that holds the token, checked only for its shape.
- **The memory of a conversation is let go when the conversation ends.** The store of
  `state` can now forget a context, and the server does it when one expires; before, a
  server that ran for long would only grow.
- **The door of the server is written and tested before the server.** `internal/serve`
  has the guard every request goes through: the Host must be one the server answers to
  (a page that makes your browser call a server on your computer carries another),
  a request made by a browser (an `Origin`, or the `Sec-Fetch-Site`, `Sec-Fetch-Dest` or `Sec-Fetch-User` of a browser) is refused, the token is
  compared without the time telling how much was right, a refusal never says what was
  wrong, ten wrong tries from one place stop that place for a minute, only a POST of
  `application/json` is taken, the body has a limit, and nothing ever says that another
  site may call the server (there is no CORS, and a preflight gets 405).
- **Behind a proxy the slowing down is turned off, and the proxy has to limit the rate.**
  Every request then comes from the address of the proxy, so ten wrong tokens from anyone
  would stop everyone. Trusting `X-Forwarded-For` would let whoever sets the header pick
  its address, so it is not used.
- **The server issues the id of a conversation.** A caller cannot name one it was not
  given, an idle conversation expires, there is a limit of open ones, and one that is in
  use is never swept away in the middle of a call.
- **The token is `METAGENTE_TOKEN`, 32 characters or more**, from `metagente token`. If it
  is not set, one is made only for a person at a terminal on this computer; anywhere else
  (a CI, a container, `--public`) the server does not start, because a secret shown there
  stays in a log.
- **The log is for you, not for whoever called.** A failure inside Metagente shows one
  sentence and the place of the log. It lives in `~/.local/state/metagente` (Linux),
  `~/Library/Logs/metagente` (macOS) or `%LocalAppData%\\metagente` (Windows), or in
  `METAGENTE_STATE_DIR`. The folder is made private, a folder that others can change is
  not used, and a log file that is a link leading out of the folder is refused. Keys,
  tokens and the variables named in `[credentials]` are taken out of every line.
- **Credentials live in `[credentials]`, as the name of a variable.**
  `Bob = "BOB_TOKEN"` means: send the token held in `BOB_TOKEN` to `remote Bob`. It
  also works for a tool server that is an address (`tool search from mcp "https://..."`
  uses the name `search`); a program started by a command never gets a token, because
  it gets variables through `env`. The token is read when it is used, sent only with
  the calls and with the card, only to the address that was approved,
  only over `https` or on this machine, and taken out of anything the other side
  echoes. An agent cannot read that variable through the `env` tool.
- **A token is part of what you approve.** `metagente trust` shows "connects to: URL
  (and sends it the token held in BOB_TOKEN)", and approving the address alone does
  not approve sending a token there.
- **Anthropic keys that are not tied to one workspace.** A personal key (it starts with
  `sk-ant-usr-`) works in several workspaces, so each request must say which one, with
  the header `anthropic-workspace-id`; without it the API answers 400. Put the id in
  `workspace_id` in `[llm]`. A key created inside a workspace needs nothing. The id is
  not a secret, so it may live in `metagente.toml`.
- **Answers people meet when they set up come with what to do.** 401, 403, 404 and the
  400 about the workspace each carry a hint, shown as the `Fix:` line. Other errors keep
  the general advice.
- **`api_key_env` holds a name, never the key.** A value that cannot be the name of a
  variable is refused without being repeated, because it is probably the key itself.
- **The key is read when the first `think` runs**, from the process environment, and
  kept in memory for the run. A server on `localhost` may go without a key.
- **The model does not see paths.** An error from a tool is shown to the model with
  its message and its fix only, never with a file name or a line of source.

## Layout

```text
cmd/metagente          the binary: it only calls internal/cli
internal/cli           commands; Run(args, stdout, stderr) is testable in-process
internal/config        metagente.toml, defaults, limits, [credentials], the T4 search
internal/diag          the one error shape users see (and its Public form)
internal/lang          lexer, parser, AST, checker, capabilities
internal/runtime       the interpreter, `run`, `think`, `link`, `remote`, the approvals
internal/tools         file, http, env, state, clock, and the registry
internal/mcp           the pool of tool servers (MCP client)
internal/remote        the A2A client
internal/serve         the A2A server, the MCP server, the door, the conversations, the listener
internal/llm           the providers of language models (Anthropic, OpenAI compatible)
internal/trust         what the person approved for a project
internal/applog        the log of failures inside Metagente
internal/secret        hiding secrets in a text; what a name of a variable may be
internal/clip          shortening a text that came from outside before a message repeats it
internal/scaffold      `metagente new`
internal/value         the values agents work with
internal/acceptance    black-box suite (build tag `acceptance`)
testdata/examples      example agents, copied from the Rust project
testdata/script        acceptance cases (testscript)
```

## Running the tests

```text
go vet ./...
go test ./...                                        # unit tests; works offline
go test -race ./internal/serve/ ./internal/cli/      # the packages that run things at the same time
go test -tags acceptance ./internal/acceptance/...   # the binary, through the scripts of testdata/script
```

`make check` runs all of these and the formatting check below. The same checks run in `.github/workflows/ci.yml`: on Linux and Windows at every push,
and on macOS once a week and on request (in a private repository a minute of macOS costs ten). The
Windows job lists the tests that skip themselves there; the test of the groups of processes (E4) is
not built for Windows at all, because that part does not exist there yet.

### The record of the language

`internal/lang/characterization_test.go` runs hundreds of programs, good and bad, through the lexer,
the parser and the checks, and compares what comes out (the tokens, the tree, and every message with
its line, its column and its advice) with `internal/lang/testdata/characterization.golden`. It says
nothing about whether the record is right, only that it did not change, which is what makes it safe to
rewrite how those parts are written. When a change of behavior is meant, `make characterize` rewrites
the record; read the difference of the file before committing it. `make characterize-cover` lists the
blocks of the lexer, the parser and the checks that the programs never run.

### Formatting

The sources have to be formatted with `gofmt`, which only changes spaces, the alignment of
comments and fields, and the order of the imports inside a block; it never changes what the code
does. `make fmt` rewrites the files, and `make fmtcheck` fails, naming the files, if any of them
needs it. A file that comes from outside (for example the zip of a new slice) may not be formatted,
so run `make fmt` after copying it over the project.

## How the Rust tests were ported

| Rust test | Class | Go test |
| --- | --- | --- |
| `diagnostics_golden` (all 10) | A, B | `check_test.go`, `runtime_test.go`, `golden_*.txt`, `run_errors.txt`. The missing-model test became "this agent needs a language model to think, and none is set up" |
| `parser_tests` (all 13) | A, E | `parser_test.go`, `parser_errors.txt` |
| `example_size`, `examples_check` | A, B | `examples_test.go`, `examples.txt` |
| `env_scope` | A, B | `tools_test.go`, `run_http_and_env.txt` |
| `builtin_tools` | B | `runtime_test.go`, `run_files.txt` |
| `permissions` | B | `file_test.go`, `runtime_test.go`, `run_files.txt` |
| `http_state_tools` | D | `http_test.go`, `runtime_test.go` (the three D tests, rewritten for H1 and S7) |
| `timeout` | C | `runtime_test.go`, using `clock.wait` instead of an MCP double |
| `internal_error` (2 of 3) | B | `cli_test.go`, `new_check.txt`, `run_basic.txt` |
| `dynamic_link`, `link_cycle`, `link_interface` | B, C | `link_test.go`, `link_*.txt`. The MCP double of `dynamic_link` was replaced by a plain agent: the point of that test is the change picked up without editing the caller |
| `mcp_client` | C | `internal/mcp/pool_test.go` (a real server built with the SDK, started from the test binary itself) and `mcp_end_to_end.txt` |
| `llm_provider`, `think` | C | `internal/llm/llm_test.go` (servers that pretend to be the providers), `internal/runtime/think_test.go` (a scripted model) and the end to end tests of `cli_test.go` |
| `mcp_server` | C | `internal/serve/mcp_test.go` (a client of the SDK, in memory) and `internal/cli/serve_stdio_test.go` (the whole command, through pipes) |
| `sample_city_briefing` | C | pending |
| `contract/*`, `a2a_*`, `serve_*` | B | covered by the tests of `internal/serve` and `internal/cli`; a port of the original ones is pending |
| `internal_error::an_internal_failure...` | E | `internal/cli` (`TestAnInternalFailureShowsOneSentence...`), `internal/runtime/internal_test.go` |
| `perf` | B, C | partly: `internal/serve/load_test.go` (50 requests at once with `-race`, the 65th task, 100 slow connections); a port of the original is pending |

## What comes next

1. **The missing part of S10** (MCP over HTTP, behind the same door), which needs to be tried before it is
   promised. The CI on Linux, macOS (once a week) and Windows is done. **E4 on Windows** is left out on
   purpose (see its row above); to take it up, the program has to be started with the pipes of this
   project, put in a job object right after `Start`, and ended the way the SDK ends it.
2. **The port of the tests of the original project** (`contract/*`, `a2a_*`, `sample_city_briefing`,
   `perf`).
3. **Checks in real conditions** that cannot be automated (next section), most of which were done.
4. **Quality:** the functions that Sonar marked, in the code and in the tests, are split, and the copies
   of `shorten` are one function (`internal/clip`). Run Sonar again to see what is left; the
   record of the language (above) is what makes the next change in `internal/lang` safe.
5. **Distribution:** binaries for the three systems, a version number (it is `0.0.0-dev`), a
   reference of the language for people who write agents, `CONTRIBUTING` and `CHANGELOG`.
6. **Smaller decisions:** a smaller default for the memory a conversation may use (see Memory
   above), `--token-file`, a real TOML library in place of the small reader, and a review of the
   security of `serve` by someone who did not write it, before it is exposed to the internet.

### Not verified yet

Checked against the real thing (the steps and the results are in `validation/README.md`):

- the MCP server, with the MCP Inspector, a client of another team, in TypeScript;
- a tool server started by `npx`, with the minimal environment of E1: what the shell had did not reach it;
- `think` with a model that speaks the format of OpenAI, on this computer (Ollama), with tools;
- `--behind-proxy`, with a reverse proxy (Caddy) and a certificate that the client checks;
- the A2A server, with the command line of the official A2A SDK in JavaScript.

These work in the tests, which use doubles, or were run only on macOS, and are not checked yet:

- the MCP server with a desktop assistant (Claude Desktop); only the Inspector was tried;
- `think` with Azure, or another provider of the format of OpenAI that is not on this computer;
- a tool server started with `uvx`;
- `--public`, with a certificate of its own;
- the A2A client with servers of third parties: it was tried only with the SDK;
- the end of the group of processes of a tool server (E4): tested on macOS and, in the CI, on Linux;
  on Windows it does not exist yet;
- Windows: the CI builds and tests it (without the race detector), and the tests of symbolic links run there
  and pass: the file tool does not leave its folder through a link, the approvals are not hidden behind one
  and the log does not follow one. Five tests skip themselves there, each for its own reason: one is about
  a file system that tells upper and lower case apart, which Windows does not; two ask for the permissions of
  Unix (`rwx` for others), and their protection is not checked on Windows, which has ACLs; one is the end of
  the group of processes of a tool server (E4), which does not exist there yet; and one is the detection of hard
  links (F7).

Not done on purpose: isolating a program at the level of the operating system (a
sandbox). The SDK gives a minimal environment, not a sandbox; the approval is the
protection until that is decided.

Decisions that were open and are now taken:

- **Where credentials go (Q2):** in `[credentials]` of `metagente.toml`, as the *name* of the
  variable that holds the token; never the token itself.
- **The Agent Card (Q3):** protected by the token. `--public-card` gives anyone a minimal one
  (names only).
- **TLS (Q4):** `--public` needs TLS of its own. `--behind-proxy` only counts when the server
  listens on this same computer.
- **The scope of `state` (Q5):** one memory for each conversation, and the server issues the
  id of a conversation. There is no way to share a memory between conversations for now.

## License

Apache License 2.0, the same as the original project. See `LICENSE` and `NOTICE`.
