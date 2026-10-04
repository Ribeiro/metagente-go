.PHONY: all fmt fmtcheck vet test race acceptance check characterize characterize-cover

all: fmt vet test

# Rewrites the files that are not formatted. It only changes spaces, alignment and the
# order of the imports inside a block, never what the code does.
fmt:
	gofmt -l -w .

# Fails, naming the files, if any of them is not formatted.
fmtcheck:
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then echo "These files need gofmt (run: make fmt):"; echo "$$unformatted"; exit 1; fi

vet:
	go vet ./...

test:
	go test ./...

# The packages that run things at the same time.
race:
	go test -race ./internal/serve/ ./internal/cli/

# The binary, through the scripts of testdata/script. Behind a build tag.
acceptance:
	go test -tags acceptance ./internal/acceptance/...

# Everything, in the order that stops soonest.
check: fmtcheck vet test race acceptance

# The record of what the lexer, the parser and the checks do with a large set of programs
# (internal/lang/characterization_test.go). Rewrite it only when a change of behavior is meant,
# and read the difference of testdata/characterization.golden before committing it.
characterize:
	go test ./internal/lang -run TestCharacterization -update-characterization

# The blocks of the lexer, the parser and the checks that those programs never run.
# Each line is file:start,end, followed by how many statements are in the block.
characterize-cover:
	go test ./internal/lang -run TestCharacterization -coverprofile=/tmp/lang-characterization.out
	@awk -F'[: ]' '$$NF==0 && $$1 ~ /lang\/(lexer|parser|check)\.go$$/ {print $$1":"$$2" ("$$3" statements)"}' /tmp/lang-characterization.out
