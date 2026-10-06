# Changelog

What changes in a way that a person who uses Metagente can notice is written here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the versions follow
[Semantic Versioning](https://semver.org/): while the first number is 0, anything may change between two
minor versions.

## [Unreleased]

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

[Unreleased]: https://github.com/Ribeiro/metagente-go/compare/v0.3.1...HEAD
[0.3.1]: https://github.com/Ribeiro/metagente-go/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/Ribeiro/metagente-go/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/Ribeiro/metagente-go/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/Ribeiro/metagente-go/releases/tag/v0.1.0
