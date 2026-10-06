# Contributing

## What is needed

Go 1.26 or newer, and `make`. `make check` runs what the CI runs: the format, `go vet`, the tests, the
tests with `-race` of the packages that run things at the same time, and the tests of the binary.

## The rules of this project

- Code, comments, tests and messages are written in English.
- Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/): `fix:`, `feat:`,
  `docs:`, `test:`, `refactor:`, `ci:`.
- Run `make fmt` before a commit; `make check` fails if a file is not formatted.
- A change comes with a test, and the test is read before it is trusted: one that cannot fail is not a test.
- A message to a person says what is wrong and how to fix it (`Problem` and `Fix`), and never repeats a
  secret. What comes from outside goes through `internal/clip` before a message repeats it.
- A part that talks to another program (MCP, A2A, a model, a proxy) is not checked by tests that use doubles
  alone. Try it against software that others wrote, and write the steps and what they showed in
  `validation/README.md`: six of the eleven checks there found a defect that no test had.

## The language

`internal/lang` has a record of what the lexer, the parser and the checks do with a large set of programs,
good and bad: `internal/lang/testdata/characterization.golden`. It does not say that the answer is right,
only that it did not change, and that is what makes it safe to change how those parts are written. If a change
is meant to alter what they do, run `make characterize` and read the difference of the file before committing
it. `make characterize-cover` lists what the programs do not reach.

## A problem of security

Do not open a public issue. Use "Report a vulnerability", in the Security tab of the repository.

## A release

1. Write what changed in `CHANGELOG.md`, under `## [0.2.0] - DATE`, and add its link at the end.
   `scripts/release-notes.sh 0.2.0` prints what the release will say.
2. Tag it on `master`: `git tag v0.2.0 && git push origin v0.2.0`.
3. The workflow `Release` runs the tests, builds an archive for each system with `make dist` (the version
   inside the binaries is the one of the tag), and publishes the release with the archives, `SHA256SUMS` and
   the notes. A version without its section in `CHANGELOG.md` is not published.
