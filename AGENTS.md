# Repository Guidelines

## Project Overview

`factorio-ai-player` is a Go CLI that lets AI agents inspect and control Factorio 2.0 through GraphQL over administrative RCON. GraphQL executes in-process for `query`, `mutation`, and `script`; the optional `serve` command exposes the same schema over HTTP on `127.0.0.1:8080` with no authentication. Gameplay actions run Lua on the Factorio server, so mutations can affect the save and disable achievements. Treat RCON credentials as administrative secrets.

## Architecture & Data Flow

CLI commands in `main.go` route GraphQL operations through `graphql_cli.go` and gqlcli's inline executor into the gqlgen schema. Resolvers in `graphql_*_resolvers.go` delegate to hand-written helpers; `resolver.go` converts typed inputs to backend command arguments, calls the game-operation dispatcher, then normalizes and decodes JSON. `main.go` builds Lua and sends it over the RCON TCP protocol. Factorio executes Lua and returns JSON. Each backend call opens its own connection, so successful sessions are capped at six (`rconConcurrency`) and the rest queue with their deadline starting after the wait.

Navigation, shooting, belt throughput, and belt topology additionally call the included mod's remote interfaces (`factorio_ai_navigation`, `factorio_ai_actions`, `factorio_ai_belt_flow`, `factorio_ai_topology`); navigation and shooting jobs advance on game ticks. Topology sends only a `remote.call` when the mod provides `factorio_ai_topology` and otherwise falls back to byte-identical inline Lua, so an older loaded mod works but recompiles its prelude per query. Mod version is 1.2.0, topology protocol 3, so a save still holding the 1.1.0 prelude fails the probe and uses the fixed inline path; the live server keeps running the previously loaded mod until Factorio restarts.

A response carrying both data and errors prints the partial data, then fails with one message per error prefixed by its response path, and the CLI exits non-zero.

Mutations are not transactional: an error or lost response does not prove action failed or rolled back. Inspect game state before retrying. Nested GraphQL relationships may make separate RCON reads and are not atomic snapshots.

## Key Directories

- Repository root: Go CLI, GraphQL schemas, resolver/domain code, tests, and server configuration. Source is mostly organized as domain files, not nested Go packages.
- `mods/factorio_ai_navigation/`: Factorio Lua navigation mod and its Lua test harness.
- `scripts/`: managed server lifecycle script, invoked through `justfile`.
- `.agents/skills/factorio-gameplay/`: gameplay handbook, auto-loaded by the project gameplay agents in `.omp/agents/`; not general source documentation.

## Development Commands

```sh
go build ./...                 # compile packages
go test ./...                  # run Go tests
go run . help                  # CLI help
just                            # list server recipes
just start                      # start managed local Factorio server
just observe                    # open a separate client
just save                       # request server save
just logs                       # inspect server log
just stop                       # save and stop server
just graphql                    # serve the GraphQL schema over HTTP on 127.0.0.1:8080
just generate                   # regenerate gqlgen output after a schema change
just check                      # gofmt drift, gqlgen drift, vet, build, full tests
just test-integration           # real-engine sandbox suite
```

Example GraphQL call (server must be running; password comes from managed state):

```sh
FACTORIO_RCON_PASSWORD="$(cat .factorio/password)" go run . query '{ status { tick players { name } } }'
```

Global `--addr HOST:PORT` goes before the CLI command. Use `go run . queries --args --desc` and `go run . describe TypeName --desc` to discover the GraphQL contract. There is no separate linter: `just check` runs `scripts/check.sh`, which rejects `gofmt` drift, regenerates gqlgen in a temp copy to detect schema drift, then runs `go vet`, `go build`, and `go test`. `justfile` has `test` and `test-integration` but no build recipe.

## Starting a new sandbox game (fresh map)

Sandbox is a **scenario**, not a map preset: `factorio --create` cannot produce it, and
`scripts/server.sh` (behind `just start`) only accepts `--start-server <save>`. So convert
scenario → save once, then run that save with the normal managed workflow. Only one Factorio
instance may use the default write-data dir (`~/Library/Application Support/factorio/.lock`),
so the scenario instance runs in a throwaway dir on alternate ports.

```sh
S="$HOME/Library/Application Support/factorio/saves"
D=/tmp/factorio-new-sandbox
cd /path/to/factorio-ai-player
go build -o /tmp/fap-worker .            # CLI copy usable against the alternate RCON port

# 1. Stop the managed server first (stop saves the live state), then archive that save
just stop
cp "$S/ai-player-sandbox.zip" "$S/ai-player-sandbox-$(date +%Y%m%d-%H%M).zip"

# 2. Throwaway write-data dir with a copy of the bundled sandbox scenario
mkdir -p "$D/data/scenarios"
cp -R /Applications/factorio.app/Contents/data/base/scenarios/sandbox "$D/data/scenarios/"
printf '[path]\nread-data=__PATH__system-read-data__\nwrite-data=%s/data\n' "$D" > "$D/config.ini"
openssl rand -hex 24 > "$D/password"

# 3. Create the save from the scenario on alternate ports (game 34198, RCON 27016).
#    just world-settings-check must pass first: it refuses configs that would spawn biters.
just world-settings-check
nohup /Applications/factorio.app/Contents/MacOS/factorio -c "$D/config.ini" \
  --mod-directory "$HOME/Library/Application Support/factorio/mods" \
  --start-server-load-scenario sandbox \
  --map-gen-settings "$PWD/map-gen-settings-sandbox.json" \
  --map-settings "$PWD/map-settings.json" \
  --bind 127.0.0.1:34198 --rcon-bind 127.0.0.1:27016 \
  --rcon-password "$(cat "$D/password")" \
  --server-settings "$PWD/server-settings.json" > "$D/server.log" 2>&1 < /dev/null &
# wait until $D/server.log contains "Starting RCON interface"
FACTORIO_RCON_PASSWORD="$(cat "$D/password")" /tmp/fap-worker --addr 127.0.0.1:27016 \
  mutation 'mutation { save { response } }'          # shutdown alone does NOT save
FACTORIO_RCON_PASSWORD="$(cat "$D/password")" /tmp/fap-worker --addr 127.0.0.1:27016 \
  mutation 'mutation { shutdown { response } }'      # -> $D/data/saves/sandbox.zip

# 4. Install it as the managed save and start normally
cp "$D/data/saves/sandbox.zip" "$S/ai-player-sandbox.zip"
FACTORIO_SAVE="$S/ai-player-sandbox.zip" just start
rm -rf "$D"
```

Verify the result: `status { tick players { name } }` shows a small advancing tick and no
players, `characters { total }` is 0, and `entities` over ±100 tiles returns only natural
entities (resources/trees, ~3k) — no machines. `just observe` opens a client. The settings
disable enemies on Nauvis only; other Space Age surfaces generate their own when first visited.

Gotchas:

- `--start-server-load-scenario` searches only `<write-data>/scenarios`, hence the scenario copy;
  pass the same `--mod-directory` as the managed server so `factorio_ai_navigation` loads.
- Sandbox strips the connecting player's character: the client joins as god controller with
  `player.character == null`, so `player:` inputs that need a character (`place`, `fuel`,
  `transfer`) fail. Restore one with `/silent-command game.players[1].create_character()` or use
  `characterSpawn` for a detached worker with its own reach and inventory.
- `shutdown` sends `/quit` **without** saving; always `save` first. Resulting save name is the
  scenario name (`sandbox.zip`).
- Two servers sharing the default write-data dir fail with "Couldn't acquire exclusive lock";
  always `just stop` first.
- Revert to the freeplay save: `FACTORIO_SAVE="$S/ai-player.zip" just restart`.

## Code Conventions & Common Patterns

- Keep Go code idiomatic and `gofmt`-formatted. Use domain-named root files (`insights_stock.go`, `factory_machines.go`, `navigation.go`) rather than adding parallel abstractions.
- Define public behavior and inputs in `*.graphqls`; gqlgen resolver adapters delegate to hand-written domain helpers. Put game-specific reads and writes in Lua-building functions and validate inputs before RCON calls.
- Reuse the generic `command[T]` bridge in `resolver.go` for ordinary game operations; use domain helpers for specialized queries, polling, and Lua responses.
- Return errors with context; use `context.Context` for cancellable RCON calls and wait/poll loops. Tests inject fake RCON listeners or callbacks instead of requiring a live server.
- GraphQL inputs use camelCase; Go symbols use Go naming; backend command flags and Lua payload conventions are normalized at the resolver boundary.
- Edit schema/config and regenerate gqlgen outputs when GraphQL contracts change. Do not hand-edit `graphql_generated.go` or `graphql_models.go`.
- Keep mutations explicit and conservative. Do not blindly retry operations with uncertain outcomes; verify inventories, entities, or job status first.

## Multiple Agents on One Working Tree

Several agents (and the user) may edit this checkout and the managed server at the same time. Treat
the working tree as shared state.

- **Never rewrite shared state.** No `git reset`, `git reset --hard`, `git checkout -- <path>`,
  `git restore`, `git stash`, `git stash pop`, `git clean`, branch switching, rebase, or amend on a
  shared branch. These silently discard or relocate other agents' uncommitted work; a transient
  compile error from a mid-write `graphql_generated.go` is expected, not a reason to reset.
- **Never stage broadly.** No `git add -A`, `git add .`, or `git commit -a`; they sweep other
  agents' in-progress files into your commit. Stage only the exact paths you changed
  (`git add <file>...`) and inspect `git status` before committing.
- **Commit only when asked.** Other agents' work is usually uncommitted by design; the user commits
  the tree as a whole. Report your changed files and propose the commit instead.
- **Do not delete or rewrite files you do not own.** Unfamiliar new `*.graphqls`, `*_resolvers.go`,
  or domain files are someone else's work in progress: leave them, and never regenerate or reformat
  them out of existence.
- **Expect interleaved edits in generated files.** `graphql_generated.go`, `graphql_models.go`, and
  `graphql_schema.resolvers.go` accumulate everyone's fields. After `gqlgen generate`, only fill in
  your own panics and leave others' implemented resolvers untouched.
- **One live server, shared save.** Do not stop, restart, or reinstall the managed server (see the
  sandbox recipe) while others are playing; verify mutations with reads and net-zero edit/revert
  probes instead.

## Escalation & API Feedback

- **Stop and raise anomalies.** If a GraphQL query/mutation, Lua payload, RCON call, or in-game
  outcome looks wrong — unexpected error, null or default value where the contract promises
  data, mutation reported success but state unchanged, machine behavior contradicting the docs —
  stop that workstream and report to the user: exact command or GraphQL document, variables,
  tick, observed output, expected result. Do not silently work around it, guess a fix, hide it in
  a throwaway script, or blind-retry a mutation.
- **Separate tool defect from game behavior.** Only call it a bug after comparing the schema, the
  generated Lua, and the live response. Mark unresolved suspicions `[INFERENCE]` and state which
  query or log line would settle it.
- **Raise valuable API gaps.** When the GraphQL surface makes a real task awkward — missing read
  or relationship, ambiguous semantics/defaults, per-entity RCON fan-out, mutation that needs
  unrelated setup (reach, detached character) — record a concise proposal for the user: the need,
  the workaround used, the suggested field/input/behavior, and rough cost/blast radius. Do not
  implement it mid-task unless asked.

## Important Files

- `main.go`: process entry, game-operation dispatch, Lua execution, and RCON protocol.
- `graphql_cli.go`: GraphQL CLI commands and schema discovery.
- `schema.graphqls`, `navigation.graphqls`, `settings.graphqls`, `insights_*.graphqls`, and the feature schemas (`construction.graphqls`, `trains.graphqls`, `fluids.graphqls`, `agent_actions.graphqls`, `player_gear.graphqls`, `research_extra.graphqls`, `world_extra.graphqls`, `entity_meta.graphqls`, `writes_extra.graphqls`, `blueprint_catalog.graphqls`): GraphQL API and inline descriptions. Each feature schema generates its own `graphql_<name>.resolvers.go`.
- `resolver.go`, `graphql_*_resolvers.go`: resolver bridge and schema adapters.
- `gqlgen.yml`, `graphql_generated.go`, `graphql_models.go`: gqlgen configuration and generated output.
- `actions.go`, `production.go`, `insights_*.go`, `factory_*.go`, `navigation.go`: gameplay operations and read-only insights. Topology (`insights_topology.go`), machine diagnostics (`insights_diagnostics.go`), planning (`insights_planning.go`), research/construction/fluids (`insights_operations.go`), belt-flow measurement (`insights_belt_flow.go`, backed by the navigation mod's `belt_flow.lua`) and tile-grid placement probing (`placement_probe.go`, one RCON call per area) live in their own domain files.
- Newer write domains: `deconstruction.go`, `construction.go` (buildGhosts, logistic networks), `ghost_placement.go` (placeGhosts bulk ghost planning), `construction_orders.go` (deconstruction/upgrade order reads), `trains.go` (train reads/control, setDirection), `fluids.go` (setFluid, fluidStock), `agent_actions.go` (game speed, teleport, capsules, repair), `agent_shoot.go` (mod-backed shooting), `writes_extra.go` (dropItem), `entity_meta.go` (inspection metadata, tags, name tag), `player_gear.go` (equipment grid, actor logistic requests), `research_extra.go` (research queue, sandbox unlock), `world_extra.go` (surfaces, circuit signals), `recipe_machine.go`/`machineRecipe.go` (recipe drain, clearRecipe), `craft_plan.go` (craftPlan hand-craft dependency planning and execution), `blueprint_catalog.go` (named blueprint books persisted as exported book strings in script storage, so a catalog works without any character and survives save/load), `actor_resolve.go` (player→character resolver helper), and `nearby.go` (ordered, paginated nearby).
- `inspect_many.go` (batched `inspectMany`, one Lua chunk for 1–200 targets with per-target errors), `factory_alerts`/`briefing.go` (status scan and attention classification), `fluid_segments`-style reads in `fluids.go` (`fluidSegments`, grouped by engine segment): batch reads that replace per-entity fan-out.
- The navigation mod also owns `actions.lua` (tick-driven shooting jobs), `topology.lua` (the `factorio_ai_topology` prelude behind `insights_topology.go`'s inline fallback), and their Lua tests (`actions_test.lua`); Factorio 2.0.77 stores tags only as blueprint/ghost metadata and ignores scripted direction writes on belts and rails, so those paths fail with actionable errors rather than pretending to succeed.
- Factory graph (CLI `graph export|load|schema|sync`, plus GraphQL `exportMapSnapshotToMemgraph` / `queryMemgraph` in `memgraph.graphqls` + `memgraph_api.go`): `graph_export_lua.go` (census, chunk and leaf Lua; the leaf/mobile bodies are the `graph_prelude`/`graph_leaf`/`graph_mobile` regions of `topology.lua`), `graph_export.go` (leaf planning, link routing, reconciliation, snapshot files), `graph_load.go` (Bolt loader into Memgraph, scope pruning), `memgraph/` (compose stack and `schema.cypherl`), `scripts/graph-compose.sh`, `just graph-*`. Bolt target is env (`MEMGRAPH_BOLT`/`USER`/`PASSWORD`), never GraphQL input; `queryMemgraph` is a lexical read-only guard, not a Memgraph permission. Design and query cookbook: `docs/memgraph-schema.html`.
- `go.mod`: Go version and dependencies. `justfile`, `scripts/server.sh`, `server-settings.json`: local server workflows/configuration.
- `README.md`: operational and GraphQL behavior reference; `.agents/skills/factorio-gameplay/SKILL.md`: gameplay-specific notes.

## Runtime/Tooling Preferences

Use Go modules; `go.mod` and the README agree on Go 1.26+. `just`, Bash, OpenSSL, and Factorio 2.0 are needed for managed server workflows; `lua` is optional and enables the navigation/action job harness during `go test`. The installed engine is Factorio 2.0.77 with the builtin quality and space-age features enabled, so validate Lua API names against the 2.0.77 runtime docs (`https://lua-api.factorio.com/2.0.77/`), not the "latest" 2.1 docs: fluid writes (`insert_fluid`/`clear_fluid_inside`), equipment-grid `put` (one item per call), `LuaEntity.get_control_behavior()`, and `LuaLogisticNetwork` robot counts (numbers, not arrays) all differ. No Node.js, Bun, or JavaScript package manager is required. gqlgen is pinned as a Go tool dependency; schema generation is configured by `gqlgen.yml`.

Server defaults target macOS Factorio paths. `scripts/server.sh` supports `FACTORIO_BIN`, `FACTORIO_SAVE`, `FACTORIO_GAME_PORT`, `FACTORIO_RCON_PORT`, `FACTORIO_STATE_DIR`, and optional `FACTORIO_MOD_DIR`; use the same state/mod directories when managing one instance. Runtime credentials and state live in ignored `.factorio/`.

Reach checks live in the generated Lua (`reach.go` helpers), not in the engine: the script API acts at any distance. They are permissive by default; `FACTORIO_ENFORCE_REACH=1` enables every actor build/interaction/capsule guard, and unsetting it restores the permissive default. `serve` reads it per operation, so restart the process to change it.

## Testing & QA

Tests use Go's standard `testing` package. Run `go test ./...`; tests cover validation and CLI behavior, pure helpers and polling, GraphQL relationships, and RCON framing with fake TCP servers. `TestNavigationController` runs `lua mods/factorio_ai_navigation/navigation_test.lua` and `TestActionController` runs `lua mods/factorio_ai_navigation/actions_test.lua` when `lua` is installed; both skip otherwise. There is no coverage target, but `.github/workflows/quality.yml` runs the same `scripts/check.sh` gate on pushes to `main` and on pull requests.

Unit tests do not exercise Factorio itself. For changes that affect server behavior, smoke-test against a local Factorio server using `just start` and a focused `go run . query|mutation ...` operation, then inspect the resulting state before considering the change verified. Consult `README.md` for mutation semantics, environment variables, and server safety constraints.

Real-engine coverage is opt-in: `go test -tags integration -run TestIntegration -v ./...`
boots a disposable sandbox scenario on random localhost ports, runs the working-tree GraphQL
schema against Factorio, saves, shuts down, and removes its temporary write-data directory.
`FACTORIO_BIN` overrides the executable; `FACTORIO_DATA` locates the bundled sandbox scenario
when it is not beside the executable. The suite takes roughly 20–30 seconds and never uses
`.factorio/` or the managed save.

<!-- BEGIN BEADS INTEGRATION v:1 profile:minimal hash:1105d646 -->
## Beads Issue Tracker

This project uses **bd (beads)** for issue tracking. Run `bd prime` to see full workflow context and commands.

### Quick Reference

```bash
bd ready              # Find available work
bd show <id>          # View issue details
bd update <id> --claim  # Claim work
bd close <id>         # Complete work
```

### Rules

- Use `bd` for ALL task tracking — do NOT use TodoWrite, TaskCreate, or markdown TODO lists
- Run `bd prime` for detailed command reference and session close protocol
- Use `bd remember` for persistent knowledge — do NOT use MEMORY.md files

**Architecture in one line:** issues live in a local Dolt DB; sync uses `refs/dolt/data` on your git remote; `.beads/issues.jsonl` is a passive export. See https://github.com/gastownhall/beads/blob/main/docs/core-concepts/sync-concepts.md for details and anti-patterns.

## Agent Context Profiles

The managed Beads block is task-tracking guidance, not permission to override repository, user, or orchestrator instructions.

- **Conservative (default)**: Use `bd` for task tracking. Do not run git commits, git pushes, or Dolt remote sync unless explicitly asked. At handoff, report changed files, validation, and suggested next commands.
- **Minimal**: Keep tool instruction files as pointers to `bd prime`; use the same conservative git policy unless active instructions say otherwise.
- **Team-maintainer**: Only when the repository explicitly opts in, agents may close beads, run quality gates, commit, and push as part of session close. A current "do not commit" or "do not push" instruction still wins.

## Session Completion

This protocol applies when ending a Beads implementation workflow. It is subordinate to explicit user, repository, and orchestrator instructions.

1. **File issues for remaining work** - Create beads for anything that needs follow-up
2. **Run quality gates** (if code changed) - Tests, linters, builds
3. **Update issue status** - Close finished work, update in-progress items
4. **Handle git/sync by active profile**:
   ```bash
   # Conservative/minimal/default: report status and proposed commands; wait for approval.
   git status

   # Team-maintainer opt-in only, unless current instructions forbid it:
   git pull --rebase
   git push
   git status
   ```
5. **Hand off** - Summarize changes, validation, issue status, and any blocked sync/commit/push step

**Critical rules:**
- Explicit user or orchestrator instructions override this Beads block.
- Do not commit or push without clear authority from the active profile or the current user request.
- If a required sync or push is blocked, stop and report the exact command and error.
<!-- END BEADS INTEGRATION -->

<!-- BEGIN BEADS CODEX SETUP: generated by bd setup codex -->
## Beads Issue Tracker

Use Beads (`bd`) for durable task tracking in repositories that include it. Use the `beads` skill at `.agents/skills/beads/SKILL.md` (project install) or `~/.agents/skills/beads/SKILL.md` (global install) for Beads workflow guidance, then use the `bd` CLI for issue operations.

### Quick Reference

```bash
bd ready                # Find available work
bd show <id>            # View issue details
bd update <id> --claim  # Claim work
bd close <id>           # Complete work
bd prime                # Refresh Beads context
```

### Rules

- Use `bd` for all task tracking; do not create markdown TODO lists.
- Run `bd prime` when Beads context is missing or stale. Codex 0.129.0+ can load Beads context automatically through native hooks; use `/hooks` to inspect or toggle them.
- Keep persistent project memory in Beads via `bd remember`; do not create ad hoc memory files.

**Architecture in one line:** issues live in a local Dolt DB; sync uses `refs/dolt/data` on your git remote; `.beads/issues.jsonl` is a passive export. See https://github.com/gastownhall/beads/blob/main/docs/core-concepts/sync-concepts.md for details and anti-patterns.
<!-- END BEADS CODEX SETUP -->
