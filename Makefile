.PHONY: fmt check test test-race test-kon build

fmt:
	gofmt -w cmd internal

# The pre-commit gate: formatting, vet, and shuffled unit tests.
check:
	@test -z "$$(gofmt -l cmd internal)" || (gofmt -l cmd internal; exit 1)
	go vet ./...
	go test -shuffle=on ./...

test:
	go test ./...

test-race:
	go test -race ./...

# Round trip with a real kon; KON is a kon with `kon acp` (v0.1.15 or later).
test-kon:
	INARI_KON=$(KON) go test -count=1 -run TestRealKon -v ./internal/acp/

build:
	go build -o bin/inari ./cmd/inari
