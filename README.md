# Factorio AI Player

A Go CLI for controlling and inspecting Factorio 2.0 through admin RCON. The public gameplay interface is GraphQL; `just` manages the server. Achievements are not a project concern: operations execute server Lua and disable achievements for the save.

## Server

Requirements: Go 1.26+, [just](https://github.com/casey/just), OpenSSL and Factorio. On macOS the defaults use `/Applications/factorio.app/Contents/MacOS/factorio` and `~/Library/Application Support/factorio/saves/ai-player.zip` **directly**. Close any other Factorio instance using the same data directory first. Back up the save before automation.

```sh
just                          # list server-management recipes
just start                    # run simulation even with no connected clients
just observe                  # launch a separate client to watch the server
just save                     # request a save
just logs                     # show server log; logs-follow streams it
just stop                     # save and cleanly stop
just restart                  # stop and start
```

**The server always simulates when nobody is connected:** fuel is consumed, enemies can attack, and the world can change until stopped. This allows detached-character crafting and production to progress.

Both game and RCON bind to localhost. `just save` and `just stop` use the GraphQL `save` and `shutdown` mutations with the managed server's RCON credentials. `just stop` waits for Factorio to save and exit; `just save` is asynchronous. Runtime files, including the generated RCON password, are in `.factorio/` (ignored by Git); do not remove or change them while the server runs.

`just observe` launches Factorio with a separate client data directory at `~/factorio-client`, avoiding the server's data-directory lock. In the client, select Multiplayer → Connect to address and enter `127.0.0.1:34197` (or the configured game port). The client has separate settings, login data, and mods; sign in and sync mods if needed. Existing `~/factorio-client/config.ini` is preserved.

Startup overrides: `FACTORIO_BIN` (absolute executable path), `FACTORIO_SAVE` (absolute existing save path), `FACTORIO_GAME_PORT` (default 34197), `FACTORIO_RCON_PORT` (default 27015), `FACTORIO_STATE_DIR` (default `.factorio`), `FACTORIO_MOD_DIR` (optional Factorio mod directory), and `FACTORIO_SCREENSHOT_DIR` (graphical client `script-output`, default `~/factorio-client/script-output`). Use the same state directory to manage an instance. A non-default RCON port also requires `--addr 127.0.0.1:PORT` before CLI subcommands.

## Integration tests

Default `go test ./...` uses fake RCON servers and does not require Factorio. The
opt-in integration suite boots a disposable sandbox scenario with its own
write-data directory, navigation mod, password, and random localhost ports:

```sh
go test -tags integration -run TestIntegration -v ./...
```

`FACTORIO_BIN` overrides the executable (default:
`/Applications/factorio.app/Contents/MacOS/factorio`). Set `FACTORIO_DATA` when
the sandbox scenario is not beside that executable. A run normally takes
20–30 seconds. It never reads or writes `.factorio/` or the managed save; the
suite saves its temporary scenario, shuts the process down, and removes all
temporary files.

## Development checks

`just generate` runs pinned gqlgen from `go.mod` and updates generated models,
execution code, and resolver adapters. `just check` leaves current checkout
unchanged while verifying generated output, `gofmt`, `go vet`, `go build`, and
full Go tests. Pull requests run same `scripts/check.sh` gate in CI.

Schema discovery commands below remain authority for current capabilities.

## Autonomous character

A disconnected Factorio player can queue crafting, but its queue did not advance in Factorio 2.0.77 even on an unpaused server. `characterCreate` instead creates a **detached Factorio character entity**, not a connected player. It transfers the disconnected player's main-inventory stacks into that character without creating items. Other player inventories and equipment are not transferred. The character exists in the world, can take damage, and is saved with the map.

`characterSpawn` creates a detached character with no player at the closest non-colliding character position within 10 tiles of finite coordinates on an existing surface and force. It costs no items, the character starts with empty inventories and its own reach, and the returned `character` is its unit number. Characters created this way persist in the save and are never cleaned up automatically, so destroy unused ones. Every actor input accepts that unit number, so spawned characters can be driven independently: `craft`, `mine`, `remove`, `transfer`, `equip`, placement mutations, and `navigateStart`.

`playerRestoreCharacter(input: {player})` creates and attaches character for connected player controlling none, returning unit number, surface, and position. It leaves main inventory untouched and rejects player already controlling character. `Actor.character` reports current character unit. Actor-scoped mutations, including `navigateStart`, accept shared `actor: {player|character}` input; navigation resolves player to current character before starting.

`characters(input: {surface, detachedOnly, limit, offset})` lists characters across a whole surface, sorted by unit number; `detachedOnly` defaults to true, and `detachedOnly: false` includes connected players' characters, whose `player` name is reported. It has no 256-tile area limit, unlike the `entities` query, but scans the whole surface on each call. Detached characters cannot chat, shoot, or open GUIs because those are player-driven.


Save detached character's returned unit number as string; it is not player name. `nearby` sorts deterministically by distance or unit number and returns standard zero-based pagination. Mining, removal, pickup, and character-backed placement and transfers require reach. Mining uses Factorio's mining operation and real drops/depletion but is **instantaneous**, not timed hand-mining. Movement uses `navigateStart` pathfinding job; no raw walking-input mutation exists.

`craft` uses normal crafting queue and consumes ingredients. `requested` is requested crafts, not items: recipe yielding two products produces twice as many items; `started` may be smaller. By default, mutation returns immediately; queued crafts may still be pending. `place` checks collision and consumes selected item quality. `inspect` reports entity state, applicable inventories (including machine modules, turret ammo, and character main/guns/ammo/armor/trash), fluid contents, and force-wide production totals for that surface (not per-machine totals). `transfer` moves selected item quality between character and typed entity inventory; it reports actual `moved`.

`equip` moves selected item quality between actor main inventory and a `GUNS`, `AMMO`, `ARMOR`, or `TRASH` inventory; use `direction: TO_MAIN` to unequip. Armor slots accept only one item, so unequip before swapping. `inspect` reports character main, guns, ammo, armor, and trash inventories. Equipping does not fire weapons.

Set `wait: true` to poll until actor's entire crafting queue is empty, including crafts queued before request. `waitTimeoutSeconds` defaults to 60 and must be 1–3600. On timeout, mutation returns `craftingComplete: false`, `waitTimedOut: true`, latest `queueSize`, and `waitedSeconds`; it does not cancel queued work. Without wait, these status fields are null. Example: `mutation { craft(input: {actor: {character: "123"}, recipe: "iron-plate", count: 10, wait: true, waitTimeoutSeconds: 120}) { started queueSize craftingComplete waitTimedOut waitedSeconds } }`.

`cancelCraft` cancels up to `count` crafts starting at the one-based queue `index` and returns the remaining queue. Canceling a craft can also cancel later crafts whose ingredients depended on it. `cancelResearch` stops the force's current research and returns the resulting state; the engine removes dependent technologies from the research queue.

`dropItem` seeds a line or the ground from an actor's main inventory. The
position snaps to its tile center. `target: GROUND` refuses a tile that holds a
belt, splitter, loader or linked belt: the engine would otherwise create the
stack under the belt and it would be lost while the mutation reported success.
Use `target: BELT` for belt tiles; a rejected drop leaves the inventory intact.

`placeMany` places one selected-quality item at each of up to 1000 explicit positions in one RCON call. `placements` entries use entity-center coordinates and optional engine `direction` 0–15 (default 0/north). Underground belt halves need `undergroundType` `INPUT` on the entrance and `OUTPUT` on the exit in the same call; without it both halves are created as inputs and never connect. `place` accepts the same `undergroundType` and otherwise, for an underground belt, creates the output half when an unpaired input half sits within underground distance behind the target in the same direction. `errors` contains typed one-based index, stable code, message, and target for first 20 failures; batch continues. Operation is not transactional, so inspect world before retrying. Player placement uses player's current character; both actor variants require character on selected surface and within build reach.

`connectionPlan` plans one axis-aligned straight or single-bend run for `kind` `BELT`, `PIPE`, or `POWER_POLE_CHAIN` and returns the snapped tile centers, per-tile engine `direction`, whether each tile already holds the requested entity, the item count `needed`, and typed `conflicts`. Positions snap to the center of the containing tile; the default items are `transport-belt`, `pipe`, and `medium-electric-pole`. `bend` `AUTO` plans both orientations and keeps the fewer-conflict route, tie to `HORIZONTAL_FIRST`; a straight route reports the orientation it lies on. A tile holding the same entity name with the needed direction is `existing` and costs nothing; a same-name belt facing elsewhere reports `DIRECTION_MISMATCH`. `DIRECT` plans surface tiles only, so an obstruction is a `COLLISION` with blocker prototype names, terrain flag, and ground tile. `PIPE` runs also report `FLUID_ADJACENCY` when a pipe-like entity outside the run touches a route tile, because pipes join orthogonally and would merge fluid networks; pass `allowFluidAdjacency: true` to merge deliberately. `POWER_POLE_CHAIN` places one pole every `floor(wire reach)` route tiles, so consecutive poles stay within wire distance and the engine wires them on build.

`routing: ASTAR` (default `DIRECT`) replaces the fixed route with a bounded shortest-path search for `BELT` and `PIPE`; `POWER_POLE_CHAIN` stays direct. One RCON read samples the endpoint bounding box padded by `searchMargin` (0–64, default 12, at most 16384 tiles): placeability of the route entity, existing same-name entities, underground entities and pipe-like entities. The search itself runs in Go and is deterministic, so a plan and its apply agree. Step costs are 1 per new tile, 0.25 per reused existing tile, 0.25 per bend and 2 per underground half; the plan reports `cost`, `expanded` states, `stopReason` (`FOUND`, `NO_PATH`, `EXPANSION_LIMIT` when `maxExpandedTiles` (1–50000, default 5000) runs out, `ENDPOINT_BLOCKED`) and a matching typed conflict instead of an error. `allowUnderground` lets the route tunnel with `undergroundItem` (default `underground-belt` for belts, `pipe-to-ground` for pipes) at an offset of at most the smaller of `maxUndergroundDistance` and the prototype's max underground distance; underground steps carry `undergroundType` `INPUT` for the entrance and `OUTPUT` for the exit, and `undergroundNeeded` counts the halves. A span may pass beneath any entity except another underground belt, linked belt or pipe-to-ground, and beneath `avoidAreas` (at most 20), where no surface entity is placed. Underground belt halves face the travel direction; a pipe-to-ground faces its surface connection, so an eastbound pair is `12` then `4`. A pipe-to-ground connects only on that side, so unlike regular pipes it may sit beside a foreign pipe. A belt feeds an existing belt at the goal from behind or the side but never head-on; `preferExisting` lets a belt route reuse existing belts that already face its direction, and reusing a pipe also needs `allowFluidAdjacency` because it joins networks. ASTAR-only fields are rejected on `DIRECT`, never silently ignored. Placement re-checks every tile, so a world that changed between plan and apply yields per-step errors; items are checked before any removal.

`connectEntities` plans the same run and then places it. It refuses to change the world when the plan has conflicts (`possible: false`, `placed: 0`) or when the actor lacks the planned item count, so a refused call is net-zero; each placed tile consumes one normal-quality item from the actor's main inventory, existing tiles are skipped, and reach applies per tile when `FACTORIO_ENFORCE_REACH=1`. Engine failures are reported per step with one-based index, stable code, message, and target. It prints the usual notification only when at least one entity was placed.

`waitUntil` polls exactly one condition from the backend until it holds or a 1–300 second wall-clock timeout expires; `pollSeconds` is 0.05–5. Conditions are `tick` (game tick at least N), `inventory` (actor main inventory holds N of an item/quality), `craftQueueEmpty`, `machineStatus` (entity status name in a set), `research` (technology researched), `fluid` (entity holds N of a fluid), and `production` (force produced N of an item in a production window). Each poll is a separate RCON read, so the game thread and other agents are never blocked and the game keeps simulating between polls; nothing is mutated and no notification is printed. `satisfied` and `timedOut` are exclusive, `polls` counts reads, and `last` reports the final observation. A condition that becomes unobservable — removed entity, unknown technology — is an error rather than a timeout, because the outcome is unknown; pass the prototype name with a unit number so the lookup can fall back to a surface scan.

`prototype(name)` reads static engine prototype data in one call so callers stop guessing footprints and stack sizes. `item` reports `stackSize`, `stackable`, `placeResult`, `fuelCategory`, `fuelValue`, and `burntResult`. `entity` reports `type`, whole-tile `tileWidth`/`tileHeight`, relative `collisionBox`/`selectionBox`, `maxHealth`, `beltSpeed`, `maxUndergroundDistance`, inserter pickup/drop offsets, `wireReach`, `supplyArea`, `craftingSpeed`, sorted `craftingCategories`, `fluidBoxCount`, and `mineable`. Exactly one of the two is set unless both prototypes share the name; an item with no same-named entity falls back to its place result, which can carry a different entity name. A name with neither prototype is an error.

`placeNextTo` places one item beside an anchor entity using both prototype bounding boxes, so callers do not compute footprints. `side` is `EAST`, `WEST`, `NORTH`, or `SOUTH`, `gap` is whole tiles between bounding boxes, and `direction` must be cardinal (0, 4, 8, or 12); rotated footprints swap width and height. The mutation snaps the preferred center to the tile grid, tries deterministic half-tile nudges, and keeps only positions that leave at least `gap` tiles on the requested side. It requires the anchor on the actor's force and surface, consumes one selected-quality item, reserves nothing on refusal, and refunds the item if engine creation fails. The result reports the placed entity, its position, the anchor unit number, side, and gap.

`fuelMany` fuels 1–200 explicit entities and `transferMany` moves one item between the actor and a typed inventory of 1–200 explicit entities, each in one RCON call. Targets use the shared entity reference (a unit number with the prototype name, or the name with its center position) and carry their own surface. Duplicate targets are rejected before any call. Per-target semantics match the single operations: `fuelMany` inserts exactly `countPerTarget` fuel or the target fails with a typed error (`LOOKUP`, `VALIDATION` for an entity without a fuel inventory, `REACH` under `FACTORIO_ENFORCE_REACH=1`, `INVENTORY` when the actor lacks fuel or the fuel inventory cannot take the full count, `ENGINE`), so a full burner is never partially filled; `transferMany` moves `min(countPerTarget, available, capacity)` honouring stack limits and bar slots, and a short move is reported in `results` without an error. Both continue past failures and report `failed`, at most 20 typed `errors` with one-based index and target identity, and per-target `results` for the targets reached. `moved` totals the batch and `transferredStacks` counts source stack moves that moved at least one item. They are not transactional: nothing is rolled back, so inspect state before retrying a batch whose response was lost. There is no area scan; pass entities from `machines`, `nearby` or `entities`.

`placeTile` places one tile item such as landfill, concrete, or foundation after checking the item's prototype placement rule against the current tile and rejecting tiles where an entity (other than characters and resources) has a collision layer shared with the tile. It consumes one normal-quality item and returns the placed tile name. Tile items whose condition spans more than one tile are rejected. The acting character, or the selected player's current character, must be on the surface and within build reach.

`createBlueprint` exports a blueprint string from blueprintable entities inside an area; `includeModules`/`includeFuel` default on, tiles/trains/station names default off. `createBlueprintFromEntities` builds the same string from explicit blueprint-relative entities and tiles instead of copying the world; supports entity `name`, position, direction, quality, recipe, and mirror flag. `placeBlueprint` imports that string and places **entity ghosts** at finite coordinates, rotated by `direction`; it does not consume items and does not build real entities. Real construction needs construction bots or manual building. Blueprint strings are limited to 2,000,000 characters.

The `rotate` mutation rotates a reachable entity by 1–3 clockwise quarter turns via its `turns` input. Inserter placement direction specifies its pickup side, not its drop side. `inspect` reports inserter pickup/drop positions and transport-belt lane contents. `beltTake` moves up to `count` normal-quality items from a reachable belt into the acting character's inventory; supply either `character` or `player`, and the acting character must be within reach of the belt. If an unexpected partial insertion occurs, the remainder is spilled near the character.

`inspect { logistics { tick inserters { inserter { name position { x y } pickupPosition { x y } dropPosition { x y } heldStack { name count } } pickup { name type position { x y } lanes { index items { name count } } } drop { name type position { x y } lanes { index items { name count } } } } } }` resolves inserters whose engine pickup or drop target is the inspected entity. Endpoints can be machines, belts, containers, other entities, or null; belt endpoints include lane contents. The resolver scans inserters across the surface, including long-handed and modded long-range inserters, without a distance limit. It performs a separate RCON read, so `logistics.tick` may differ from the inspection snapshot; a removed or replaced entity can make the nested read fail.

`clearArea` immediately destroys tree entities and simple entities whose prototype names contain `rock`, within the required rectangular area; centers must lie inside the inclusive bounds. It removes entities directly, without mining drops. Each dimension must be positive and at most 256 tiles. Response reports `treesRemoved` and `rocksRemoved`. Example: `mutation { clearArea(input: {surface: "nauvis", area: {leftTopX: 0, leftTopY: 0, rightBottomX: 32, rightBottomY: 32}}) { surface treesRemoved rocksRemoved } }`.

`remove` mines one entity on actor's current surface, using normal mining so actor recovers entity and contents. Input uses `actor` plus `target`; target is exactly unit number or prototype name plus center position. Actor must reach entity, and characters at target cannot be removed. Non-minable entities fail. `items` reports stacks added to actor main inventory; overflow spills nearby and is not listed. Example: `mutation { remove(input: {actor: {player: "wricardo"}, target: {entity: "stone-furnace", position: {x: 40, y: 64}}}) { tick surface force actor { player character } entity type position {x y} items {name quality count} } }`.

`harvestArea` accepts one nested `actor` and scans actor's current surface. It mines reachable trees and rock-named simple entities with normal mining. `items` reports inventory gains; `outOfReach` and `failed` retain counts while `errors` provides typed per-target details. Uses same 256-tile-per-side area limit. Example: `mutation { harvestArea(input: {actor: {character: "123"}, area: {leftTopX: 0, leftTopY: 0, rightBottomX: 32, rightBottomY: 32}}) { treesMined rocksMined outOfReach failed errors {index code message} items {name quality count} } }`.

`pickupArea` accepts nested `actor` and collects reachable dropped stacks. Partial stacks remain when inventory fills; aggregate item lists report moved and remaining counts. `stacksOutOfReach` retains aggregate count while typed `errors` distinguishes `REACH` from `INVENTORY` per stack. Example: `mutation { pickupArea(input: {actor: {character: "123"}, area: {leftTopX: 0, leftTopY: 0, rightBottomX: 32, rightBottomY: 32}}) { stacksFound stacksOutOfReach errors {index code message target {position {x y}}} itemsPickedUp {name quality count} itemsRemaining {name quality count} } }`.

`deconstructArea` and `cancelDeconstructArea` place or cancel deconstruction orders for deconstructible entities in required area, applying force planner semantics. They need no actor unless `instant` is requested; ordinary mode removes nothing itself. `force` defaults to `player`; same 256-tile-per-side limit applies. Entity variants accept up to 200 `targets`, each using shared `EntityRefInput`. All continuing operations return counts plus typed `errors {index code message target {...}}`; clients never parse indices from text. `inspect.toBeDeconstructed` reports current mark.

When finished, wait for all crafting to finish and return the character's main inventory to the disconnected player before joining as that player. Then stop the server.

Return fails if either crafting queue is nonempty, the player is connected, the player's main inventory lacks enough empty slots, or the character has items outside its main inventory. It destroys the detached character after successful transfer. Save the unit number across CLI invocations and server restarts; losing it does not automatically return items.

For player-backed inspection, crafting, placement, fueling, transfer, and belt pickup, supply the player name instead of the character ID in the relevant input. Every entity interaction requires the acting character to be within reach: for a player actor that is the player's current character, so placement, fueling, transfer, and belt pickup are not remote-administrative. Offline-player crafting may remain pending. Reach checks are a policy layer implemented in the generated Lua, not an engine guarantee: Factorio's script API executes these operations at any distance, so the resolvers would otherwise call `can_reach_entity`/`build_distance` themselves. They are **disabled by default**, matching the administrative script API; set `FACTORIO_ENFORCE_REACH=1` to enforce actor build and interaction reach (placement, entity interaction, wires, capsule targets), and unset it to return to the permissive default. `serve` reads the flag per operation, so restart it after changing the environment. `playerInsight.reach.canReachEntity` always reports the engine's own answer, independent of this policy. `startResearch` starts an enabled, unresearched technology whose prerequisites are researched; the engine queues it behind the current research. A lab's science packs are its `input` inventory, so `transfer` feeds a lab and `inspect` reports them. Combat and full factory planning are not yet implemented.

### Destination navigation

Destination navigation requires the included Factorio mod. Back up the save, then run `just navigation-install` while the server is stopped. The recipe links the mod into `~/Library/Application Support/factorio/mods` on macOS and refuses to overwrite an existing installation. Set `FACTORIO_MOD_DIR` for a different mod directory, and use the same value when starting the server. Keep the checkout at the same path so the link remains valid; restart Factorio after code changes. Enable the mod if the installation's mod list has disabled it. Connected clients must have matching mods. Installing or removing a mod changes the save's mod set.

`navigateStart` starts a persistent job for a detached character or a connected player's current character, using coordinates, a `targetEntity`, or a `targetCharacter` on its current surface. `tolerance` defaults to 0.5 tiles; `timeoutTicks` defaults to 3600 game ticks. By default it returns immediately. Set `wait: true` to wait for a terminal job state; `waitTimeoutSeconds` limits that wall-clock wait to 1–3600 seconds (default 60). On wait timeout the mutation returns the latest observed job, which may still be active; it does not cancel the movement. Check `status` rather than assuming arrival. The job's separate tick-based timeout still applies. If the request is interrupted or a status read fails, the error includes the job ID when known; the job may continue. Use the job ID with the `navigation` query and `navigateCancel` mutation. Use `go run . mutations --filter navigate --args --desc` and `go run . describe NavigateStartInput --desc` for the schema; `go run . describe NavigationJob --desc` lists outcomes.

`navigateStart` also accepts `targetEntity` instead of coordinates to walk next to a static entity such as a chest, assembling machine, or inserter. Pass its unit number; the goal is the closest point on the entity's bounding box inflated by `tolerance`, so the character stops beside the entity instead of trying to stand inside it, and the job ends as `TARGET_MISSING` when the entity is removed or moves to another surface. The target is resolved once at start: buildings without a unit-number lookup flag are found by scanning the character's surface, and the box is cached, so re-checking a still-valid target costs one surface scan per second at most. Characters are rejected here — use `targetCharacter` to chase them.

`navigateStart` also accepts `targetCharacter` instead of coordinates, which keeps the destination on that character's current position so the walker follows it. The target must be another character on the same surface, and `tolerance` must then be at least 1 because characters collide. The goal is not a single point: the mod builds a ring of eight candidate points at `tolerance` around the target, three rings wide in 0.75-tile steps, ordered nearest-ring-first and then by distance to the walker, so a point inside a chest or inserter cannot fail the chase. Each unreachable candidate (no path, or a path requiring a destroyed obstacle) advances to the next one, and only exhausting all 24 candidates reports `NO_PATH`; a pathfinder-busy reply retries the current candidate. The job re-requests a path when the target drifts more than 3 tiles from the planned goal, ignores the target entity while pathfinding, reports its unit number as `targetCharacter`, and ends as `TARGET_MISSING` when the target disappears or changes surface.

The mod requests a Factorio path and follows waypoints on game ticks, without an RCON call per step. It retries pathfinding when stalled, stops on arrival, cancellation, timeout or failure, and retains job state in saves. Only one navigation job per character is allowed. For a connected player, navigation controls walking until it ends; manual movement input may compete with scripted direction, so cancel the job to stop navigation. Disconnecting or changing the controlled character cancels its job and stops scripted walking. A paused server cannot advance a job or its tick-based timeout. Paths can become blocked as the world changes, and a path does not guarantee interaction reach. Inspect job status and character position before retrying after an uncertain RCON response. The mod is not installed into or activated on an already-running server automatically.

## GraphQL

GraphQL runs in-process through gqlcli; no GraphQL HTTP server is required. Game operations use RCON: set `FACTORIO_RCON_PASSWORD` to the server's password. Actor reachability checks are off by default; `FACTORIO_ENFORCE_REACH=1` enables them. The default address is `127.0.0.1:27015`; use `--addr HOST:PORT` **before** the GraphQL command to override it. Schema discovery runs locally and does not need a running server.

### Schema discovery

Use these commands to discover operations, input types, fields, and the inline schema documentation:

| Command | Use |
| --- | --- |
| `go run . types` | List schema types grouped by kind. `--filter NAME` (`-f`) matches a case-insensitive substring; `--builtin` includes built-in `__` types. |
| `go run . queries` | List available query fields. |
| `go run . mutations` | List available mutation fields. |
| `go run . describe TYPE_NAME` | Show a type's SDL definition. `TYPE_NAME.FIELD_NAME` selects a field; `d` is an alias for `describe`. |

`queries`, `mutations`, and `describe` accept `--args` (`-a`) to include field arguments, `--desc` (`--descriptions`) to show inline schema descriptions, and `--depth N` to recursively include referenced non-scalar types. Depth defaults to 0 (only the requested type) and must be nonnegative. `queries` and `mutations` also accept `--filter NAME` (`-f`) to narrow the field list. For example, `go run . queries --args --desc` shows query signatures and descriptions; `go run . describe ActorInput --desc` explains actor selection.

### Executing operations

Use `go run . query` (`q`) for reads and `go run . mutation` (`m`) for actions. Supply the GraphQL operation as one positional argument, with `--query DOCUMENT` (`-q`), or from a file with `--file PATH` (`-f`). If multiple sources are supplied, the file takes precedence over `--query`, which takes precedence over the positional argument. Quote inline documents to prevent shell expansion. The operation must match the selected command.

Both commands accept:

| Option | Use |
| --- | --- |
| `--variables JSON` (`-v`) | Supply variables as a JSON object. |
| `--var-file PATH` | Read variables from a JSON file; takes precedence over `--variables`. |
| `--format FORMAT` | Output as `toon` (default), `json`, or `table`. |
| `--output PATH` (`-o`) | Write formatted output to a file instead of standard output. |
| `--help` (`-h`) | Show command usage and options. |

GraphQL errors are reported instead of printing partial data. Use `go run . help` for the command list, or add `--help` to any GraphQL command for its options.

### Scripts

Use `go run . script` (`js`) for loops, conditionals, dependent operations, or controlled concurrency. Scripts run in-process against the same schema as `query` and `mutation`; no GraphQL HTTP server is required.

```js
async function run(gql, input) {
  const status = await gql.query("{ status { players { name } } }")
  return await gql.each(
    status.data.status.players,
    async (player) => gql.query(
      "query($name: String!) { player(input: {player: $name}) { tick character } }",
      { name: player.name }
    ),
    { concurrency: 4, stopOnError: false }
  )
}
```

| Option | Use |
| --- | --- |
| `--file PATH` (`-f`) | Read the JavaScript source from a file. |
| `--source JS` | Run inline JavaScript. |
| `--op NAME` | Run a script saved under `scripts` in `.gqlcli.json`. |
| `--function NAME` | Function to call; defaults to `run` or the saved script's function. |
| `--arg JSON` | Pass a JSON object as the second script argument. |
| `--arg-file PATH` | Read that object from a file; takes precedence over `--arg`. |
| `--output PATH` (`-o`) | Write the returned JSON to a file instead of standard output. |

Exactly one of `--file`, `--source`, or `--op` is required. The script function receives `gql` and `input`. The `gql` helpers accept an optional operation name and expose `query`, `mutation`, `request`, and `each`. `console.log` and `console.error` write to the command output.

Scripts can run mutations, so they are as powerful and as risky as `go run . mutation`; a script does not make a multi-operation workflow transactional. Check game state before retrying after an error or lost response.

### GraphQL server

Use `go run . serve` to expose the same schema over HTTP for external GraphQL clients. The endpoint accepts GraphQL over HTTP `POST`, query-only `GET`, and `OPTIONS` at `/query`; introspection is enabled. Requests are capped at 1 MiB.

```sh
FACTORIO_RCON_PASSWORD="$(cat .factorio/password)" go run . serve --listen 127.0.0.1:8080
curl -s -X POST http://127.0.0.1:8080/query \
  -H 'Content-Type: application/json' \
  -d '{"query":"{ status { tick players { name } } }"}'
```

| Option | Use |
| --- | --- |
| `--listen HOST:PORT` | HTTP listen address; defaults to `127.0.0.1:8080`. |
| `--addr HOST:PORT` | Global RCON address, as for inline commands. |
| `--help` (`-h`) | Show command usage and options. |

`FACTORIO_RCON_PASSWORD` is required to start the server. Each request runs resolvers independently over RCON; the endpoint is not a snapshot and is not transactional. Stop it with Ctrl-C or SIGTERM, which drains in-flight requests for up to 5 seconds. The server has no authentication or authorization, so bind it to localhost unless you deliberately trust every client that can reach it.

### Native screenshots

The managed Factorio server is headless and cannot render screenshots itself. `screenshot` requires a connected graphical player and delegates Factorio's native `game.take_screenshot` call to that same player as both `player` and `by_player`; it never uses operating-system screen capture. Start `just observe`, connect its client to the server, and pass that connected player name.

```graphql
mutation {
  screenshot(input: {
    player: "viewer"
    surface: "nauvis"
    position: {x: 32.5, y: -18.5}
    width: 1280
    height: 720
    zoom: 1
    showEntityInfo: true
  }) {
    tick
    surface
    player
    path
    width
    height
    bytes
  }
}
```

Factorio writes the PNG beneath the graphical client's `script-output` directory at a server-generated unique path under `factorio-ai-player/`; callers cannot select the path. The GraphQL process reads `FACTORIO_SCREENSHOT_DIR` as its trusted view of that directory, defaulting to `~/factorio-client/script-output` to match `just observe`. Set it when the graphical client output is mounted elsewhere, for example `FACTORIO_SCREENSHOT_DIR=/mnt/factorio-client/script-output go run . serve`.

The mutation waits up to 10 seconds for a new regular PNG, checks its PNG header, requested dimensions, and complete end marker without decoding the full image, and reports byte size only after verification. Missing, stale, invalid, incomplete, or wrong-sized output returns a GraphQL error and sends no in-game notification. Successful verification sends the standard generated notification or the optional custom `message`.

### In-game notifications

Every mutation prints an in-game notification after it succeeds. `BeltThroughputStartInput` has no notification field, and `save`/`shutdown` take no input. `navigateCancel` exposes the option as a field argument.

- By default the notification is a generated summary derived from the mutation result, such as `placed stone-furnace at (3, 4)` or `cleared nauvis: 2 trees and 1 rocks removed`.
- `message: String` prints this text verbatim with `game.print` instead of the generated summary, so all connected players see it in chat.

`navigateCancel` accepts the same option as a field argument: `navigateCancel(id: ID!, message: String)`.

The `announce(message: String!)` mutation prints a free-form message to all connected players; use it to narrate plans or reasoning before acting. Unlike mutation notifications, it fails when the message is blank or the RCON print fails.

Notifications are best-effort and sent in second RCON command after mutation succeeds; failed notification does not change mutation result. Detached characters have no owning player, but `game.print` reaches every connected player. Example: `mutation { place(input: {actor: {character: "123"}, position: {x: 3, y: 4}, item: "stone-furnace"}) { name position {x y} } }` prints `placed stone-furnace at (3, 4)`; `announce(message: "mining iron next")` prints that sentence.

### Relationships and snapshots

Nested relationships can combine several reads in one operation. `Actor.force`
identifies the actor's force; recipe reads use the specified actor's force.
`Actor.nearby` searches around the actor, with an optional radius (default 10),
name, and type. `ConnectedPlayer.actor` provides the same actor relationships
from each player in `status.players`. `entity(input: EntityRefInput!)` returns
`EntitySnapshot {tick, surface, force, entity}` using same unit-number or
prototype-plus-center reference as mutations. Lookup first uses engine unit
lookup, then scans requested surface for unsupported prototypes. Nested
`entity.inspection(actor)` reuses returned identity and position.

`Entity.inspection(actor: ActorInput!)` inspects an entity using the supplied
actor. It is nullable, but a failed inspection produces a GraphQL error; the CLI
reports that error instead of printing partial data.
`EntityInspection.recipeInfo(actor: ActorInput!)` and
`QueueEntry.recipeInfo(actor: ActorInput!)` read recipe details for the supplied
actor and return null when the entity or queue entry has no recipe. Nearby entities
carry their surface into inspection, as do mining entities returned by an
inspection.

Each nested relationship delegates to an existing game command and can make another
RCON call, including for each entity or queue entry. These reads are not an
atomic snapshot: independently resolved fields can observe different game ticks.

### Factory production and machines

Defaults: surface `nauvis`, force `player`, window `ONE_MINUTE`. Supported
windows: `ONE_MINUTE`, `TEN_MINUTES`, `ONE_HOUR`. Counts cover the selected
window; rates are Factorio's per-minute averages over that window, not
instantaneous rates. Items include previously produced or consumed names even
when their current window counts are zero.

Machine scans cover assembling machines, furnaces, mining drills, labs and rocket
silos for the chosen force and surface. Status filters use Factorio status names
such as `no_fuel`; optional `area` uses `leftTopX`, `leftTopY`, `rightBottomX`,
and `rightBottomY`. Results sort by type, name, position, then unit number.
`total` counts filtered matches; request subsequent pages using `offset` and
`limit` (1–1000). Machine summaries are grouped by type before the status filter,
and `other` includes statuses outside the listed buckets; `noPower` counts
`no_power`, `low_power` and `not_connected_to_electric_network`. Because scans cover
only the machine types above, inserter power faults do not appear there — use
`powerConsumers` and its `connectedToProducingNetwork` for every electric entity.
Since the factory can change between requests, pages may shift. Overview production
and machine scans are separate reads; their ticks identify each snapshot.

### Factory diagnosis and planning

`machines` also returns recipe, input/output inventories, fluid boxes, crafting
progress, energy and mining target when applicable. `itemRecipes` searches recipes
by product and reports candidate machine prototypes by crafting category; a
category match alone does not prove a particular machine can run the recipe.
`researchInsights` exposes force research state, prerequisites, science and
unlock effects. Its `available` means enabled and prerequisites researched, not
that labs, science packs or trigger conditions are satisfied.

`stock` distinguishes stored, in-transit, player and ground quantities.
It covers the inventory types listed in its schema description, not every
inventory in the game. Ground items have no force ownership. Belt-line
deduplication fails explicitly after one million comparisons; narrow the area
rather than relying on partial totals. Each belt contributes its own tile's
lines, so in-transit totals match the sum of per-belt lane contents. Locations,
totals and items are paginated independently: `totals` breaks down per item,
quality and category, while `items` gives one row per item and quality summed
across every category.

`electricNetworks` reports one-minute network-wide production and consumption
averages in watts; selected area restricts poles, not the network's statistics.
`powerConsumers` reports observed energy, network and status. Current network
satisfaction and per-entity instantaneous production are not exposed.
`powerConsumers` filters server-side before pagination, so `pageInfo.total`
counts matches: `status` (exact engine status name, unknown names are an error),
`connectedToProducingNetwork` (entities where the predicate does not apply never
match), `networkId` (positive engine network id), and `onlyIssues`, which keeps
entities reporting `no_power`, `low_power`, `not_plugged_in_electric_network` or
`not_connected_to_electric_network`, or with `connectedToProducingNetwork: false`.
Filters combine with AND and the default leaves behavior unchanged.
`electricNetworks` accepts `networkId` to keep one network and rejects the entity
filters rather than ignoring them.
`worldLogistics` inspects belts, inserters and fluid boxes; `worldResources`
returns resource tiles, obstacles, water and nearby drills. Both require an area
of at most 256 tiles per dimension. Nearby drills do not imply mining coverage.
`worldPlacement` performs the engine placement check, not inventory or reach; a refused tile reports the colliding prototype names, whether the ground tile itself blocks the prototype, and the ground tile name, so a single check explains why without a follow-up probe.
`probePlacement` repeats that check on every tile center of an area of at most
16384 tiles in one round trip. `mode: BLOCKED_TILES` (the default) returns every
refused tile with the colliding entity names, the ground tile name, and whether
the terrain itself blocks the prototype. `mode: GRID` returns a compact
placeability map (`rows`, one `1`/`0` string per tile row, top row first) and
`mode: SITES` returns the largest non-overlapping empty tile rectangles of at
least `siteWidth` by `siteHeight` tiles, largest first, anchored at their
top-left tile corner, capped by `maxSites`. Samples are hypothetical placements,
so grid and site checks may overlap; use a 1x1 prototype (`wooden-chest`,
`transport-belt`) to measure open ground and a machine prototype
(`electric-furnace`) to measure where that machine fits. Use it to route belts or
pipes and to find smelting-array or big-build footprints; unlike navigation it
answers per prototype, one direction per call, and ignores character reach.
Both world scans are bounded per call, so a map-wide search pages 256-tile areas
client-side (the `script` command loops and aggregates them in one process).
`worldLogistics` and `worldResources` accept `limit`/`offset` of at most 1000
rows per call. `worldResources` accepts `kinds` (`resource`, `tree`, `cliff`,
`simple-entity`, `water`) to narrow `worldEntries` and `total` while the
partition totals stay unfiltered, and `groupPatches: true` to cluster matching
resource entities into connected patches with bounds, centroid, total amount and
nearby drills, sorted by amount descending.
`mapArea` renders one bounded area as a compact character grid for headless clients, so an agent gets spatial context without screenshots or paged entity lists. The area is downsampled to at most `maxRows` by `maxColumns` cells (1–64 each) using one square `cellSize`; `truncated` is true whenever `cellSize` exceeds 1. Each cell reports the highest-priority entity whose center falls in it — character, machine, inserter, belt, pipe or fluid entity, pole, chest, resource, tree, rock, cliff — otherwise water (`~`) or empty (`.`). Natural entities appear regardless of force; built entities are limited to the selected force. `layer` narrows the scan to `ALL`, `FACTORY` (characters and built entities; natural obstacles and resources omitted), or `RESOURCES` (resources only, water as terrain). The legend maps every character to its prototype name, type, first unit number and cell count, so a caller can follow a glyph with `entities` or `inspect`. One cell shows one entity and cells are sampled at their top-left tile for water, so the grid is a spatial overview, not a completeness claim.

`factoryBriefing` is one bounded read-only attention snapshot. It composes the existing production, machine, power-network, research, safety, construction-order, ghost and stock reads, each in its own RCON call, so every section reports its own tick (`productionTick`, `machineTick`, `powerTick`, `researchTick`, `safetyTick`, `constructionTick`, and `stockTick` when `watchItems` were given) and sections can differ by a few ticks. `attention` lists observations by severity (`CRITICAL` stops output or threatens loss, `WARNING` degrades it, `INFO` is queued work), then kind, position and name, cut to `attentionLimit` (1–100, default 20); `attentionTotal` and `attentionCounts` keep the exact pre-cut totals per kind. Kinds are `NO_FUEL`, `NO_POWER`, `NO_INGREDIENTS`, `OUTPUT_FULL`, `NO_RECIPE`, `FLUID_SHORTAGE` (from engine machine statuses), `ENEMY` and `DAMAGED` (safety), `NO_RESEARCH` (research enabled, nothing active or queued), `GHOST_BACKLOG`, `DECONSTRUCTION`, and `LOW_STOCK`. Per-entity items carry name, position and unit number when the source exposes one; aggregate kinds carry a `count` instead. `watchItems` (at most 20, each with a `minimum`, default 1) report `stockouts` against observed factory stock, with `actorHas` from the optional `actor`'s main inventory. Ghost counts need `area` (positive dimensions, at most 256 tiles per side) and are skipped with a warning otherwise; `ghostsBuildable` counts ghosts whose placement material is in observed stock, over the first 1000. Per-machine attention covers the first 3000 machines while summaries cover all, and a truncated scan is named in `warnings`. A failing section fails the whole query, so a returned briefing is never silently partial. It recommends nothing and stores nothing.

`nearestPlacement` answers "where is the nearest legal spot for this prototype near this origin" in one round trip. Candidates are tile-aligned footprint centers (odd sizes on half tiles, even sizes on whole tiles, width and height swapped for east and west directions) within `maxRadius` (greater than 0, at most 64) of `origin`, ordered by distance then x then y; the first that passes every check wins, so the answer is the nearest legal position, not a nearby one. Checks run cheapest first: the engine placement check, `avoidResources`, `avoidWater`, `minDistanceFromEntities` (0–16 tiles of clear space around the footprint, counting built entities and ghosts of any force but never resources, trees, rocks, cliffs, characters, corpses or items), `requirePowerCoverage` (footprint touches an electric pole's supply area of the selected force) and `requireWaterAdjacency` (a water tile within one tile of the footprint). `expanded` counts candidates evaluated. `blockers`, `terrain` and `tile` describe why the snapped origin itself is refused by the engine and are empty when the engine accepts it, so a not-found answer still explains the origin; a candidate rejected only by a caller constraint reports no blockers. Like `worldPlacement` it ignores reach, inventory and research. Water means a tile with a pumpable fluid: out-of-map tiles share the `water_tile` collision layer but are not water, here and in `mapArea`.

`entities` pages every entity in an area with optional prototype-name and type
filters; `belts` pages transport belts and both lane contents. `belts` is
transport-only by contract: underground belts, splitters, loaders and linked
belts have different line counts and are read with `worldLogistics` or
`factoryGraph` (whose belt nodes already carry `direction` and `lanes`), or as
a linear run with `beltStretch`. Both expose
`total` with `limit`/`offset`. `removeMany` mines up to 200 explicit,
reachable targets and reports per-target failures without aborting the batch.
`safety` observes enemy and damaged entities, optional position pollution, and
player alerts; it does not predict attacks. Alerts require a player name.

### Topology, diagnosis and planning

`beltRun` walks the engine's real belt connections from the belt covering a
position: branches, merges, underground pairs, splitters, loaders and linked
belts. It returns a directed edge list, `sources`/`sinks`, and `boundaryEdges`
for connections that leave the returned node set. `sources`/`sinks` are
structural terminals, not stall or dead-end diagnoses, and `maxEntities`
(1–1000) bounds traversal with `truncated` set when belts were omitted.
`beltStretch` walks one linear stretch instead of a whole component: it starts
at the belt covering the target, follows tile-adjacent engine connections, and
collapses underground and linked-belt pairs into one hop, so a tunnel never
breaks the sequence. The walk stops at a merge (two predecessors), a split (two
successors, including any splitter), a surface boundary, a revisited belt, or
`maxEntities`; `ends` reports where and why, with the connected belts beyond the
stop. A forward `boundary` of a transport belt or underground exit is also
classified from the last belt's direction and the tiles ahead: `expectedTarget`
is the tile it outputs toward, `blocker` names the first non-belt entity on the
expected tile or between it and the next aligned belt (or a belt facing the run
head-on), and `reason` separates `NATURAL_END` (no aligned belt within 8 tiles,
as when a run ends beside its last inserter or in front of a machine) from the
gaps `MISSING_BELT`, `BLOCKED`, `HEAD_ON` and `UNPAIRED_UNDERGROUND`; `gap` is
true for the gaps. Merge, split, loop, truncated and backward ends are
`NOT_APPLICABLE`, and a splitter, loader or linked belt as the last entity is
not classified. A gap needs evidence that the line continues, so a belt that
points into an empty field is a natural end, not a gap. Every lane carries the engine transport-line name and its side
(`left_line`/`right_line`, splitter lines, and an underground belt's
`secondary_*` tunnel lines), and `left`/`right`/`unknown` totals
sum every belt's transport lines once, so a multi-tile stretch reports the items
actually sitting on it; a line object that one entity exposes twice is
deduplicated. `mixed` is set
when at least one belt in the stretch carries more than one prototype/quality
on that side, which is the lane-contamination signal. The totals are one
snapshot of in-transit stock, not flow: a stalled or backed-up stretch reports
items that are not moving; `beltStretchMeasure` walks the same stretch and
starts a `beltThroughputStart` measurement on its first, middle (default) or
last transport-belt tile, returning both the snapshot and the per-lane crossings
that prove whether the mixture actually moves. One tile is sampled per call, and
stretches of only splitters, underground belts or loaders have no measurable
tile.
`factoryGraph` returns machines, belts, chests, inserters and fluid-network
entities intersecting an area in one snapshot, including inserters outside the
area whose actual target is inside it; `boundaryEdges` identify every endpoint
that leaves the set. Fluid entities include offshore pumps, pipes,
pipe-to-ground, pumps, boilers, generators, heat exchangers and storage tanks,
with directed `fluid` edges from the engine's pipe connections. Belt nodes carry
`direction` and per-line `lanes` contents, so topology and what each lane holds
come back in one call. Neither query measures throughput.

`beltComponents` groups every belt in an area into weakly connected components
in one snapshot and classifies each one, replacing a seed-per-component audit:
`belts` counts the component's belts, `nodes`/`edges` follow the same engine
connections as `factoryGraph`, `sources`/`sinks` are structural terminals
inside the area, `inserters` lists every arm whose pickup or drop target is one
of the belts (once per component, so an arm linking two components appears in
both), and `items`/`itemCount` aggregate the in-transit stock on its lanes.
`dead` is true when nothing feeds or drains the component: no inserter target
and no machine drop target touches it. `stallEnds` are sinks no inserter picks
from, so items stop there, and `starveHeads` are sources nothing drops onto.
Components are sorted dead first, then by belt count, and paged with
`limit`/`offset`; `deadOnly` keeps just the dead ones. Because this is a
read-only single-snapshot analysis, its area may be up to 1024 tiles per side
instead of the 256-tile paging cap, so a whole base is one call. `maxEntities`
(1–5000) bounds each traversal and sets that component's `truncated`;
`beltTotal` counts belts found before grouping.

Lane reads in `worldLogistics`, `factoryGraph` and `beltStretch` carry the
engine transport-line name and its side (`left_line`/`right_line`, splitter
lines, an underground belt's surface and `secondary_*` tunnel lines).
`LogisticsEndpoint` also exposes `direction`, so a directed walk does not have to
reconstruct it from edges. `belts` only returns transport belts, where `line1`
is the left line and `line2` the right line.

`adjacency` answers "are these actually connected": it lists every entity in an
area (at most 64×64 tiles) whose axis-aligned bounding box lies within
`tolerance` (0–4 tiles) of another, plus the engine connection kind when one
exists. `connection` is empty for pairs that merely touch or sit near each other
— the usual misaligned pipe, boiler, offshore pump or inserter case — and
non-empty values are `belt`, `underground`, `linked`, `loader`, `pickup`, `drop`
and `fluid`. `gap` is the bounding-box distance in tiles (0 when touching).
`limit` (1–2000) bounds nodes and pairs with `truncated` set when either was
omitted. Geometric adjacency never proves flow; use `factoryGraph`, `beltRun` or
`fluidNetwork` for engine truth.

`inspect { logistics }` resolves inserters whose engine pickup or drop target is
the inspected entity, with belt lane contents and a separate snapshot tick. It
scans all surface inserters, so long-handed and modded long-range inserters are
included. `inspect { logistics { beltRuns } }` walks the belt components reached
from those actual targets and errors if a target changed between snapshots.
`logistics.circuit` is null when an inserter has no control behavior.
`LogisticsEndpoint.inserterSettings` returns filter slots, filter mode, stack
override, engine active state and circuit settings; it is null for non-inserters.

`machines { machines { diagnostics } }` bundles one snapshot of per-machine
diagnosis: `shortfall` (demand for one next craft from input inventories and
compatible fluid boxes, quality-aware), `throughput` (theoretical crafts and
expected base products per second at the current crafting speed, excluding
productivity, downtime and shortages; `actualCraftsPerSecond` is null because a
single snapshot cannot measure it), `blocked` (categorized reason plus the raw
engine status name so unknown future statuses stay visible) and `power`
(electric state, network id, engine "connected to a producing network" flag;
`satisfaction` is null because Factorio 2.0 exposes no per-machine demand
ratio). `diagnostics` is non-null, so a machine whose entity vanished between
snapshot and nested read fails the whole query rather than returning partial data.

`itemFlow` lists configured-recipe and mining-target producers and consumers of
one item on a surface, with the buffered matching items in that role's inventory
only and a theoretical rate per second. `billOfMaterials` recursively expands
enabled recipes for a target quantity against a one-tick stock snapshot, spending
each stored unit once and sharing coproduct surplus across the plan. It reports
`materials`, `steps`, `rawRequirements`, `unresolved` reasons (`RECIPE_CYCLE`,
`EXPANSION_LIMIT`, `NO_ENABLED_RECIPE`, `FLUID_TEMPERATURE_NOT_PLANNED`,
`CATALYST_SEED_MISSING`) and `complete`. `complete: false` means the dependency
tree was bounded or unresolved, not that the item cannot be built. Mined and
fluid leaves appear in `rawRequirements` without making the plan incomplete, and
nothing in the result claims machine, power, transport or immediate craftability.

`scienceBudget` compares the active research's remaining nominal pack demand
(progress and research units included) with retrievable force storage on the
surface, and counts labs that are actually missing a pack. It ignores
productivity, drain and quality effects, so it is a nominal budget. `inspect {
missingSciencePacks }` reports the packs one lab currently lacks and whether the
lab accepts them. `ghosts` lists entity ghosts, tile ghosts and item-request
proxies with real placement alternatives, request counts and scoped area stock;
`buildable` means the materials are present in scope, not that a robot, reach or
logistics network can place them. `inspect { fluidNetwork }` traverses engine
fluid-box connections without crossing a machine's internal boxes, deduplicates
shared engine segment storage, and reports `visitedBoxes`/`truncated` for the
configured `maxBoxes`.

`beltThroughputStart` and `beltThroughput` measure real per-lane item crossings
on one normal transport-belt tile using the navigation mod's
`factorio_ai_belt_flow` remote interface (protocol 1, Factorio >= 2.0.76). Only
item IDs observed on both sides of each lane's fixed midpoint count, so the
result is observed flow (crossings × 3600 / elapsed sampled ticks), not a buffer
delta or belt capacity. Stalled lanes report zero; removal, rotation,
replacement or lane-geometry changes invalidate the sample; `elapsedTicks` stops
at the last valid sample. Splitters, underground belts and loaders are rejected,
32 watches run at once and 128 records are retained. The updated mod must be
loaded from a save before these fields exist. Measurement depends on the game
simulating that belt's chunks: with no player on a surface, belts there can stay
paused and legitimately report zero crossings.

### Entity settings and circuit configuration

`inspect { settings }` reads the configuration Factorio keeps outside recipes
and inventories in one extra snapshot: `splitter` (filter, input/output
priority), `inserter` (filter slots, filter mode, stack size override, circuit
block), `lamp` (color, always-on, circuit color mode), `container` (bar limit
for the requested `inventory`), `logistic` (request sections and slots),
`trainStop` (name, train limit, priority, color), `rocketSilo` (part count,
engine status, automatic launch), and `circuit` (raw control behavior flags and
conditions). Sections are null for prototypes that do not support them, and
reading never creates a control behavior.

Writes are explicit, non-transactional mutations that resolve the acting
character, enforce build reach, and return the post-write state in the same
tick. A splitter filter routes matching items to the output chosen by
`outputPriority`: setting a filter without one defaults it to `left`, and an
explicit `none` is rejected because the filter would be inert; an omitted
comparator keeps the engine default (`=`). The writes are
`setSplitterSettings`, `setInserterSettings`, `setLampSettings`,
`setContainerBar`, `setLogisticRequests`, `setTrainStop`, `setRocketSilo`,
`launchRocket`, and `setCircuitBehavior`. Omitted fields keep their current
values; `clear`, `clearConditions`, and `bar: 0` reset to the engine default
(no filter, no condition, no bar limit). `setInserterSettings` replaces the
whole filter set when `filters` is given, so include every slot you want kept.

`setCircuitBehavior` writes only the fields a prototype's behavior actually
has and fails with the engine message for unsupported ones, for example
`setRecipe` on a container. The first write materializes the control behavior,
which is a persistent save change. `connectWire` and `disconnectWire` link the
red (`circuit_red`) or green (`circuit_green`) connector of two reachable
entities and report `connected` from the engine's own connectivity check.

`launchRocket` only launches from `waiting_to_launch_rocket`; otherwise it fails
with the engine status and the assembled part count, and it reports the engine
status again if `launch_rocket` itself is refused. It cannot bypass payload,
landing-pad, or platform requirements. Splitters in 2.0 have no mirrored
variant, so mirroring is not exposed.

Read queries are snapshots at their reported ticks. Pagination across calls can
shift while the world changes. Missing fields are not proof of a root cause;
engine status and actual inventories are observations, not causal diagnosis.

GraphQL mutations have reach checks, side effects, and uncertain-outcome risks. Multiple mutation
fields execute in order; an error does not roll back earlier actions. Never retry a
failed mutation without inspecting the world first.

### Coordinate conventions

Arguments are not interchangeable between families. Placement commands
(`place`, `placeMany`, `placeBlueprint`) take the placement position: a 1x1
entity at integer `x,y` occupies that tile and then reports its center at
`x+0.5,y+0.5`; 3x3 machines use an already half-integer center; a blueprint
position is its top-left corner. Entity-addressed commands (`inspect`,
`transfer`, `fuel`, `remove`, `rotate`, `setRecipe`, `clearRecipe`, `setFluid`,
`repair`, `setDirection`, settings and metadata writes) match the entity's
reported center, so use the half-integer center from `nearby`/`entities` rather
than the placement tile. `placeTile` takes whole tile coordinates, and
`probePlacement` samples tile centers and reports blocked tile centers. When in
doubt, `worldPlacement`/`probePlacement` answer legality for a prototype and
`entities` reports the actual center after placement.

### Agent actions, speed and travel

`setGameSpeed(input: {speed, paused})` sets the simulation multiplier (0.1–1000)
and optionally the pause flag, returning the post-write tick, speed and paused
state; `status` now reports `speed` and `paused`. This is an administrative
knob: it affects every player and machine on the server.

`teleport(input: {actor, surface, position, snapToGrid})` moves acting character
to finite coordinates through engine teleport call and returns resulting
position. It bypasses travel; build reach still applies afterwards.

`useCapsule(input: {actor, item, target, count})` uses capsule item from player
character main inventory at reachable target and reports created entities.
Player actor only: detached characters cannot use player-input capsules.

`shoot(input: {actor, target, count})` fires player character weapon for `count`
simulation ticks through navigation mod `factorio_ai_actions` job. Mutation
waits and reports ammo spent. Connected player controlling character required.

`repair(input: {actor, target, count})` consumes repair packs from acting
character main inventory and restores health in one scripted step. It reports
before/after health and packs used; base pack durability is 300 HP.

### Construction, deconstruction and robot logistics

`buildGhosts(input: {actor?, force, surface, area, mode, limit})` revives entity
ghosts in area. Script revival bypasses items and construction robots, so both
modes are explicit sandbox shortcuts: `REVIVE` raises built events, `SILENT`
does not. `missing` reports items displaced by collisions, not shortages;
`entities` lists successes and `errors` contains typed failures.

`deconstructArea` now accepts optional `names` and `types` prototype filters, a
`limit` cap, and `instant: true`, which mines reachable targets as the actor
(detached characters included). Targets the actor cannot mine are emptied
first: container contents, modules, fuel, and the entity's own placement item
are spilled on the ground, then the entity is destroyed, so instant removal
never voids items. Recovered `items` report the actor inventory delta plus
everything spilled. `cancelDeconstructArea`,
`deconstructEntities` and `cancelDeconstructEntities` are unchanged.
`EntityInspection.toBeDeconstructed` reports planner state.

`constructionOrders(input: {surface, area?, state, limit, offset})` lists the
entities currently registered with the construction manager: `state` is
`DECONSTRUCTION` (default) or `UPGRADE`, and omitting `area` scans the whole
surface. Each order reports name, type, position, direction, unit number, force
and, for upgrades, `upgradeTarget`/`upgradeQuality`. It is a read-only engine
filter, so the cost does not grow with the base size the way an `entities` scan
plus per-entity `inspect` would. `inspect.statusName` reports
`marked_for_deconstruction` for the same entities.

`logisticNetworks(input: {surface, force, area?, networkId?, includeItemStats,
itemsLimit, itemsOffset})` reports each network served by roboports in scope:
id, custom name, cell count, robot limit, logistic and construction robot
totals/availability, deployed and item-carrying logistic robots, engine
contents, point counts and a point breakdown (roboports, requester/buffer/
provider/storage chests, character request points). Roboports created by scripts
only join a network when built with the built event, so use API placement rather
than raw Lua when testing.

With `includeItemStats` (default true) each network also returns `items`, one
row per item prototype and quality, ordered by name then quality and paginated
per network by `itemsLimit`/`itemsOffset` with `itemPageInfo`:

- `stored` is physical stock summed from provider, buffer and storage chest
  inventories, split into `storedPassiveProviders`, `storedActiveProviders`,
  `storedBuffers` and `storedStorage`. Robot hands and requester chests are not
  part of it.
- `available` is the engine's `get_contents()` number. Provider contents
  subtract items committed to robot pickup, so an over-committed row can be
  negative even while `stored` is positive (verified live:
  `stored 3 - targetedPickup 4 = available -1`).
- `requested`/`requestedMinimum` sum the compiled requester-chest maximums and
  minimums; `unlimitedRequests` counts slots with no maximum. `delivered` is
  stock already inside requester chests, `incoming` is items committed for
  delivery, `targetedPickup` is items committed for pickup, and `inFlight` is
  cargo currently held by deployed logistic robots.
- `missing = max(0, requested - delivered - incoming)`. A row with
  `missing > 0` plus `stored > 0` and `targetedPickup = 0` points at a robot
  throughput/power problem rather than a production shortage; `stored = 0`
  points at supply.

Scope is logistic chests. Character personal-logistics requester points are
counted in `points.characterRequestPoints` but excluded from item demand and
stock, as are roboport robot inventories. Construction logistics and ghosts are
separate (`ghosts`, `constructionOrders`).

### Fluids

`setFluid(input: {actor, target, fluid, amount, temperature, mode})` writes one
reachable entity's fluidboxes. `SET` clears then inserts (clamped to capacity),
`ADD` inserts into existing content and reports accepted amount, and `CLEAR`
empties every box. `before`, `after`, `capacity`, and `boxesAffected` come from
same post-write snapshot.

`fluidStock(input: {surface, force, area?, fluid?})` aggregates fluid contents of
pipes, tanks, fluid-using machines and fluid wagons, with independently paginated
locations and totals. Temperatures appear only when the engine exposes them per
fluid entry.

### Trains and placement directions

`trains(input: {surface, force, area?, station?})` lists rolling stock with
engine state, manual mode, speed, schedule records, cargo and fluids.
`setTrainSchedule` replaces a train's schedule with time-based records (or
`clear: true`), and `trainControl` sets manual mode and can send the train to a
named schedule station. Both resolve the entity (locomotive or carriage), enforce
reach, and return the post-write train snapshot.
Schedule reads expose first common wait condition as typed `waitCondition`;
`waitConditionRawJSON` preserves complete unmodified engine condition list for
unsupported or modded fields from same snapshot. Schedule writes accept typed
wait conditions only; no JSON string input is parsed.


Placement inputs (`place`, `placeMany`, `placeBlueprint`,
`createBlueprintFromEntities`) accept one canonical engine `direction: Int`
from 0 through 15, defaulting to 0. This represents 4-way, 8-way, and 16-way
prototypes without contradictory aliases. Cardinal-name conversion belongs in
clients. `setDirection(input: {actor, target, direction})` writes existing
entity direction, verifies readback, falls back to one quarter turn when
applicable, and otherwise fails: Factorio 2.0.77 silently ignores scripted
direction writes for belts and rails, so place those with desired direction.

### Character gear, requests and research

`Actor.equipmentGrid` reads armor grid and `equipGrid(input: {actor, item, count,
direction: TO_GRID|TO_MAIN})` moves items between main inventory and grid one
equipment at a time; armor with grid must be equipped first.

`Actor.logisticRequests` and `setActorLogisticRequests(input: {actor, sections,
clear})` read and replace character requester sections using same section/slot
shape as containers. `setLogisticRequests` also accepts rocket silos, selecting
silo requester point.

`queueResearch(input: {force, technology})` appends a technology to the queue,
`clearResearchQueue` empties it while current research continues, and
`unlockAllTechnologies(input: {force, mode: RESEARCH|ENABLE})` is an explicit
sandbox shortcut.

### World, circuit and safety reads

`surfaces(force)` returns `{tick, force, surfaces}`; each surface carries index,
planet, total pollution and evolution factor. `circuitNetwork(input:
{target: {entity|unitNumber}, color})` returns signals visible on an entity's
red or green network plus connection counts.
`safety` now also reports `evolutionFactor` with its time/pollution/kill
components, `totalPollution`, and per-type `killCounts`.

### Item writes, quality and inspection metadata

`dropItem(input: {actor, surface, item, quality, count, position, target})` spills
items as ground stack (`GROUND`) or inserts into transport belt lane (`BELT`),
removing them from actor inventory first and rolling back on failure.

Quality is no longer hardcoded to `normal`: `place`, `placeMany`, `transfer`,
`fuel`, `beltTake`, `equip`, `placeTile` and `dropItem` accept quality prototype
name. Placement inputs accept `fastReplace` to replace fast-replaceable entity
instead of failing on collision. `placeMany` returns entity unit number, name,
type, and position for each success. `transfer.inventory` is typed
`InventoryKind`, including roboport, wagon, car, and spidertron inventories;
`transferDirection` in result uses same closed enum accepted by input.

`nearby` accepts explicit `order` (`DISTANCE` or `UNIT_NUMBER`), zero-based
`offset`, and `limit` (1–1000). `pageInfo` always reports `total`, `limit`,
`offset`, and `hasMore`; ties use unit number, name, position, then type.
`EntityInspection` adds `unitNumber`, `nameTag`, `tagsJSON`,
`combinatorParameters` and `parametersRawJSON`, plus `undergroundNeighbour`,
`beltNeighbours` and `fluidboxNeighbours`. Arithmetic and decider combinators
use typed parameter objects; unsupported parameter-bearing prototypes retain
unmodified engine data only in `parametersRawJSON`.
`setCombinatorParameters(input: {actor, target, parameters})` replaces either
arithmetic or decider parameters. Exactly one parameter family is required;
signal-vs-constant operands are mutually exclusive, arithmetic operations are
limited to Factorio's `* / + - % ^ << >> AND OR XOR`, and decider condition and
output lists contain 1–100 entries. Invalid input fails before RCON.
Factorio 2.0.77 exposes no underground-neighbour API, so
`undergroundNeighbour` is always null there and says nothing about pairing;
`beltNeighbours` and `fluidboxNeighbours` report engine neighbours when
available. `setNameTag` writes or clears map label. `setEntityTags` exists but
fails loudly on normal entities: Factorio 2.0.77 stores tags only as
blueprint/ghost metadata.
`addChartTag` creates a force-visible marker at any finite map position with
nonblank text, a signal icon, or both. `chartTags` lists markers for one force
and surface, optionally inside an area, with stable tag-number ordering and
pagination. `removeChartTag` deletes one marker by its force-unique tag number
and surface. These operations use Factorio custom chart tags and do not require
an entity or actor; they are distinct from entity `nameTag` and blueprint
metadata `tagsJSON`.
Snapshot invariants: every game read/write payload has observation `tick`;
spatial payloads have `surface`; force-scoped payloads have `force`; actor
mutations have `actor` preserving selected player or character selector. These
fields come from operation snapshot and are non-null where applicable.
`FactoryOverview` intentionally uses `productionTick` and `machineTick` because
its two reads are separate snapshots. `save`/`shutdown` return only server
response because server commands do not execute snapshot Lua. Nested
`@goField(forceResolver: true)` relationships are documented independent reads
and may have later ticks. Persistent navigation and belt measurements separate
observation `tick` from `startedTick`/`updatedTick`; navigation `force` is null
only after character disappears. Unknown status names map to `OTHER`, preserve
raw name/code, and are conservatively nonterminal.

### Recipes

`setRecipe` accepts nested `actor`, `recipeQuality`, and `drain` mode:
`ABORT` keeps original empty/idle contract, `TO_ACTOR` refuses machines holding
fluids and drains input/output into actor before change, and `SPILL` clears
fluids and spills drained contents at machine. Returned stacks come from
pre-change contents plus anything engine releases.
`clearRecipe(input: {actor, target})` drains machine into actor and clears recipe.

## Security

RCON grants administrative access and is unencrypted. Do not expose it to the internet or give untrusted agents the password. `.factorio/password` has restrictive permissions, but sufficiently privileged local processes can read it; Factorio also receives it as a command-line argument. A holder can execute arbitrary administrative Lua outside this CLI. The `serve` endpoint exposes the same mutations without authentication; keep it bound to localhost. Factorio may reject the first Lua command pending confirmation, producing an empty response; the CLI's completion command may already confirm and disable achievements. Check the server log before retrying a mutation: an interrupted or failed response does not prove the action did not run.

Lua commands share response handling: reported Lua exceptions are surfaced as server errors, but an error or missing response does not guarantee a mutation was rolled back. Inventory rollback after an unexpected engine failure is best-effort; inspect the affected inventories and world before retrying.

When a client is connected, Factorio can return the RCON completion marker before the command result. The CLI waits for the result and sends another marker to collect its remaining packets. A timeout still leaves mutation outcome unknown.
