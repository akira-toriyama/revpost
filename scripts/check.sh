#!/bin/sh
# check.sh — the full local verification, runnable by you or by Claude Code with
# no TTY. Mirrors what CI enforces (build.yml → shared go-ci reusable: module
# hygiene / build / vet / race-test / lint; plus govulncheck), so a green run
# here means a green CI. GOTOOLCHAIN=local uses whatever toolchain is installed;
# the go.mod floor is a supported minor and CI's go-version-file pins it.
set -eu
cd "$(dirname "$0")/.."
export GOTOOLCHAIN=local

echo "→ module hygiene (go mod tidy -diff + verify)"
go mod tidy -diff
go mod verify

echo "→ go build"
go build ./...

echo "→ go vet"
go vet ./...

echo "→ go test -race"
go test -race ./...

# Bounded fuzzing actually mutates inputs; `go test -race` only replays the seed
# corpus. These pure-core targets assert the agent-facing stdin path never panics.
echo "→ fuzz (bounded, 15s each)"
go test -run='^$' -fuzz='^FuzzParseInput$' -fuzztime=15s ./internal/core
go test -run='^$' -fuzz='^FuzzParseRDJSON$' -fuzztime=15s ./internal/core
go test -run='^$' -fuzz='^FuzzBuildCommentSet$' -fuzztime=15s ./internal/core

if command -v golangci-lint >/dev/null 2>&1; then
  echo "→ golangci-lint"
  golangci-lint run ./...
else
  echo "→ golangci-lint (skipped — not installed; CI runs it)"
fi

if command -v govulncheck >/dev/null 2>&1; then
  echo "→ govulncheck"
  govulncheck ./...
else
  echo "→ govulncheck (skipped — not installed; CI runs it)"
fi

echo "→ build binary for live checks"
go build -o bin/revpost ./cmd/revpost
BIN="$(pwd)/bin/revpost"

echo "→ smoke: --version / --help"
"$BIN" --version >/dev/null
"$BIN" --help >/dev/null
echo "✓ all checks passed"
