#!/usr/bin/env bash
# Unattended ticket runner: one `omp` print-mode turn against scripts/tickets/prompt.md.
# Lock-guarded so overlapping launchd fires (or a manual run) never collide.
# Nothing here touches Factorio; the prompt owns the repo/game rules.
set -uo pipefail

HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO="${TICKETS_REPO:-$(cd -- "$HERE/../.." && pwd)}"
STATE_DIR="${TICKETS_STATE_DIR:-$HOME/.cache/factorio-ai-player-tickets}"
LOG_DIR="$STATE_DIR/logs"
LOCK_DIR="$STATE_DIR/lock"
PROMPT="$REPO/scripts/tickets/prompt.md"

mkdir -p "$LOG_DIR" || exit 1
DAY="$(date +%Y-%m-%d)"
LOG="$LOG_DIR/run-$DAY.log"

# Atomic lock: mkdir either succeeds or fails; no PID races, no stale-file guessing.
if ! mkdir "$LOCK_DIR" 2>/dev/null; then
  echo "tickets: lock held at $LOCK_DIR, skipping run" >&2
  exit 0
fi
trap 'rmdir "$LOCK_DIR" 2>/dev/null' EXIT INT TERM HUP

if [[ ! -f "$PROMPT" ]]; then
  echo "tickets: missing prompt $PROMPT" >&2
  exit 2
fi

cd -- "$REPO" || exit 1

{
  printf '\n===== run start %s (pid %s, repo %s) =====\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$$" "$REPO"
  printf 'git status (first 15 lines, for the record):\n'
  git status --porcelain 2>/dev/null | head -n 15
} >>"$LOG" 2>&1

# OMP may be overridden (e.g. OMP=echo) for dry runs.
OMP_BIN="${OMP:-omp}"
"$OMP_BIN" -p --auto-approve --max-time=2700 -- "@$PROMPT" >>"$LOG" 2>&1
code=$?

printf '===== run end %s exit=%d log=%s =====\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$code" "$LOG" >>"$LOG" 2>&1
echo "tickets: exit $code (log $LOG)"
exit "$code"
