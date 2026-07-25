.PHONY: build test fuzz lint vendor all

all: lint test

build:
	go build ./...

test:
	go test ./...

# Byte-identical round-trip is the property under test — see the implementation
# plan §7. Go runs one fuzz target per invocation, hence the separate lines.
# doc's targets arrive with M0b.
FUZZTIME ?= 30s

fuzz:
	go test -run '^$$' -fuzz FuzzMetaRoundTrip -fuzztime $(FUZZTIME) ./meta
	go test -run '^$$' -fuzz FuzzMetaInsertIsAdditive -fuzztime $(FUZZTIME) ./meta
	go test -run '^$$' -fuzz FuzzMetaFormatParse -fuzztime $(FUZZTIME) ./meta

lint:
	go vet ./...
	@test -z "$$(gofmt -l .)" || { echo "gofmt needed in:"; gofmt -l .; exit 1; }

# Refresh vendored frontend assets (HTMX, Sigma.js/graphology) for package serve.
# No frontend build step exists or should exist — assets are committed and
# go:embed-ed (harvest R9).
vendor:
	@echo "not yet implemented — vendored assets land with M2 (serve)"
