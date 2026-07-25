.PHONY: build test fuzz lint vendor all

all: lint test

build:
	go build ./...

test:
	go test ./...

# Fuzz targets arrive with M0a (meta) and M0b (doc); this target fails until then.
# Byte-identical round-trip is the property under test — see the implementation
# plan §7.
fuzz:
	go test -run '^$$' -fuzz FuzzMetaRoundTrip -fuzztime 30s ./meta
	go test -run '^$$' -fuzz FuzzDocRoundTrip -fuzztime 30s ./doc

lint:
	go vet ./...
	@test -z "$$(gofmt -l .)" || { echo "gofmt needed in:"; gofmt -l .; exit 1; }

# Refresh vendored frontend assets (HTMX, Sigma.js/graphology) for package serve.
# No frontend build step exists or should exist — assets are committed and
# go:embed-ed (harvest R9).
vendor:
	@echo "not yet implemented — vendored assets land with M2 (serve)"
