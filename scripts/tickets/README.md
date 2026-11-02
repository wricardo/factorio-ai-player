# Unattended ticket runner

Every 30 min launchd runs `run.sh`, which claims **one** `bd ready` ticket via `omp -p` against `prompt.md`, validates, closes it, and exits.

Install: `mkdir -p ~/.cache/factorio-ai-player-tickets/logs` then
`launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/sh.omp.tickets.plist` (after copying the plist there).
Pause/resume: `launchctl bootout gui/$(id -u)/sh.omp.tickets` — nothing runs while it is booted out.
Logs: `~/.cache/factorio-ai-player-tickets/logs/run-YYYY-MM-DD.log` (full stdout+stderr per run, timestamped) and `launchd.out.log`/`launchd.err.log`.
Lock: `mkdir ~/.cache/factorio-ai-player-tickets/lock`; a second fire exits 0 immediately, and the trap removes it on exit. A crash leaves it behind — delete the dir to unstick.
Dry run: `OMP=echo bash scripts/tickets/run.sh`.

Failure modes: each run is a cold session with no memory, so the ticket text must be self-contained — vague tickets get noted and unclaimed, not guessed at.
Game tickets mutate a shared save non-transactionally; a run killed mid-build can leave half-placed entities, so the prompt re-reads before writing and verifies before retrying.
A `just check` failure caused by someone else's uncommitted file is recorded, never "fixed"; the runner never commits, pushes, or restarts the server.
