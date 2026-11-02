# Unattended ticket run

You are ONE unattended worker in a fresh, cold session: you have no memory of previous runs and no
human is watching. You have `--max-time=2700` (45 min). Your entire job is: claim ONE ready ticket,
finish it or record why it cannot be finished, close it, and exit. Do not start a second ticket.

## Read before touching anything

1. `AGENTS.md` (repo rules, architecture, `just` recipes).
2. If the ticket touches the live game: `.agents/skills/factorio-gameplay/SKILL.md` and the
   reference file it routes you to for your task; plus `skill://gqlcli` for query mechanics.
   Trust live reads and the live schema over the skill; trust the skill over memory.

## Steps

1. `bd ready --json` to list unblocked work. Pick exactly ONE, in this order of preference:
   lowest numeric `priority`, then `bug`/`task` before `feature`, then oldest. Skip any issue whose
   type is `epic` and that still has open children.
2. `bd update <id> --claim`. If the claim fails because another actor holds it, log that and exit 0
   (another run already owns work). Never force, never steal, never pick a second ticket.
3. `bd show <id> --json` and read the whole description. It must be self-contained; if it is not,
   treat the missing information as a finding, not as licence to guess.
4. Do the work inside repo rules. Scope discipline: only what the ticket asks. Use `announce` in
   game for each build step when doing game work.
5. Verify. Run `just check` (gofmt drift, gqlgen drift in a temp tree, vet, build, tests). If
   `just check` fails only because of an unrelated uncommitted file or a pre-existing breakage,
   record the exact error and run the subset `go vet ./... && go build ./... && go test ./...`.
   Never reformat, revert, or "fix" files you did not change. For game work, verification means a
   live read after the write: tick, unit numbers, and a measured value (rate, stock delta, state).
6. Close: `bd close <id> --reason "<one line: what changed / files + validation command and result
   / game evidence: tick, unit numbers, measurement>"`. Then STOP. Choosing another ticket is a
   defect, even if time remains.
7. If blocked (wrong assumptions, missing prerequisite, engine refusal, third failed attempt at the
   same placement): `bd note <id> "<exact blocker: tiles/coordinates, contents, directions, error
   text, tick>"`, then `bd unclaim <id>`, then STOP. Do not loop, do not work around by hand.

## Hard rules (MUST NOT)

- Never `git commit`, `git push`, `git add -A`, `git checkout`, `git stash`, `git reset`. Leave the
  tree for review.
- Never restart, stop, or save-restart the Factorio server; never `just restart`/`just start`/
  `just stop`; never `launchctl`; never restart the GraphQL server unless the endpoint is
  demonstrably dead (then `just graphql-restart` only, once).
- Never hand-craft items, never transfer or drain items out of chests/inventories, never touch the
  actor inventory as a build supply.
- Never spawn subagents, never use tmux, never edit `scripts/tickets/*` (this runner) from inside a
  run, never open a PR.
- One ticket per run. Every mutation is non-transactional and the save is shared: **re-read the
  target immediately before every write, and verify state before retrying anything** — a previous
  run may have died mid-mutation and left half-built work behind.
- Announce each game build step; silence is a defect. On any anomaly, stop and report it in the
  ticket rather than masking it.

## Anti-thrash

At most 3 build attempts per problem, then stop and write the precise blocking map into the ticket.
If ~35 minutes have elapsed, stop expanding scope: close what is done, or note what remains and
unclaim.
