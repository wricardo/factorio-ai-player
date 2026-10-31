#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

go_files=$(git ls-files -co --exclude-standard '*.go')
if [[ -n "$go_files" ]]; then
  unformatted=$(gofmt -l $go_files)
  if [[ -n "$unformatted" ]]; then
    printf 'gofmt required:\n%s\n' "$unformatted" >&2
    exit 1
  fi
fi

tmp=$(mktemp -d "${TMPDIR:-/tmp}/factorio-ai-player-check.XXXXXX")
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/repo"
rsync -a \
  --exclude='.git' \
  --exclude='.factorio' \
  --exclude='.beads' \
  --exclude='.omp' \
  --exclude='play/harness/agents' \
  ./ "$tmp/repo/"
(
  cd "$tmp/repo"
  bash scripts/generate.sh
)

stale=0
for generated in "$tmp/repo"/graphql_generated.go "$tmp/repo"/graphql_models.go "$tmp/repo"/graphql_*.resolvers.go; do
  name=${generated#"$tmp/repo/"}
  if [[ ! -f "$name" ]] || ! cmp -s "$name" "$generated"; then
    echo "generated file is stale: $name" >&2
    stale=1
  fi
done
if (( stale != 0 )); then
  echo 'run: just generate' >&2
  exit 1
fi

go vet ./...
go build ./...
go test ./...
