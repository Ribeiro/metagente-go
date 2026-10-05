# Changelog

What changes in a way that a person who uses Metagente can notice is written here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the versions follow
[Semantic Versioning](https://semver.org/): while the first number is 0, anything may change between two
minor versions.

## [Unreleased]

### Added

- `metagente serve --mcp`: the agents as MCP tools over HTTP too, at `/mcp`, behind the same door and the
  same token as A2A (S10). A session is a conversation with each agent; it ends when the client says so or
  after `task_retention_seconds` without use, and what its agents kept is let go. The answer comes in the
  answer to the POST, as JSON; a stream of the server (`GET`) is refused with `405`. There are no more
  sessions at once than `max_retained_tasks` (one more is a `503`).
- The access log says which agent, message and task an MCP call ran, and how it ended (`rpc=mcp`).

### Changed

- A2A and MCP share `max_running_tasks` and `max_retained_tasks`: they are limits of the server, not of
  each protocol.

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

[Unreleased]: https://github.com/Ribeiro/metagente-go/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/Ribeiro/metagente-go/releases/tag/v0.1.0
