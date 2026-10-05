# Phase 1 spikes

> **A record, not a part of the program.** These were the first experiments with the two SDKs, before any
> SDK code went into `metagente`. The decisions they were made for are taken, and are in the README of the
> project (E1, E3, E4, S2 to S6, P4, and the choice of the A2A client written by hand). The module still
> compiles with the versions of the SDKs that the program uses (`go vet ./...` passes in this folder), and
> is kept to show why each decision was taken. Nothing in it is run by `make check` or by the CI.

Small tests that turn what the documentation of the two official SDKs *says*
into what they *do*, before any SDK code goes into `metagente`. This is a
separate Go module, so `go test ./...` of the project does not run it, and it
can be deleted when the decisions are made.

Versions read when this was written: MCP Go SDK `v1.8.0`, A2A Go SDK `v2.6.0`.
The documentation page of each flagged those as not being the latest, so newer
versions exist. Run these as they are first; upgrade afterwards, one SDK at a
time, and run them again.

When this was first written nothing here had been compiled, and the signatures came
from the documentation. They were fixed since, and the module compiles.

## Running

```text
cd spikes
go mod tidy           # needs the network: downloads the two SDKs
go vet ./...
go test -v ./...
```

Add `-run V1` (or any name) to run one. The file `winroot_test.go` compiles only
on Windows.

## What each result decides

| Test | Question | If it fails |
| --- | --- | --- |
| V1 | Can several calls run at once on one MCP session? | Use one session per concurrent call, or a small pool (E3) |
| E1 | Does the child see only the environment we give it? | Build the `exec.Cmd` ourselves and verify `Env` (E1) |
| E4 | Does closing the session end the child in bounded time? | Wrap `Close` with our own kill after a deadline (E4) |
| V2 | Which MCP protocol versions does the SDK speak, and can a client ask for an older one? | Pin the version in `.ag` or in the config |
| V3 | Can our authentication sit in front of the A2A handler, with the Agent Card left public? | Use the SDK's call interceptor instead of an HTTP middleware |
| V5 | Does `http.CrossOriginProtection` behave as the plan assumes, alone and inside the MCP handler? | Write the check ourselves (S4) |
| S2, S3, S6 | Does the MCP handler refuse a wrong `Content-Type`, a rebinding `Host` and a huge body? | Add our own middleware in front of it |
| V6 | Do large multiline texts cross stdio and the A2A message types untouched? | Encode the arguments (for example as a single JSON string) |
| P4 | Does a panic in the agent leave the server alive? | Add our own `recover` around the executor |
| V4 | How does `os.Root` treat `NUL`, `CON`, `file:stream`, trailing dots and `\\?\` paths? (Windows only) | Add our own name check on top (F3) |

## Not covered, on purpose

- **D2, call depth across processes.** It needs the metadata field of the A2A
  request, and the documentation read did not show its name. Look at it with
  `go doc github.com/a2aproject/a2a-go/v2/a2a SendMessageRequest` and
  `go doc github.com/a2aproject/a2a-go/v2/a2a Message`, then a test is a few lines.
- **An independent MCP server (interoperability).** These tests talk to a server
  built with the same SDK, so they cannot find a disagreement between two
  implementations. That needs a server written with another SDK, for example the
  reference one in Python or TypeScript.
- **TLS.** The standard library does it (`ListenAndServeTLS`); there is nothing
  to find out about the SDKs.
