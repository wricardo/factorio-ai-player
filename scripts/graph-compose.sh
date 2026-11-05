#!/usr/bin/env bash
# Run docker compose against the Memgraph stack, with either the compose plugin
# (`docker compose`) or the standalone `docker-compose` binary.
set -euo pipefail
cd "$(dirname "$0")/.."
file=memgraph/docker-compose.yml
if docker compose version >/dev/null 2>&1; then
    exec docker compose -f "$file" "$@"
elif command -v docker-compose >/dev/null 2>&1; then
    exec docker-compose -f "$file" "$@"
fi
echo "error: neither 'docker compose' nor 'docker-compose' is available" >&2
exit 1
