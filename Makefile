.PHONY: build bins test race fuzz lint vendor all notefmt noterun noteserve clinote sqlnote check-corpus demo clean

all: lint test

build:
	go build ./...

# Every command, into bin/. One static binary each, no runtime file dependencies
# beyond the notebook and its sidecar directory.
BINS = notefmt noterun noteserve clinote sqlnote

bins:
	@mkdir -p bin
	@for b in $(BINS); do \
		printf '  %-10s' "$$b"; \
		go build -o bin/$$b ./cmd/$$b && echo ok || exit 1; \
	done
	@echo "built $(words $(BINS)) binaries into bin/"

clean:
	rm -rf bin
	rm -f $(BINS)

# notefmt is the M0 deliverable and stays useful as a linter.
notefmt:
	go build -o notefmt ./cmd/notefmt

# The M1 demo: the full async loop with no server involved.
noterun:
	go build -o noterun ./cmd/noterun

# The M2 demo: the full HTMX loop in a browser.
noteserve:
	go build -o noteserve ./cmd/noteserve

# clinote v2 (M3): the first real consumer — a shell executor plus a main.
clinote:
	go build -o clinote ./cmd/clinote

# sqlnote (gate 4): the second consumer, and the first non-shell domain.
sqlnote:
	go build -o sqlnote ./cmd/sqlnote

# Run the shipped example, which rewrites its own result blocks in place. The example
# is self-contained (no sqlnote-db), so it reconstructs its data from empty every time
# and leaves no database behind.
demo: bins
	./bin/notefmt list examples/parts.md
	@echo
	@echo "now: ./bin/sqlnote examples/parts.md   then open the URL it prints"

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
# Versions and sources are recorded in serve/assets/VENDOR.md; update it too, and read
# the diff — a vendored asset that changed silently is an unreviewed upgrade.
HTMX_VERSION ?= 2.0.4

vendor:
	curl -fsSL -o serve/assets/htmx.min.js \
		https://unpkg.com/htmx.org@$(HTMX_VERSION)/dist/htmx.min.js
	@echo "vendored htmx $(HTMX_VERSION); update serve/assets/VENDOR.md and review the diff"
