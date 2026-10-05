.PHONY: all fmt fmtcheck vet test race acceptance check characterize characterize-cover version build dist

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

# The version of a build comes from the git tag: v0.1.0 gives 0.1.0, and between two tags it is like
# 0.1.0-3-g56d1202. Without a tag it is 0.0.0-dev+ and the commit. In both cases -dirty is added if files
# were changed, so a build of files that are not in a commit does not say that it is one.
# `make dist VERSION=1.2.3` gives it by hand.
ifndef VERSION
VERSION := $(shell v="$$(git describe --tags --dirty 2>/dev/null)"; if [ -n "$$v" ]; then echo "$$v" | sed 's/^v//'; else echo "0.0.0-dev+$$(git describe --always --dirty 2>/dev/null || echo unknown)"; fi)
endif
LDFLAGS := -s -w -X metagente/internal/cli.Version=$(VERSION)

version:
	@echo $(VERSION)

# The binary of this computer, with the version in it, in bin/.
build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/metagente ./cmd/metagente

# The binaries that are given to other people, each in an archive with the license, and the sums
# to check them. Nothing in them is C, so one computer builds all of them.
PLATFORMS := darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64

dist:
	rm -rf dist && mkdir -p dist
	@for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		name=metagente-$(VERSION)-$$os-$$arch; ext=""; \
		if [ "$$os" = windows ]; then ext=".exe"; fi; \
		mkdir -p dist/$$name; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$$name/metagente$$ext ./cmd/metagente || exit 1; \
		cp LICENSE NOTICE README.md docs/LANGUAGE.md dist/$$name/; \
		if [ "$$os" = windows ]; then (cd dist && zip -qr $$name.zip $$name); else tar -C dist -czf dist/$$name.tar.gz $$name; fi; \
		rm -rf dist/$$name; \
		echo "built $$name"; \
	done
	@cd dist && (sha256sum *.tar.gz *.zip 2>/dev/null || shasum -a 256 *.tar.gz *.zip) > SHA256SUMS
	@echo "the sums are in dist/SHA256SUMS"
