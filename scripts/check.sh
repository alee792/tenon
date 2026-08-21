#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_root"

if ! command -v go >/dev/null 2>&1; then
  echo "go is required; install the toolchain pinned in go.mod" >&2
  exit 1
fi

unformatted=$(gofmt -l .)
if [ -n "$unformatted" ]; then
  echo "gofmt is required for:" >&2
  echo "$unformatted" >&2
  exit 1
fi

go vet ./...
go test -race ./...
