# Working on Metagente (the Go port)

Notes for an AI assistant, and for people, who work on this repository. The rules of the project itself
are in [CONTRIBUTING.md](CONTRIBUTING.md); this page adds how the work is done here.

## Language

- Talk to the maintainer in Portuguese (Brazil).
- Code, comments, tests, messages, commit messages and documents are in English, in plain words: a person
  who does not program should be able to read the documents.
- The titles and descriptions of pull requests are written in Portuguese.

## Before changing anything

- A design in `docs/` is a design, not a request. Do not build it until the maintainer asks. Today that
  is [`docs/design-async-elt.md`](docs/design-async-elt.md).
- Do not run `make characterize` unless the maintainer asks: it rewrites the record of the language.
- Never ask the maintainer to paste a token or any other secret in the chat.

## How a change goes in

- Work on the branch the session names. Open a pull request for what the maintainer asked for, tell the
  maintainer when the CI ends, and **do not merge until the maintainer says "Faça o merge"**.
- After a merge, bring the branch to the new `master`:
  `git fetch origin master && git merge --ff-only origin/master && git push -u origin <branch>`. Then stop
  watching that pull request.
- Before pushing code: `make fmt`, then `make check`. If it cannot run, at least `gofmt -l .`,
  `go vet ./...` and `go test ./...`. The check "CI passed" is the gate, and `govulncheck` and Sonar run
  inside the CI.
- The commits are authored by the maintainer (`git log -1 --format='%an <%ae>'` shows the name and the
  e-mail to set with `git config user.name` and `user.email`; set them again in a new clone), with the
  trailers that the session asks for.
- A change that people can notice goes in `CHANGELOG.md`, under `## [Unreleased]`. A change of behavior
  is also in the requirements table of `README.md` and in `docs/LANGUAGE.md`.
- Every whole agent shown in `docs/LANGUAGE.md` and `docs/tutorial.md` must pass `metagente check`: a
  test (`internal/lang/docs_test.go`) reads them.

## Releases

An assistant opens the pull request "Release X.Y.Z": the section of `CHANGELOG.md` with its compare link,
and the version in `README.md` and in `samples/city-briefing`. After it is merged, **the maintainer** makes
the tag. The steps, and why `go install` waits for the tag, are in the section "A release" of
[CONTRIBUTING.md](CONTRIBUTING.md).

## Tests

- A part that talks to another program is tried against the real thing too, and the steps go in
  `validation/README.md` (see CONTRIBUTING).
- **The integration tests of the asynchronous ELT, when it is built, use Testcontainers**
  (`testcontainers-go`): PostgreSQL, MySQL/MariaDB and NATS with JetStream in containers, with the images
  pinned by digest. They live in a module of their own (`integration/`, with its own `go.mod`), so the
  product does not take their dependencies; they run the compiled binary, carry the build tag
  `integration`, run only in the Linux job of the CI, and are skipped when there is no container runtime.
  SQLite and the fake broker stay in the unit tests. The details are in section 16 of the design.
