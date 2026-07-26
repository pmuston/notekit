.PHONY: build test race fuzz lint vendor all notefmt noterun check-corpus

all: lint test

build:
	go build ./...

# notefmt is the M0 deliverable and stays useful as a linter.
notefmt:
	go build -o notefmt ./cmd/notefmt

# The M1 demo: the full async loop with no server involved.
noterun:
	go build -o noterun ./cmd/noterun

# Lint the corpus with the tool itself: a self-check that the acceptance suite's
# own files are clean.
check-corpus: notefmt
	./notefmt check doc/testdata/corpus/*.md

test:
	go test ./...

# The scheduler runs one goroutine per notebook, so data races are a real failure
# mode rather than a theoretical one.
race:
	go test -race ./...

# Byte-identical round-trip is the property under test — see the implementation
# plan §7. Go runs one fuzz target per invocation, hence the separate lines.
FUZZTIME ?= 30s

fuzz:
	go test -run '^$$' -fuzz FuzzMetaRoundTrip -fuzztime $(FUZZTIME) ./meta
	go test -run '^$$' -fuzz FuzzMetaInsertIsAdditive -fuzztime $(FUZZTIME) ./meta
	go test -run '^$$' -fuzz FuzzMetaFormatParse -fuzztime $(FUZZTIME) ./meta
	go test -run '^$$' -fuzz FuzzDocParse -fuzztime $(FUZZTIME) ./doc
	go test -run '^$$' -fuzz FuzzDocSetResult -fuzztime $(FUZZTIME) ./doc
	go test -run '^$$' -fuzz FuzzDocAssignID -fuzztime $(FUZZTIME) ./doc

lint:
	go vet ./...
	@test -z "$$(gofmt -l .)" || { echo "gofmt needed in:"; gofmt -l .; exit 1; }

# Refresh vendored frontend assets (HTMX, Sigma.js/graphology) for package serve.
# No frontend build step exists or should exist — assets are committed and
# go:embed-ed (harvest R9).
vendor:
	@echo "not yet implemented — vendored assets land with M2 (serve)"
