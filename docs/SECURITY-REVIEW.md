# A review of the security of `serve`

**Date:** 2026-10-05. **Scope:** `internal/serve` (the door, A2A, MCP over standard input and output and
over HTTP, the conversations, the listener, the access log, the token) and the parts of `internal/cli`
that start it. **Version:** the `master` of that day, after S10.

**What this review is not.** The README asks for a review "by someone who did not write it, before it is
exposed to the internet". This one was made by the same assistant that wrote MCP over HTTP (S10), reading
the rest of the code for the first time in that session. It looked for what an attacker could do, wrote a
test for each problem before changing anything, and kept the fixes small. It does not replace a review by
a person from outside, which is still recommended before `--public`.

## How it was done

Each part was read with one question: what can someone do who reaches the port **without** the token, and
what can someone do **with** it. Each finding below was shown by a test that failed before the fix and
passes after it.

## Found and fixed

| # | What | Who could do it | Fix | Test |
|---|---|---|---|---|
| R1 | A token of one repeated character (`kkkk...`, 40 of them) was accepted, and so was `password` five times: the length was checked, not whether it could be random | the person who sets the token, by mistake; then anyone who guesses it | a token needs at least 12 different characters. `metagente token` gives about 30, and 64 hexadecimal digits give 16 | `TestOnlyALongPlainTokenIsAccepted` |
| R2 | When the list of places that failed was full (1024), places were forgotten at random, the stopped ones too. Whoever was stopped for sending wrong tokens could get out by failing from 1024 other addresses, which IPv6 gives by the million | anyone who reaches the port | a stopped place is never forgotten before its minute ends; when the list is full of stopped places, a new one is not counted | `TestAPlaceThatIsStoppedCannotGetOutByFailingFromOtherAddresses` |
| R3 | A connection that the door turned away (no token, wrong Host, a browser) stayed open, idle, for up to `idle_timeout_seconds` (60). With a request each, a stranger could keep the `max_connections` (256) of the server busy | anyone who reaches the port | the door closes the connection after a refusal (`Connection: close`) | `TestTheDoorHangsUpOnWhomItTurnsAway`, `TestAConnectionThatWasTurnedAwayIsClosed` |

R1 changes what a person may notice: a token that was accepted before may now be refused, with a message
that says why and how to make a good one. It is in the changelog.

## Looked at and found sound

- **The token** is compared in constant time, after a hash of both sides, so neither the content nor the
  length shows. It is never in a log, an error or the access log; the tests look for it in each.
- **The door** checks, in this order, the Host (421), a browser (403), the token (401, and 429 after ten
  wrong ones in a minute), and only then the method, the query, the type and the size of the body. A
  stranger learns nothing about the rest: every path answers 401 without the token.
- **No browser** gets through: `Origin`, `Sec-Fetch-Site`, `Sec-Fetch-Dest` and `Sec-Fetch-User` are
  refused, there is no CORS and a preflight gets 405.
- **The body** is limited before it is read (`Content-Length`) and while it is read (`MaxBytesReader`).
  A request of A2A has at most 8 parts and 32 values, names of 64 characters, a chain of 32 agents and an
  id of 256 characters.
- **What a caller is told** about a failure is the sentence and the fix, never a file, a line or the
  source (P1), over A2A, over MCP and to a model.
- **The listener** speaks HTTP/1.1 only and TLS 1.2 or newer, with timeouts for the headers, the body and
  idle connections, a limit of connections, of the size of headers, and of requests at once.
- **Conversations and sessions** have ids issued by the server (128 bits for A2A, 130 for MCP), a limit
  that A2A and MCP share, and an end: a conversation that is not used expires, and what it kept is let go.
- **A panic** ends only the request or the call, and the cause goes to the log of the user.
- **The address in the card** never comes from the request (S8).
- **`--behind-proxy`** only listens on this computer, and turns off the slowing down, because all requests
  come from the proxy; `X-Forwarded-For` is never trusted.

## Open, with what is recommended

| # | What | Why it is not fixed here | What to do |
|---|---|---|---|
| O1 | Connections that send their headers slowly can still hold the places of the listener for `read_header_timeout_seconds` (5) each, and a stranger can open them again and again | it needs a limit of connections for each address, which behind a proxy would stop everyone, and choosing that limit is a decision | for `--public`, put the server behind a proxy that limits connections for each address, or add such a limit to the listener (off with `--behind-proxy`) |
| O2 | The call depth of D2 does not cross MCP over HTTP: a Metagente agent that calls another one through `tool x from mcp "https://.../mcp"` carries no chain, so a circle through MCP is not found | MCP has no place for the chain that both sides agree on | a circle ends anyway, at `timeout_seconds` and `max_running_tasks`; to find it, the chain could go in the `_meta` of the call |
| O3 | Whoever holds the token can use up to `max_retained_tasks` × `max_state_bytes` of memory (about 250 MiB with the defaults), and keep `max_running_tasks` calls running | the token is meant to give that much | on a small machine lower the two settings (see Memory in the README) |
| O4 | `--token-file` reads the token once, when the server starts | changing the token while it runs would need a decision about the requests that are running | restart the server to change the token |

## Not reviewed

The interpreter, the tools and the providers of models (`internal/runtime`, `internal/tools`,
`internal/llm`) were not part of this review, except where `serve` calls them. Their requirements (F, H,
L, E, T) have tests of their own, listed in the README.
