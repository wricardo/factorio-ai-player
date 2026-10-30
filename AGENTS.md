# Repository Guidelines

## Project Overview

`factorio-ai-player` is a Go CLI that lets AI agents inspect and control Factorio 2.0 through GraphQL over administrative RCON. GraphQL executes in-process; there is no HTTP server. Gameplay actions run Lua on the Factorio server, so mutations can affect the save and disable achievements. Treat RCON credentials as administrative secrets.

## Architecture & Data Flow

CLI commands in `main.go` route GraphQL operations through `graphql_cli.go` and gqlcli's inline executor into the gqlgen schema. Resolvers in `graphql_*_resolvers.go` delegate to hand-written helpers; `resolver.go` converts typed inputs to backend command arguments, calls the game-operation dispatcher, then normalizes and decodes JSON. `main.go` builds Lua and sends it over the RCON TCP protocol. Factorio executes Lua and returns JSON. Navigation additionally calls the included mod's remote interface; its jobs advance on game ticks.

Mutations are not transactional: an error or lost response does not prove action failed or rolled back. Inspect game state before retrying. Nested GraphQL relationships may make separate RCON reads and are not atomic snapshots.

## Key Directories

- Repository root: Go CLI, GraphQL schemas, resolver/domain code, tests, and server configuration. Source is mostly organized as domain files, not nested Go packages.
- `mods/factorio_ai_navigation/`: Factorio Lua navigation mod and its Lua test harness.
- `scripts/`: managed server lifecycle script, invoked through `justfile`.
- `play/`: gameplay-specific agent handbook and save-state notes; not general source documentation.

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
```

Example GraphQL call (server must be running; password comes from managed state):

```sh
FACTORIO_RCON_PASSWORD="$(cat .factorio/password)" go run . query '{ status { tick players { name } } }'
```

Global `--addr HOST:PORT` goes before the CLI command. Use `go run . queries --args --desc` and `go run . describe TypeName --desc` to discover the GraphQL contract. No lint command or lint configuration is provided; run `gofmt` on changed Go files. `justfile` has no build or test recipes.

## Code Conventions & Common Patterns

- Keep Go code idiomatic and `gofmt`-formatted. Use domain-named root files (`insights_stock.go`, `factory_machines.go`, `navigation.go`) rather than adding parallel abstractions.
- Define public behavior and inputs in `*.graphqls`; gqlgen resolver adapters delegate to hand-written domain helpers. Put game-specific reads and writes in Lua-building functions and validate inputs before RCON calls.
- Reuse the generic `command[T]` bridge in `resolver.go` for ordinary game operations; use domain helpers for specialized queries, polling, and Lua responses.
- Return errors with context; use `context.Context` for cancellable RCON calls and wait/poll loops. Tests inject fake RCON listeners or callbacks instead of requiring a live server.
- GraphQL inputs use camelCase; Go symbols use Go naming; backend command flags and Lua payload conventions are normalized at the resolver boundary.
- Edit schema/config and regenerate gqlgen outputs when GraphQL contracts change. Do not hand-edit `graphql_generated.go` or `graphql_models.go`.
- Keep mutations explicit and conservative. Do not blindly retry operations with uncertain outcomes; verify inventories, entities, or job status first.

## Important Files

- `main.go`: process entry, game-operation dispatch, Lua execution, and RCON protocol.
- `graphql_cli.go`: GraphQL CLI commands and schema discovery.
- `schema.graphqls`, `navigation.graphqls`, `insights_*.graphqls`: GraphQL API and inline descriptions.
- `resolver.go`, `graphql_*_resolvers.go`: resolver bridge and schema adapters.
- `gqlgen.yml`, `graphql_generated.go`, `graphql_models.go`: gqlgen configuration and generated output.
- `actions.go`, `production.go`, `insights_*.go`, `factory_*.go`, `navigation.go`: gameplay operations and read-only insights. Topology (`insights_topology.go`), machine diagnostics (`insights_diagnostics.go`), planning (`insights_planning.go`), research/construction/fluids (`insights_operations.go`) and belt-flow measurement (`insights_belt_flow.go`, backed by the navigation mod's `belt_flow.lua`) live in their own domain files.
- `go.mod`: Go version and dependencies. `justfile`, `scripts/server.sh`, `server-settings.json`: local server workflows/configuration.
- `README.md`: operational and GraphQL behavior reference; `play/AGENTS.md`: gameplay-specific notes.

## Runtime/Tooling Preferences

Use Go modules; `go.mod` declares Go 1.26.0 (README currently says Go 1.25+, so use Go 1.26+). `just`, Bash, OpenSSL, and Factorio 2.0 are needed for managed server workflows; `lua` is optional and enables the navigation harness during `go test`. No Node.js, Bun, or JavaScript package manager is required. gqlgen is pinned as a Go tool dependency; schema generation is configured by `gqlgen.yml`.

Server defaults target macOS Factorio paths. `scripts/server.sh` supports `FACTORIO_BIN`, `FACTORIO_SAVE`, `FACTORIO_GAME_PORT`, `FACTORIO_RCON_PORT`, `FACTORIO_STATE_DIR`, and optional `FACTORIO_MOD_DIR`; use the same state/mod directories when managing one instance. Runtime credentials and state live in ignored `.factorio/`.

## Testing & QA

Tests use Go's standard `testing` package. Run `go test ./...`; tests cover validation and CLI behavior, pure helpers and polling, GraphQL relationships, and RCON framing with fake TCP servers. `TestNavigationController` runs `lua mods/factorio_ai_navigation/navigation_test.lua` when `lua` is installed and skips otherwise. There is no configured coverage target or CI/lint workflow.

Unit tests do not exercise Factorio itself. For changes that affect server behavior, smoke-test against a local Factorio server using `just start` and a focused `go run . query|mutation ...` operation, then inspect the resulting state before considering the change verified. Consult `README.md` for mutation semantics, environment variables, and server safety constraints.

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
