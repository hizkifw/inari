.PHONY: fmt check test test-race test-kon build release tag clean

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

# Builds every release archive into dist/, as CI does for a tag.
release:
	@test -n "$(VERSION)" || (echo "VERSION is required, for example VERSION=v0.1.0"; exit 1)
	./scripts/release.sh "$(VERSION)"

# kon tags the release, following the Releases section of AGENTS.md.
tag:
	kon run --incognito $(if $(MODEL),--model $(MODEL)) \
	  "Tag the next $(or $(BUMP),patch) release of inari, following the Releases section of AGENTS.md. Do not push the tag."

clean:
	rm -rf bin dist
