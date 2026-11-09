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

## New game settings

The repository ships the settings used for new worlds:

| File | Use |
| --- | --- |
| `map-gen-settings-regular.json` | Regular freeplay map (`--create`). |
| `map-gen-settings-sandbox.json` | Sandbox scenario map (`--start-server-load-scenario sandbox`). |
| `map-settings.json` | Map settings for either: enemy evolution and expansion disabled. |

Both map-gen files disable enemy bases (`"enemy-base": {"frequency": 0, "size": 0, "richness": 0}`) and set
`peaceful_mode`; ore and oil patches are more frequent, larger and richer (frequency 2, size 3, richness 6)
while water and trees keep their defaults. Edit those files to change a new world; they are only read when the
save is created. `game.map_settings` is mutable at runtime, `game.surfaces[1].map_gen_settings` is not.

Every creation flow must keep new worlds enemy-free. `just new-game` refuses to run when the files drift, and
`just world-settings-check` runs the same check for the manual sandbox steps; a verified world reports
`safety.enemies.pageInfo.total == 0` with `evolutionFactor == 0`. An existing save keeps the settings it was
created with, so a save born with biters stays hostile until its spawners are removed in game.

These files cover the starting surface (Nauvis): `autoplace_controls` is per-surface map generation, so with the
Space Age feature enabled, Vulcanus, Gleba, Fulgora and Aquilo still generate their own enemies when first
visited — only the shared `enemy_evolution`/`enemy_expansion` blocks in `map-settings.json` apply to them. The
managed save (`ai-player-sandbox.zip`, `evolutionFactor` 0.245 driven by `evolutionByTime`) was created before
these files existed and is therefore not covered by them.

The managed server must be stopped first (the second Factorio instance cannot share the default write-data
directory), and never overwrite an existing save:

```sh
just stop
S="$HOME/Library/Application Support/factorio/saves"
FACTORIO_BIN="${FACTORIO_BIN:-/Applications/factorio.app/Contents/MacOS/factorio}"

# Regular freeplay save: character, hand crafting, normal progression.
"$FACTORIO_BIN" --create "$S/ai-player-new.zip" \
  --map-gen-settings "$PWD/map-gen-settings-regular.json" \
  --map-settings "$PWD/map-settings.json"

FACTORIO_SAVE="$S/ai-player-new.zip" just start
```

Creating a sandbox is a scenario conversion, not `--create`: run the throwaway write-data/server steps from
`AGENTS.md` ("Starting a new sandbox game") and pass the sandbox files to the scenario load:

```sh
D=/tmp/factorio-new-sandbox   # write-data dir prepared per AGENTS.md step 2
"$FACTORIO_BIN" -c "$D/config.ini" --mod-directory "$HOME/Library/Application Support/factorio/mods" \
  --start-server-load-scenario sandbox \
  --map-gen-settings "$PWD/map-gen-settings-sandbox.json" \
  --map-settings "$PWD/map-settings.json" \
  --bind 127.0.0.1:34198 --rcon-bind 127.0.0.1:27016 \
  --rcon-password "$(cat "$D/password")" --server-settings "$PWD/server-settings.json"
```

`--map-gen-settings` and `--map-settings` are accepted by `--start-server-load-scenario` as well as
`--create`; use a new save name and the scenario's generated save (`<write-data>/saves/sandbox.zip`) as the
managed save.

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

`characterSpawn` creates a detached character with no player at the closest non-colliding character position within 10 tiles of finite coordinates on an existing surface and force. It costs no items, the character starts with empty inventories and its own reach, and the returned `character` is its unit number. Characters created this way persist in the save and are never cleaned up automatically, so destroy unused ones with `characterDelete(input: {character, force})`: it destroys one detached character only after rejecting any character owned by a player (including a disconnected player's stored character and the human's character), any force mismatch, a nonempty crafting queue, and any items in the main, guns, ammo, armor, or trash inventories, so nothing is ever voided. Every actor input accepts that unit number, so spawned characters can be driven independently: `craft`, `mine`, `remove`, `transfer`, `equip`, placement mutations, and `navigateStart`.

`playerRestoreCharacter(input: {player})` creates and attaches character for connected player controlling none, returning unit number, surface, and position. It leaves main inventory untouched and rejects player already controlling character. `Actor.character` reports current character unit. Actor-scoped mutations, including `navigateStart`, accept shared `actor: {player|character}` input; navigation resolves player to current character before starting.

`characters(input: {surface, detachedOnly, limit, offset})` lists characters across a whole surface, sorted by unit number; `detachedOnly` defaults to true, and `detachedOnly: false` includes connected players' characters, whose `player` name is reported. Like `entities` and `entityCount`, it scans the whole surface on each call instead of requiring an area. Detached characters cannot chat, shoot, or open GUIs because those are player-driven.


Save detached character's returned unit number as string; it is not player name. `nearby` sorts deterministically by distance or unit number and returns standard zero-based pagination. Mining, removal, pickup, and character-backed placement and transfers require reach. Mining uses Factorio's mining operation and real drops/depletion but is **instantaneous**, not timed hand-mining. Movement uses `navigateStart` pathfinding job; no raw walking-input mutation exists.

`craft` uses normal crafting queue and consumes ingredients (unless the player is in cheat mode: `player`/`playerInsight` `cheatMode` is true, crafting is then free and instant, and null means unknown, as for a detached character). `requested` is requested crafts, not items: recipe yielding two products produces twice as many items; `started` may be smaller. By default, mutation returns immediately; queued crafts may still be pending. `place` checks collision and consumes selected item quality. `inspect` reports entity state, applicable inventories (including machine modules, turret ammo, and character main/guns/ammo/armor/trash), fluid contents, and force-wide production totals for that surface (not per-machine totals). `transfer` moves selected item quality between character and typed entity inventory; it reports actual `moved`, the destination's `bar` (null when it has none) and `toBeDeconstructed` (a marked destination blocks insertion), and when nothing moves it reports `reason` (`BAR_LIMITED`, `NO_CAPACITY`, `NO_SOURCE`, `MARKED_FOR_DECONSTRUCTION`, `FILTERED`) instead of a bare zero.

`equip` moves selected item quality between actor main inventory and a `GUNS`, `AMMO`, `ARMOR`, or `TRASH` inventory; use `direction: TO_MAIN` to unequip. Armor slots accept only one item, so unequip before swapping. `inspect` reports character main, guns, ammo, armor, and trash inventories. Equipping does not fire weapons.

Set `wait: true` to poll until actor's entire crafting queue is empty, including crafts queued before request. `waitTimeoutSeconds` defaults to 10 and must be at least 1; larger values are clamped to 10 seconds. On timeout, mutation returns `craftingComplete: false`, `waitTimedOut: true`, latest `queueSize`, and `waitedSeconds`; it does not cancel queued work. Without wait, these status fields are null. Example: `mutation { craft(input: {actor: {character: "123"}, recipe: "iron-plate", count: 10, wait: true, waitTimeoutSeconds: 10}) { started queueSize craftingComplete waitTimedOut waitedSeconds } }`.

`craftPlan` expands a hand-craft into its dependency chain. `query { craftPlan(input: {actor, recipe, count}) }` is read-only: it uses the actor's main inventory as stock and recursively expands every missing item ingredient through the enabled, non-hidden, hand-craftable producer recipe with the lowest name when several match, returning `steps` bottom-up — each step's ingredients are in stock or produced by an earlier step — plus `missing`, the aggregated shortfall of ingredients that cannot be hand-crafted at all (fluids, or items whose producer is machine-only, disabled, hidden, or unavailable). `complete` is true when `missing` is empty. `count` counts crafts of the named recipe, matching `craft`; a step's `count` is crafts too, so recipes yielding several products over-produce rather than under-produce. Planning ignores quality and probabilistic yields (it uses `amount`, then `amountMin`) and gives up on a chain deeper than 16 levels. Recycling recipes and recipes whose product is probabilistic are never chosen as producers (scrap-recycling yields steel-plate with probability 0.04), so such an ingredient lands in `missing`. Stock is the main inventory plus queued products minus queued ingredients, because queued crafts only take their ingredients as each craft starts. `mode: UP_TO` makes `count` the wanted product total of the recipe's first item product instead of an increment (`need = count - (inventory + queued products - queued ingredients)`), so re-issuing the same request is idempotent. There is no separate plan mutation: `mutation { craft(input: {actor, recipe, count, expand, mode, wait, waitTimeoutSeconds}) }` is the single craft entry point. `expand: false` (default) queues `count` crafts of exactly that recipe (`started` can be below `requested`); when nothing queues, the error names the reason (not hand-craftable, or the ingredients the hand-craft chain lacks). `expand: true` performs the plan above and returns it in `chain` (`complete`, `queued`, `remaining`, `missing`): steps queue bottom-up and nothing is queued when `missing` is non-empty. With `wait: false` only what is affordable now is queued and the rest stays in `chain.remaining`; with `wait: true` the queue drains between layers, bounded by `waitTimeoutSeconds` (default 10, clamped), and on expiry `waitTimedOut` is true. A timed-out call is resumed by re-issuing the same request with `mode: UP_TO`. Example: `mutation { craft(input: {actor: {character: "123"}, recipe: "inserter", count: 1, expand: true, mode: UP_TO, wait: true}) { chain { complete queued { recipe count } missing { name amount } remaining { recipe } } waitTimedOut } }`.

`cancelCraft` cancels up to `count` crafts starting at the one-based queue `index` and returns the remaining queue. Canceling a craft can also cancel later crafts whose ingredients depended on it. `cancelResearch` stops the force's current research and returns the resulting state; the engine removes dependent technologies from the research queue.

`dropItem` seeds a line or the ground from an actor's main inventory. The
position snaps to its tile center. `target: GROUND` refuses a tile that holds a
belt, splitter, loader or linked belt: the engine would otherwise create the
stack under the belt and it would be lost while the mutation reported success.
Use `target: BELT` for belt tiles; a rejected drop leaves the inventory intact.

`placeBelts` is the routed way to place transport belts of any tier, and `placePipe` the one for pipes. Both use the same planner/applier as `connectEntities`, so ASTAR routing, underground crossings, endpoint reuse and side-load/fluid-adjacency refusal behave identically. `place` and `placeMany` reject transport belts and pipes with an error naming the correct mutation; single `underground-belt` halves, `pipe-to-ground`, `splitter` and `loader` stay directly placeable because the routed mutations cannot place those half/single-tile forms.

`placeBelts` is the belt-named front door to `connectEntities` with `kind: BELT`: its `BeltRunInput` carries only belt-relevant fields (no `kind`, no `allowFluidAdjacency`) and it converts into the same `ConnectEntitiesInput` and planner/applier, so results, refusals and `direction` rules are identical. `connectEntities` stays the generic entry point for belts, pipes and power-pole chains.

`placeMany` places one selected-quality item at each of up to 10000 explicit positions in one RCON call. `placements` entries use entity-center coordinates and optional engine `direction` 0–15 (default 0/north). Underground belt halves pair by facing side, not by call: pass `undergroundType` `INPUT` on the entrance and `OUTPUT` on the exit, and the engine pairs a half with the first facing half of the same tier, in this batch or a later call. Without `undergroundType` both halves are created as inputs and never connect. Without `fastReplace` a position whose tile holds an entity whose collision mask intersects the placed prototype's mask fails with `COLLISION` naming the occupant before the engine is asked to create anything (`create_entity` ignores collision boxes and would otherwise stack a second entity on the tile); `place` and `placeMany` share that tile-wide rule, so a neighbouring footprint that clips the tile counts, while ghosts, tile ghosts, item stacks, resources and corpses (doodad layer, tiles only, so a tree stump never blocks a build) are never named; `fastReplace` first proves the tile with the engine's `can_fast_replace` (or a legal manual placement) and otherwise fails the position with `COLLISION`; the acting character is passed to the engine, so the replaced entity and its contents come back to that actor instead of landing on the ground. `place` accepts the same `undergroundType` and otherwise, for an underground belt, creates the output half when an unpaired input half sits within underground distance behind the target in the same direction. Each position is probed after a refused creation: the engine can raise or return nil after committing an entity, so a committed entity is reported as placed instead of as an `ENGINE` failure with a refunded, duplicated item. `errors` contains typed one-based index, stable code, message, and target for first 20 failures; batch continues. Operation is not transactional, so inspect world before retrying. Player placement uses player's current character; both actor variants require character on selected surface and within build reach.

`connectionPlan` plans one axis-aligned straight or single-bend run for `kind` `BELT`, `PIPE`, or `POWER_POLE_CHAIN` and returns the snapped tile centers, per-tile engine `direction`, whether each tile already holds the requested entity, the item count `needed`, and typed `conflicts`. Positions snap to the center of the containing tile; the default items are `transport-belt`, `pipe`, and `medium-electric-pole`. `bend` `AUTO` plans both orientations and keeps the fewer-conflict route, tie to `HORIZONTAL_FIRST`; a straight route reports the orientation it lies on. A tile holding the same entity name with the needed direction is `existing` and costs nothing; a same-name belt facing elsewhere reports `DIRECTION_MISMATCH`. `DIRECT` plans surface tiles only, so an obstruction is a `COLLISION` with blocker prototype names, terrain flag, and ground tile. `PIPE` runs also report `FLUID_ADJACENCY` when a pipe-like entity outside the run touches a route tile, because pipes join orthogonally and would merge fluid networks; pass `allowFluidAdjacency: true` to merge deliberately. `POWER_POLE_CHAIN` places one pole every `floor(wire reach)` route tiles, so consecutive poles stay within wire distance and the engine wires them on build.

`routing: ASTAR` (default `DIRECT`) replaces the fixed route with a bounded shortest-path search for `BELT` and `PIPE`; `POWER_POLE_CHAIN` stays direct. One RCON read samples the endpoint bounding box padded by `searchMargin` (0–256, default 32, at most 65536 tiles): placeability of the route entity, existing same-name entities, underground entities and pipe-like entities. The search itself runs in Go and is deterministic, so a plan and its apply agree. Step costs are 1 per new tile, 0.25 per reused existing tile, 0.25 per bend and 2 per underground half; the plan reports `cost`, `expanded` states, `stopReason` (`FOUND`, `NO_PATH`, `EXPANSION_LIMIT` when `maxExpandedTiles` (1–200000, default 10000) runs out, `ENDPOINT_BLOCKED`) and a matching typed conflict instead of an error. `allowUnderground` lets the route tunnel with `undergroundItem` (default `underground-belt` for belts, `pipe-to-ground` for pipes) at an offset of at most the smaller of `maxUndergroundDistance` and the prototype's max underground distance; underground steps carry `undergroundType` `INPUT` for the entrance and `OUTPUT` for the exit, and `undergroundNeeded` counts the halves. A span may pass beneath any entity except another underground belt, linked belt or pipe-to-ground, and beneath `avoidAreas` (at most 100), where no surface entity is placed. Underground belt halves face the travel direction; a pipe-to-ground faces its surface connection, so an eastbound pair is `12` then `4`. A pipe-to-ground connects only on that side, so unlike regular pipes it may sit beside a foreign pipe. A belt feeds an existing belt at the goal from behind or the side but never head-on; `preferExisting` lets a belt route reuse existing belts that already face its direction, and reusing a pipe also needs `allowFluidAdjacency` because it joins networks. ASTAR-only fields are rejected on `DIRECT`, never silently ignored. Placement re-checks every tile, so a world that changed between plan and apply yields per-step errors; items are checked before any removal.

`connectEntities` plans the same run and then places it. It refuses to change the world when the plan has conflicts (`possible: false`, `placed: 0`) or when the actor lacks the planned item count, so a refused call is net-zero; each placed tile consumes one normal-quality item from the actor's main inventory, existing tiles are skipped, and reach applies per tile when `FACTORIO_ENFORCE_REACH=1`. Engine failures are reported per step with one-based index, stable code, message, and target. It prints the usual notification only when at least one entity was placed. Besides `from`/`to` tiles, `fromEntity`/`toEntity` accept an entity reference with an optional preferred side and lane, so the planner picks the adjacent route tile and orientation itself and a side-loading goal belt reports its chosen `lane`. An entity reference resolves by `unitNumber` for any prototype (machines, chests and belts included; the engine's direct unit-number lookup only covers a few prototypes, so the resolver scans the surface) or by position. For `PIPE` runs a fluid machine endpoint resolves to one of its fluidbox pipe-connection target tiles (the `target` of `inspect.fluidPorts`) instead of a tile beside its centre; with several ports (a refinery has five) the preferred `side`, then the port facing the other endpoint, then the first free port decides, so pass `side`, or `from`/`to` set to a `fluidPorts` target, when a specific port matters. A belt outside the run that points into a new route tile is a `BELT_ADJACENCY` conflict, because it would side-load foreign items or straighten a planned corner; `allowBeltAdjacency: true` accepts that merge.

`placePipe` accepts the same fields as `connectEntities` except `kind`, which it fixes to `PIPE`, and delegates to the same planner/applier. `connectEntities`, `placeBelts` and `placePipe` accept `direction` (0 north, 4 east, 8 south, 12 west) for a run whose `from` and `to` resolve to the same tile: the route implies no heading there, so the mutation requires it and otherwise fails before any Lua runs; a run of two or more tiles rejects `direction` because every tile follows the route. `connectionPlan` refuses single-tile routes for the same reason. Each created entity in the result's `entities` reports the engine `direction` it was built with (north is reported as `0`, not null) and, for underground halves, `undergroundType`, so the placed heading needs no second `entity()` read. Prefer them over `place`/`placeMany` for continuous belts and pipes: they route the run, can use underground pairs against crossings, and reject foreign side-loads/fluid adjacency by default. `place` and `placeMany` reject `transport-belt`-type items and `pipe` with an error naming `placeBelts`/`placePipe`; single underground halves, splitters, loaders, and `pipe-to-ground` remain directly placeable.

At a machine's filtered fluid port, Factorio can reject pipe placement with `COLLISION` and an empty `blockers` list when the new pipe would join an incompatible fluid (for example, a water-filled pipe beside a refinery's petroleum-gas outlet). `allowFluidAdjacency: true` permits the planner's network merge check; it does not override the engine's fluid compatibility rule. Inspect and clear the incompatible fluid before retrying. A longer run through the same tile does not bypass this refusal.

Three belt workflows, by intent: `connectionPlan` is read-only planning; `connectEntities` with `kind: BELT` places one tile (`from` equals `to` plus `direction`) or a routed stretch; `buildConnection` plans and places in one call. `buildConnection(input, dryRun)` takes the `connectEntities` input and returns `{ plan, result }` from one call: it runs the same `connectionPlan` and `connectEntities` code (no second router), so inventory use, underground spans, adjacency opt-ins, existing tiles and conflicts behave identically. `dryRun: true`, or a plan with conflicts, returns the plan with `result: null` and places nothing; a possible plan is applied and per-tile failures are reported in `result.errors`, so re-read the world after a failure. Neither mutation is transactional. Saved operations: `connection-plan`, `connect-entities`, `build-connection`.

`buildCell(input)` builds a declarative cell: `entries` (at most 200) are applied in order, each exactly one of `place` (one entity), `connection` (a routed run) or `recipe` (a recipe on a machine named by an earlier entry's `ref` or by an explicit `target`). Every entry is validated, and refs checked, before any write. `dryRun: true` stops after preflight and writes nothing. Application stops at the first failed step and later entries are reported `applied: false`; earlier steps are not rolled back. Saved operation: `build-cell`.

`occupancy(input: {surface, force, area, includeTerrain, terrainLimit, limit, offset})` is a machine-readable occupancy report of one area (at most 1024 tiles per side): each entity reports `footprint` (prototype tile size, swapped for east/west directions, centred on the position), `direction` and `unitNumber`; ghosts report the prototype they will become in `ghostName`. `includeTerrain: true` also lists non-ground-tile blockers (water) up to `terrainLimit` with `terrainTruncated`, and needs an area of at most 65536 tiles. It does not decide placement legality for a particular prototype; use `worldPlacement`/`probePlacement` for that. Saved operation: `occupancy`.

`waitUntil` polls exactly one condition from the backend until it holds or a 1–10 second wall-clock timeout expires (default 10; larger values are clamped); `pollSeconds` is 0.05–5. Conditions are `ticks` (wait N game ticks from the first poll, so the wait follows game speed), `inventory` (actor main inventory holds N of an item/quality), `craftQueueEmpty`, `machineStatus` (entity status name in a set), `research` (technology researched), `fluid` (entity holds N of a fluid), and `production` (force produced N of an item in a production window). Each poll is a separate RCON read, so the game thread and other agents are never blocked and the game keeps simulating between polls; nothing is mutated and no notification is printed. `satisfied` and `timedOut` are exclusive, `polls` counts reads, and `last` reports the final observation. A condition that becomes unobservable — removed entity, unknown technology — is an error rather than a timeout, because the outcome is unknown; pass the prototype name with a unit number so the lookup can fall back to a surface scan.

`prototype(name)` reads static engine prototype data in one call so callers stop guessing footprints and stack sizes. `item` reports `stackSize`, `stackable`, `placeResult`, `fuelCategory`, `fuelValue`, and `burntResult`. `entity` reports `type`, whole-tile `tileWidth`/`tileHeight`, relative `collisionBox`/`selectionBox`, `maxHealth`, `beltSpeed`, `maxUndergroundDistance`, inserter pickup/drop offsets, `wireReach`, `supplyArea`, `craftingSpeed`, `miningRadius`, `miningSpeed`, sorted `resourceCategories`, `energyUsageWatts`, `moduleSlots`, sorted `craftingCategories`, `fluidBoxCount`, and `mineable`. Exactly one of the two is set unless both prototypes share the name; an item with no same-named entity falls back to its place result, which can carry a different entity name. A name with neither prototype is an error.

`placeNextTo` places one item beside an anchor entity using both prototype bounding boxes, so callers do not compute footprints. `side` is `EAST`, `WEST`, `NORTH`, or `SOUTH`, `gap` is whole tiles between bounding boxes, and `direction` must be cardinal (0, 4, 8, or 12); rotated footprints swap width and height. The mutation snaps the preferred center to the tile grid, tries deterministic half-tile nudges, and keeps only positions that leave at least `gap` tiles on the requested side and hold no collidable entity (an occupied tile is skipped, because `create_entity` ignores collision boxes and would stack a second entity there). It requires the anchor on the actor's force and surface, consumes one selected-quality item, reserves nothing on refusal, and refunds the item if engine creation fails; when the engine raises or returns nil after committing the entity, the probe finds it on the tile and the call reports that entity instead of refunding, so no item is duplicated. The result reports the placed entity, its position, the anchor unit number, side, and gap.

`fuelMany` fuels 1–1000 explicit entities and `transferMany` moves one item between the actor and a typed inventory of 1–1000 explicit entities, each in one RCON call. Targets use the shared entity reference (a unit number with the prototype name, or the name with its center position) and carry their own surface. Duplicate targets are rejected before any call. Per-target semantics match the single operations: `fuelMany` inserts exactly `countPerTarget` fuel — or fills each target's fuel inventory up to `topUpTo` items instead, which is idempotent: a target already holding them is reported as skipped and nothing is added — or the target fails with a typed error (`LOOKUP`, `VALIDATION` for an entity without a fuel inventory, `REACH` under `FACTORIO_ENFORCE_REACH=1`, `INVENTORY` when the actor lacks fuel or the fuel inventory cannot take the full count, `ENGINE`), so a full burner is never partially filled; `transferMany` moves `min(countPerTarget, available, capacity)` honouring stack limits and bar slots, and a short move is reported in `results` without an error. Both continue past failures and report `failed`, at most 20 typed `errors` with one-based index and target identity, and per-target `results` for the targets reached. `moved` totals the batch and `transferredStacks` counts source stack moves that moved at least one item. They are not transactional: nothing is rolled back, so inspect state before retrying a batch whose response was lost. There is no area scan; pass entities from `machines`, `nearby` or `entities`.

`placeTile` places one tile item such as landfill, concrete, or foundation after checking the item's prototype placement rule against the current tile and rejecting tiles where an entity (other than characters and resources) has a collision layer shared with the tile. It consumes one selected-quality item and returns the placed tile name plus `previousTile` (the tile it replaced, null on empty ground) and `items` (mineable products of the replaced tile, returned to the actor). Tile items whose condition spans more than one tile are rejected. The acting character, or the selected player's current character, must be on the surface and within build reach.

`createBlueprint` exports a blueprint string from blueprintable entities inside an area; `includeModules`/`includeFuel` default on, tiles/trains/station names default off. `createBlueprintFromEntities` builds the same string from explicit blueprint-relative entities and tiles instead of copying the world; supports entity `name`, position, direction, quality, recipe, and mirror flag. `placeBlueprint` imports that string and places **entity ghosts** at finite coordinates, rotated by `direction` — a blueprint only turns in 90° steps, so `direction` must be 0, 4, 8 or 12 and anything else is rejected before the RCON call; it does not consume items and does not build real entities, and the result reports `ghosts` placed, the blueprint's `entityCount` and `skipped` when the engine refused some entities. `placeGhosts` places the same ghosts directly from explicit entity and tile entries without a blueprint string. Real construction needs construction bots or manual building. Blueprint strings are limited to 2,000,000 characters.
`blueprintBooks` and `blueprintBook` read a **blueprint catalog** of named blueprint books that lives in the save's script storage, and `createBlueprintBook`, `importBlueprintBook`, `updateBlueprintBook`, `deleteBlueprintBook`, `addBlueprintToBook`, `addAreaToBook`, `updateBlueprintInBook`, `removeBlueprintFromBook`, `moveBlueprintInBook`, and `placeBlueprintFromBook` manage it, while `giveBlueprintBook` copies one catalog book into a connected player's main inventory as a real `blueprint-book` item (optionally into a chosen one-based slot, otherwise the first empty slot); the catalog string is imported into a temporary script stack and swapped into the inventory, leaving the catalog entry unchanged. A book is persisted as its exported blueprint-book string, so the catalog needs no player, character, or container (it works in sandbox saves) and survives save/load; every call imports the stored string into a temporary script inventory, mutates or reads the item, and writes the string back only on success, so a failed call leaves the stored book untouched. A book is summarized by `name`, `label`, `description`, `slotCount` (allocated slots including holes), `entryCount` (occupied slots), `activeIndex` (one-based, null for an empty book), `characters`, and `updatedTick`; `blueprintBook(includeBlueprint: true)` also returns the book string and each entry's string, which is how a catalog moves to another save. Entry `index` is one-based and stable: the engine only creates and fills book slots in order, so an add must target the book's first free slot (its hole keeps its number, or one past the last slot grows the book), a cleared slot keeps its number while anything follows it and stays available for a later add, and a move swaps two existing slots. The catalog holds at most 200 books, a book at most 1000 entries, and the whole catalog at most 16,000,000 characters, with names of 1-64 characters without control characters. `placeBlueprintFromBook` places one entry's ghosts (an explicit `index`, otherwise the book's active entry) with the same normal build mode and 0/4/8/12 rotation rules as `placeBlueprint`. Example: `mutation { addBlueprintToBook(input: {name: "bus", blueprint: "0eNq...", label: "iron-smelter"}) { book { entryCount slotCount } entry { index label entityCount } } }`.

The `rotate` mutation rotates a reachable entity by 1–3 clockwise quarter turns via its `turns` input. Inserter placement direction specifies its pickup side, not its drop side. `inspect` reports inserter pickup/drop positions and transport-belt lane contents. `beltTake` moves up to `count` items of the requested quality from a reachable transport belt of any tier into the acting character's inventory; supply either `character` or `player`, and the acting character must be within reach of the belt. `lanes` (`LEFT`/`RIGHT`) restricts the take to one lane and defaults to both, a requested lane the belt does not expose is an error, and the left lane is emptied first. If the inventory rejects part of the removed items, the remainder is spilled near the character and the call fails instead of reporting a clean move. `beltPurge` deletes one item and quality from the selected lanes of every transport belt in an area (1024 tiles per side) or of one belt, leaves other items and lanes untouched, stops after `limit` changed belt tiles, and returns `tilesVisited`, `tilesChanged` and `removed`.

`inspect { logistics { tick inserters { inserter { name position { x y } pickupPosition { x y } dropPosition { x y } heldStack { name count } } pickup { name type position { x y } lanes { index items { name count } } } drop { name type position { x y } lanes { index items { name count } } } } } }` resolves inserters whose engine pickup or drop target is the inspected entity. Endpoints can be machines, belts, containers, other entities, or null; belt endpoints include lane contents. The resolver scans inserters across the surface, including long-handed and modded long-range inserters, without a distance limit. It performs a separate RCON read, so `logistics.tick` may differ from the inspection snapshot; a removed or replaced entity can make the nested read fail.

`clearArea` immediately destroys tree entities and simple entities whose prototype names contain `rock`, within the required rectangular area; centers must lie inside the inclusive bounds. It removes entities directly, without mining drops. Each dimension must be positive and at most 1024 tiles. Response reports `treesRemoved` and `rocksRemoved`. Example: `mutation { clearArea(input: {surface: "nauvis", area: {leftTopX: 0, leftTopY: 0, rightBottomX: 32, rightBottomY: 32}}) { surface treesRemoved rocksRemoved } }`.

`remove` mines one entity on actor's current surface, using normal mining so actor recovers entity and contents. Input uses `actor` plus `target`; target is exactly unit number or prototype name plus a position inside the entity (its exact centre is not required, and neutral-force rocks, trees and resource tiles are accepted). Actor must reach entity, and characters at target cannot be removed. Non-minable entities fail, and a mine the engine reports as successful but leaves the entity in place is an error. Corpses are the exception: a corpse (for example the `tree-XX-stump` left by Factorio 2.0 when a tree is mined) is destroyed instead of mined, since it is `mineable=false` and would otherwise be unclearable, and it yields no items; removing a tree also destroys the stump it leaves. `items` reports stacks added to actor main inventory; overflow spills nearby and is not listed. Example: `mutation { remove(input: {actor: {player: "wricardo"}, target: {entity: "stone-furnace", position: {x: 40, y: 64}}}) { tick surface force actor { player character } entity type position {x y} items {name quality count} } }`.

`harvestArea` accepts one nested `actor` and scans actor's current surface. It mines reachable trees and rock-named simple entities with normal mining. Factorio 2.0 leaves a collidable corpse stump (`tree-XX-stump`, `type=corpse`, `mineable=false`) when a tree is mined, so each mined tree's stump is destroyed in the same call and counted in `stumpsRemoved`; `stumpsLeft` counts a stump `destroy` refused and is normally 0; a leftover stump is debris rather than an obstacle (corpses collide with tiles only, so construction proceeds over one). Stumps carry no items, so nothing is lost. `items` reports inventory gains; `outOfReach` and `failed` retain counts while `errors` provides typed per-target details. Uses same 1024-tile-per-side area limit. Example: `mutation { harvestArea(input: {actor: {character: "123"}, area: {leftTopX: 0, leftTopY: 0, rightBottomX: 32, rightBottomY: 32}}) { treesMined rocksMined outOfReach failed errors {index code message} items {name quality count} } }`.

`pickupArea` accepts nested `actor` and collects reachable dropped stacks. Partial stacks remain when inventory fills; aggregate item lists report moved and remaining counts. `stacksOutOfReach` retains aggregate count while typed `errors` distinguishes `REACH` from `INVENTORY` per stack. Example: `mutation { pickupArea(input: {actor: {character: "123"}, area: {leftTopX: 0, leftTopY: 0, rightBottomX: 32, rightBottomY: 32}}) { stacksFound stacksOutOfReach errors {index code message target {position {x y}}} itemsPickedUp {name quality count} itemsRemaining {name quality count} } }`.

`mineArea` sweeps one resource type (or every item-yielding resource when `resource` is omitted) in a bounded area (≤1024 tiles per side, `count` 1–10000) as one actor in one RCON call. Tiles are mined nearest-first with `mine_entity` and the sweep stops once the actor's inventory gained `count` items of the mined products — items, not tiles, because a partial mine only shrinks a tile's `amount`. There is no reach restriction: tiles anywhere in the area are mined, and mined items never spill. `stopReason` is `COUNT_REACHED`, `INVENTORY_FULL` or `AREA_EXHAUSTED`; `gained` may overshoot by one mine's yield. Example: `mutation { mineArea(input: {actor: {character: "123"}, area: {leftTopX: 0, leftTopY: 0, rightBottomX: 32, rightBottomY: 32}, resource: "iron-ore", count: 200}) { gained stopReason tilesFound tilesTouched tilesDepleted failed errors {index code message} } }`.

`gatherItems` finds one item in tiered sources inside a bounded area on the actor's surface and moves it into the actor's inventory until `count` is reached: `CONTAINER` (chests; infinity chests excluded unless `includeInfinityContainers: true`), `MACHINE_OUTPUT` (output inventories only of furnaces, assemblers, silos and drills — never input, fuel, module or trash), `BELT` (belt, underground and splitter transport lines), `LOOSE` (ground item entities and inserter-held stacks). Tiers are searched in that order, nearest-first within a tier, and only entities of the actor's force are touched. There is no reach restriction: sources anywhere in the area are used. `sources` lists up to 50 yielding sources (`sourcesTruncated` flags more) and `errors` the first 20 failures. Not transactional: items already moved stay with the actor. Example: `mutation { gatherItems(input: {actor: {character: "123"}, area: {leftTopX: 0, leftTopY: 0, rightBottomX: 32, rightBottomY: 32}, item: "iron-plate", count: 100}) { gathered stopReason tiers {tier sources moved} sources {tier entity unitNumber moved} errors {index code message} } }`.

`deconstructArea` and `cancelDeconstructArea` place or cancel deconstruction orders for deconstructible entities in required area, applying force planner semantics. They need no actor unless `instant` is requested; ordinary mode removes nothing itself. `force` defaults to `player`; same 1024-tile-per-side limit applies. Entity variants accept up to 1000 `targets`, each using shared `EntityRefInput`. All continuing operations return counts plus typed `errors {index code message target {...}}`; clients never parse indices from text. `inspect.toBeDeconstructed` reports current mark.

When finished, wait for all crafting to finish and return the character's main inventory to the disconnected player before joining as that player. Then stop the server.

Return fails if either crafting queue is nonempty, the player is connected, the player's main inventory lacks enough empty slots, or the character has items outside its main inventory. It destroys the detached character after successful transfer. Save the unit number across CLI invocations and server restarts; losing it does not automatically return items.

For player-backed inspection, crafting, placement, fueling, transfer, and belt pickup, supply the player name instead of the character ID in the relevant input. Every entity interaction requires the acting character to be within reach: for a player actor that is the player's current character, so placement, fueling, transfer, and belt pickup are not remote-administrative. Offline-player crafting may remain pending. Reach checks are a policy layer implemented in the generated Lua, not an engine guarantee: Factorio's script API executes these operations at any distance, so the resolvers would otherwise call `can_reach_entity`/`build_distance` themselves. They are **disabled by default**, matching the administrative script API; set `FACTORIO_ENFORCE_REACH=1` to enforce actor build and interaction reach (placement, entity interaction, wires, capsule targets), and unset it to return to the permissive default. `serve` reads the flag per operation, so restart it after changing the environment. `playerInsight.reach.canReachEntity` always reports the engine's own answer, independent of this policy. `startResearch` starts an enabled, unresearched technology whose prerequisites are researched; the engine queues it behind the current research. A lab's science packs are its `input` inventory, so `transfer` feeds a lab and `inspect` reports them. Combat and full factory planning are not yet implemented.

### Destination navigation

Destination navigation requires the included Factorio mod. Back up the save, then run `just navigation-install` while the server is stopped. The recipe links the mod into `~/Library/Application Support/factorio/mods` on macOS and refuses to overwrite an existing installation. Set `FACTORIO_MOD_DIR` for a different mod directory, and use the same value when starting the server. Keep the checkout at the same path so the link remains valid; restart Factorio after code changes. Enable the mod if the installation's mod list has disabled it. Connected clients must have matching mods. Installing or removing a mod changes the save's mod set.

The mod is version 1.3.0 and hosts four tick-side remote interfaces: `factorio_ai_navigation` (nav jobs), `factorio_ai_actions` (shooting jobs), `factorio_ai_belt_flow` (throughput) and `factorio_ai_topology` (belt topology prelude, protocol 9). Topology queries (`beltRun`, `beltStretch`, `beltRuns`, `factoryGraph`, `adjacency`, `beltComponents`, `inspect { logistics }`) send only a `remote.call` with their arguments instead of re-shipping ~17 KB of Lua prelude per call, and each keeps a byte-identical inline fallback (`insights_topology.go` copies the marked regions, with a Go test that fails on drift); a save that still runs an older mod therefore works, just slower. The live server keeps serving the previously loaded mod until Factorio restarts, so a schema query can be newer than the Lua answering it.

`navigateStart` starts a persistent job for a detached character or a connected player's current character, using coordinates, a `targetEntity`, or a `targetCharacter` on its current surface. `tolerance` defaults to 0.5 tiles; `timeoutTicks` defaults to 3600 game ticks. By default it returns immediately. Set `wait: true` to wait for a terminal job state; `waitTimeoutSeconds` limits that wall-clock wait to 1–10 seconds (default 10; larger values are clamped). On wait timeout the mutation returns the latest observed job, which may still be active; it does not cancel the movement. Check `status` rather than assuming arrival. The job's separate tick-based timeout still applies. If the request is interrupted or a status read fails, the error includes the job ID when known; the job may continue. Use the job ID with the `navigation` query and `navigateCancel` mutation. When the ID itself is lost, `navigationActive(actor)` returns the job that actor's character is currently walking, or null, so a dropped response can be recovered. Use `go run . mutations --filter navigate --args --desc` and `go run . describe NavigateStartInput --desc` for the schema; `go run . describe NavigationJob --desc` lists outcomes.

`navigateStart` also accepts `targetEntity` instead of coordinates to walk next to a static entity such as a chest, assembling machine, or inserter. Supply the shared `EntityRefInput`: prototype name plus position is preferred and is resolved inside that one tile, while a unit number alone only works for prototypes the engine can look up directly, and position targets are snapped out of collision boxes. The goal is the closest point on the entity's bounding box inflated by `tolerance`, probing the midpoint and corner of each inflated side so one blocked side does not fail the job, and the job ends as `TARGET_MISSING` when the entity is removed or moves to another surface. The target is resolved once at start and the box is cached, so re-checking a still-valid target costs one surface scan per second at most. Characters are rejected here — use `targetCharacter` to chase them.

`navigateStart` also accepts `targetCharacter` instead of coordinates, which keeps the destination on that character's current position so the walker follows it. The target must be another character on the same surface, and `tolerance` must then be at least 1 because characters collide. The goal is not a single point: the mod builds a ring of eight candidate points at `tolerance` around the target, three rings wide in 0.75-tile steps, ordered nearest-ring-first and then by distance to the walker, so a point inside a chest or inserter cannot fail the chase. Each unreachable candidate (no path, or a path requiring a destroyed obstacle) advances to the next one, and only exhausting all 24 candidates reports `NO_PATH`; a pathfinder-busy reply retries the current candidate. The job re-requests a path when the target drifts more than 3 tiles from the planned goal, ignores the target entity while pathfinding, reports its unit number as `targetCharacter`, and ends as `TARGET_MISSING` when the target disappears or changes surface.

The mod requests a Factorio path and follows waypoints on game ticks, without an RCON call per step. It retries pathfinding when stalled, stops on arrival, cancellation, timeout or failure, and retains job state in saves. Only one navigation job per character is allowed: a second start fails with the active job's ID in the message, so that job can be polled or cancelled instead of guessing. For a connected player, navigation controls walking until it ends; manual movement input may compete with scripted direction, so cancel the job to stop navigation. Disconnecting or changing the controlled character cancels its job and stops scripted walking. A paused server cannot advance a job or its tick-based timeout. Paths can become blocked as the world changes, and a path does not guarantee interaction reach. Inspect job status and character position before retrying after an uncertain RCON response. The mod is not installed into or activated on an already-running server automatically.

## GraphQL

GraphQL is served over HTTP by `go run . serve [--listen HOST:PORT]` (default `127.0.0.1:8080`, endpoint `/query`) and driven with the external `gqlcli` tool (`.gqlcli.json` points it at that endpoint). The inline `query`, `mutation` and `script` commands were removed from this binary. Game operations use RCON: set `FACTORIO_RCON_PASSWORD` to the server's password; the global `--addr HOST:PORT` (before `serve`) overrides the default RCON address `127.0.0.1:27015`. Actor reachability checks are off by default; `FACTORIO_ENFORCE_REACH=1` enables them.

Every request the `serve` endpoint accepts is appended to a JSON Lines file, one record per operation. A record carries `time`, `source` (`serve`), `command` (`query` or `mutation`), `document`, optional `variables` and `operationName`, `durationMs`, and the full GraphQL `response` envelope, so operations that fail parse or schema validation are logged with their `errors` exactly as the caller saw them. `FACTORIO_GRAPHQL_LOG` sets the path; the default is `.factorio/graphql-log.jsonl`, and an empty value disables logging. Log writes never fail an operation; the first write failure prints one warning to stderr.

### Schema discovery and execution

With `serve` running, use `gqlcli`: `gqlcli queries --args --desc` and `gqlcli mutations --args --desc` list fields, `gqlcli describe TYPE_NAME [--args]` shows a type's SDL, `gqlcli query '{ ... }'` and `gqlcli mutation 'mutation { ... }'` execute operations (variables via `--input`), and `gqlcli script --file PATH --arg '{...}'` runs a JavaScript workflow (`async function run(gql, input)`) over the schema. Scripts can run mutations, so they are as powerful and as risky as `gqlcli mutation`.

### GraphQL server

Use `go run . serve` to expose the same schema over HTTP for external GraphQL clients. The endpoint accepts GraphQL over HTTP `POST`, query-only `GET`, and `OPTIONS` at `/query`; introspection is enabled. Requests are capped at 16 MiB.

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

Each request is also appended to the GraphQL operation log described under [GraphQL](#graphql), with `source: "serve"` and the HTTP status, including requests rejected during validation.

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
name, type, and force filter, plus explicit `order`, `offset`, `limit`, and
deterministic paging. `type` follows the same engine-type rule as `entities`.
`ConnectedPlayer.actor` provides the same actor relationships
from each player in `status.players`; each player also reports the character it
controls (`character`, null for a god controller). `entity(input: EntityRefInput!)` returns
`EntitySnapshot {tick, surface, force, entity}` using same unit-number or
prototype-plus-center reference as mutations. Lookup first uses engine unit
lookup, then scans requested surface for unsupported prototypes. Nested
`entity.inspection(actor)` reuses returned identity and position.

`Entity.inspection(actor: ActorInput)` inspects an entity and is
force-agnostic: the actor is optional and only adds identity and validation,
and without it the target resolves with no force restriction, so neutral,
enemy and other-force entities stay readable. It is nullable, but a failed
inspection produces a GraphQL error for that field; the CLI still prints
whatever else resolved and then exits non-zero.
`EntityInspection.recipeInfo(actor: ActorInput, force: String = "player")` and
`QueueEntry.recipeInfo(actor: ActorInput, force: String = "player")` read
recipe details for the supplied actor or, when no actor is given, for the
named force, and return null when the entity or queue entry has no recipe.
Nearby entities
carry their surface into inspection, as do mining entities returned by an
inspection. An `entity-ghost` target also reports `ghostName`,
`ghostQuality` and `ghostRecipe`; see the ghost audit note under
construction.

`inspectMany(input: {actor, surface, targets[], includeSettings,
includeDiagnostics})` inspects 1–1000 targets in one backend call and returns one
`results` row per target in input order, each carrying the same
`EntityInspection` plus optional `settings` and `diagnostics` from the same call.
A target that cannot be resolved sets that row's `error` and leaves its
`inspection` null instead of failing the page, so a whole entity list is one
call instead of the per-entity RCON fan-out that selecting `inspection` on an
`entities` or `nearby` page causes.

Each nested relationship delegates to an existing game command and can make another
RCON call, including for each entity or queue entry. These reads are not an
atomic snapshot: independently resolved fields can observe different game ticks.

Because each backend call opens its own RCON connection and gqlgen resolves list
elements concurrently, the CLI caps simultaneous RCON sessions at six
(`rconConcurrency` in `main.go`) and queues the rest; a connection's deadline
starts only after it gets a slot, so queueing never eats a call's time budget.
Without that cap, a query selecting a forced field per row (for example
`inspection` across hundreds of entities) opened hundreds of sockets and Factorio
answered `connection reset by peer`. Expect forced nested reads over large lists
to serialize, and prefer `inspectMany` or the batch reads for whole pages.

### Factory production and machines

Defaults: surface `nauvis`, force `player`, window `ONE_MINUTE`. Supported
windows: `ONE_MINUTE`, `TEN_MINUTES`, `ONE_HOUR`. Counts cover the selected
window; rates are Factorio's per-minute averages over that window, not
instantaneous rates. `production` returns `items` and `fluids`, both taken from
the force's flow statistics over the same window and sharing the
`ProductionItem` shape, so a fluid row reports produced, consumed and the two
rates just like an item; empty `itemNames` keeps every row and a non-empty list
trims both lists to those exact prototypes without changing any rate. Rows
include previously produced or consumed names even when their current window
counts are zero. `factoryOverview` reports its production and machine halves at
different ticks (`productionTick`, `machineTick`) and fills only summaries.

Machine scans cover assembling machines, furnaces, mining drills, labs and rocket
silos for the chosen force and surface. Status filters use Factorio status names
such as `no_fuel`; optional `area` uses `leftTopX`, `leftTopY`, `rightBottomX`,
and `rightBottomY`. Results sort by type, name, position, then unit number.
`total` counts filtered matches; request subsequent pages using `offset` and
`limit` (1–10000). Machine summaries are grouped by type before the status filter,
and `other` includes statuses outside the listed buckets; `noPower` counts
`no_power`, `low_power`, `not_plugged_in_electric_network` and
`recharging_after_power_outage`, while `resourceDepleted` counts
`no_minable_resources`, meaning the mining target is exhausted or gone. Because scans cover
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

`stock` distinguishes stored, in-transit, player, worker and ground quantities:
`WORKER` rows are detached characters created for courier or builder work and
carry their unit number. `category` selects one category; omit it or pass `ALL`
for all of them.
It covers the inventory types listed in its schema description, not every
inventory in the game. Ground items have no force ownership. Belt-line
deduplication fails explicitly after one million comparisons; narrow the area
rather than relying on partial totals. Each belt contributes its own tile's
lines, so in-transit totals match the sum of per-belt lane contents. Locations,
totals and items are paginated independently: `totals` breaks down per item,
quality and category, while `items` gives one row per item and quality summed
across every category.

`electricNetworks` reports network-wide production and consumption averages in
watts over `window` (default `ONE_MINUTE`), plus `maxProductionWatts` (summed
generator capacity of the force inside the area), `polesInArea`,
`lowPowerConsumers` and accumulator charge, so capacity and headroom are visible
in one call; the area restricts poles, not the network's statistics.
`loadRatio` is `consumptionWatts / maxProductionWatts` for the observed load,
not the engine's power-satisfaction ratio, and is null when no producer is in the
area. `powerConsumers` rows are instantaneous: stored energy, buffer, drain,
temperature, remaining burning fuel, the burner fuel inventory with estimated
seconds of fuel, and every occupied fluid box, and it ignores `window`.
Per-entity instantaneous production is still not exposed.
`powerConsumers` filters server-side before pagination, so `pageInfo.total`
counts matches: `status` (exact engine status name, unknown names are an error),
`connectedToProducingNetwork` (entities where the predicate does not apply never
match), `networkId` (positive engine network id), and `onlyIssues`, which keeps
entities reporting `no_power`, `low_power`, `not_plugged_in_electric_network` or
`connectedToProducingNetwork: false`, plus burners whose fuel inventory is empty.
Filters combine with AND and the default leaves behavior unchanged.
`electricNetworks` accepts `networkId` to keep one network and rejects the entity
filters and `onlyIssues` rather than ignoring them.
`worldLogistics` inspects belts, inserters and fluid boxes; `worldResources`
returns resource tiles, obstacles, water and nearby drills. Both require an area
of at most 1024 tiles per dimension. Nearby drills do not imply mining coverage.
`worldPlacement` performs the engine's manual build check — collision plus prototype rules such as offshore pumps needing water — not inventory, reach or research; a refused tile reports the colliding prototype names, whether the ground tile itself blocks the prototype, and the ground tile name, so a single check explains why without a follow-up probe.
`probePlacement` repeats that check on every tile center of an area of at most
262144 tiles in one round trip. `mode: BLOCKED_TILES` (the default) returns every
refused tile with the colliding entity names, the ground tile name, and whether
the terrain itself blocks the prototype. Corpses (including tree stumps left by
`harvestArea`/`mine`) collide with tiles only, so they never block: a stump tile
reads placeable exactly as `place` treats it, while a belt, inserter or pole
blocks and is named. `mode: GRID` returns a compact
placeability map (`rows`, one `1`/`0` string per tile row, top row first) and
`mode: SITES` returns the largest non-overlapping empty tile rectangles of at
least `siteWidth` by `siteHeight` tiles, largest first, anchored at their
top-left tile corner, capped by `maxSites`. Samples are hypothetical placements,
so grid and site checks may overlap; use a 1x1 prototype (`wooden-chest`,
`transport-belt`) to measure open ground and a machine prototype
(`electric-furnace`) to measure where that machine fits. Use it to route belts or
pipes and to find smelting-array or big-build footprints; unlike navigation it
answers per prototype, one direction per call, and ignores character reach.
Both world scans are bounded per call, so a map-wide search pages 1024-tile areas
client-side (the `script` command loops and aggregates them in one process).
`worldLogistics` and `worldResources` accept `limit`/`offset` of at most 10000
rows per call. `worldResources` accepts `kinds` (`resource`, `tree`, `cliff`,
`simple-entity`, `water`) to narrow `worldEntries` and `total` while the
partition totals stay unfiltered, and `groupPatches: true` to cluster matching
resource entities into connected patches with bounds, centroid, total amount,
`infinite` plus `yieldPercent` for infinite ore (amounts stay 0 and sort by
yield), and the drills whose mining area actually covers patch tiles, with
`coveredTiles`/`coveredAmount` measuring real coverage rather than proximity;
`clippedByArea` marks a patch touching the scan boundary, so its tiles and
amounts are partial. Patches are sorted by amount descending.
`mapArea` renders one bounded area as a compact character grid for headless clients, so an agent gets spatial context without screenshots or paged entity lists. The area is downsampled to at most `maxRows` by `maxColumns` cells (1–1024 each; a full 1024×1024 grid is about 1 MB of JSON) using one square `cellSize`; `truncated` is true whenever `cellSize` exceeds 1. Every cell covered by an entity's footprint (tile width by height, swapped for east and west directions, ghosts sized by the prototype they will become) shows that entity, so a 3×3 machine fills nine cells and the highest-priority entity wins a shared cell — character, machine, inserter, belt, pipe or fluid entity, pole, chest, resource, tree, rock, cliff — otherwise water (`~`) or empty (`.`). Natural entities appear regardless of force; built entities are limited to the selected force. `layer` narrows the scan to `ALL`, `FACTORY` (characters and built entities; natural obstacles and resources omitted), or `RESOURCES` (resources only, water as terrain). The legend maps every character to its prototype name, type, first unit number and cell count, further prototypes collapse into one `?` row named `other`, and the alphabet never uses `.`, `?` or `~`, so a caller can follow a glyph with `entities` or `inspect`. One cell shows one entity and cells are sampled at their top-left tile for water, so the grid is a spatial overview, not a completeness claim.

`factoryBriefing` is one bounded read-only attention snapshot. It composes the existing production, machine, power-network, research, safety, construction-order, ghost and stock reads, each in its own RCON call, so every section reports its own tick (`productionTick`, `machineTick`, `powerTick`, `researchTick`, `safetyTick`, `constructionTick`, and `stockTick` when `watchItems` were given) and sections can differ by a few ticks. `attention` lists observations by severity (`CRITICAL` stops output or threatens loss, `WARNING` degrades it, `INFO` is queued work), then kind, position and name, cut to `attentionLimit` (1–1000, default 20); `attentionTotal` and `attentionCounts` keep the exact pre-cut totals per kind. Kinds are `NO_FUEL`, `NO_POWER`, `NO_AMMO`, `NO_INGREDIENTS`, `NO_RESOURCES` (`no_minable_resources`), `OUTPUT_FULL`, `NO_RECIPE`, `LOW_TEMPERATURE`, `BLOCKED` and `DISABLED` (all from engine entity statuses), `FLUID_SHORTAGE`, `ENEMY` and `DAMAGED` (safety), `NO_RESEARCH` (research enabled, nothing active or queued), `GHOST_BACKLOG`, `DECONSTRUCTION`, and `LOW_STOCK`. Enemy attention requires `area`: without one the surface-wide hostile count is named in `warnings` instead of being counted as `CRITICAL` attention. Per-entity items carry name, position and unit number when the source exposes one; aggregate kinds carry a `count` instead. `watchItems` (at most 100, each with a `minimum`, default 1) report `stockouts` against observed stock in `stockCategory` (default `STORED`; pass `IN_TRANSIT`, `GROUND`, `PLAYER`, `WORKER` or `ALL` to widen it), so player, worker, ground and belt contents cannot satisfy a factory minimum by accident, with `actorHas` from the optional `actor`'s main inventory. Ghost counts follow the briefing scope: the area when one is given, otherwise the whole surface, so the backlog is never skipped; `ghostsBuildable` counts ghosts whose placement material is in observed stock in that scope, over the first 10000. Attention rows come from that single status scan, which lists at most 10000 unhealthy rows while `attentionCounts` and `attentionTotal` always cover the whole scan, and a truncated row list is named in `warnings`. A failing section fails the whole query, so a returned briefing is never silently partial. It recommends nothing and stores nothing.

`factoryDefects(input: {surface, force, area, kinds?, limit})` is the deterministic structural and absence scan: one call composes `factoryGraph`, `beltComponents`, `powerConsumers`, `ghosts` and `logisticNetworks`, and reports `BOILER_NO_FUEL_INSERTER`, `MACHINE_NO_INPUT`, `INSERTER_DANGLING_PICKUP`/`DROP`, `INSERTER_SHARED_SOURCE`, `ORPHAN_ENTITY`, `BELT_DEAD`/`STARVE_HEAD`/`STALL_END`/`MIXED_LANE`, `GHOST_NO_ROBOPORT` and `PRODUCER_UNPLUGGED`. Rules are per entity type, so a steam engine or a drill is never reported for lacking an input arm; `area` is required (≤1024 tiles per side), sections whose kinds were filtered out are not read at all (`beltTick`/`powerTick`/`ghostTick` stay null), `limit` caps rows while `counts` covers the whole scan, and `warnings` lists truncated sections. Every section reports its own tick because the reads are separate RCON calls.

`factoryAlerts(input: {surface, force, area?, kinds?, limit})` is the single status scan behind that attention list, without the composed sections. It visits every status-bearing entity of the force, drops healthy and transient states (`working`, `normal`, `charging`, `waiting_for_train` and similar) and maps every other Factorio 2.0.77 status to an `AttentionKind`, so a boiler with `no_fuel`, an unpowered inserter, a turret with `no_ammo`, a drill with `no_minable_resources`, a reactor with `full_burnt_result_output` or a radar without power all appear; a test pins coverage of every engine status name. `kinds` keeps only kinds this scan can produce: `ENEMY`, `GHOST_BACKLOG` and `LOW_STOCK` come from other reads and are rejected instead of silently matching nothing. Rows are ordered by severity, kind, distance from the area center (or force spawn) and position, and cut to `limit`, while `scanned`, `total` and `attentionCounts` always describe the whole scan and are independent of `kinds` and `limit`; `machines` reuses `MachineSummary` and every other status-bearing type appears only as an alert row. An inserter waiting for space also reports its drop target as `destination` (`bar`, `usableSlots`), and `cause` names `DESTINATION_BAR_LIMITED` when that bar leaves no usable slot, so a bar-limited chest is distinguishable from a genuinely full one.

`nearestPlacement` answers "where is the nearest legal spot for this prototype near this origin" in one round trip. Candidates are tile-aligned footprint centers (odd sizes on half tiles, even sizes on whole tiles, width and height swapped for east and west directions) within `maxRadius` (greater than 0, at most 512) of `origin`, ordered by distance then x then y; the first that passes every check wins, so the answer is the nearest legal position, not a nearby one. Checks run cheapest first: the engine's manual build check (the rules a hand-placing player gets, so offshore pumps need water and pumpjacks need an ore field), `avoidResources`, `avoidWater`, `minDistanceFromEntities` (0–64 tiles of clear space around the footprint, counting built entities and ghosts of any force but never resources, trees, rocks, cliffs, characters, corpses or items), `requirePowerCoverage` (footprint touches an electric pole's supply area of the selected force), `requireWaterAdjacency` (a water tile within one tile of the footprint) and `requireResource` with `minResourceTiles` (1–100000, default 1) for a mining-area placement, whose winning candidate also reports `resourceTiles` and `resourceAmount`. `expanded` counts candidates evaluated. `blockers`, `terrain` and `tile` describe why the snapped origin itself is refused by the engine and are empty when the engine accepts it, so a not-found answer still explains the origin; a candidate rejected only by a caller constraint reports no blockers. Like `worldPlacement` it ignores reach, inventory and research. Water means a tile with a pumpable fluid: out-of-map tiles share the `water_tile` collision layer but are not water, here and in `mapArea`.

`entities` pages every entity on a surface with optional prototype-name and type
filters plus an optional `force` filter (rows always carry `Entity.force` and
`Entity.tick`); `type` is an engine entity type, so a prototype name such as
`iron-chest` is rejected while `container` is accepted, and `transport-belt` is a
valid type even though a prototype shares the name — use `name:` for the
prototype. Omit `area` to scan the whole surface, or pass any positive rectangle
(reads no longer cap an area at 1024 tiles, and `limit` goes to 10000), so
"where are my boilers" is one call instead of a stitched grid. Rows carry
`status`, `statusName` and `health`, so one page also reports which entities are
dry, unpowered, idle or damaged. `entityCount(input: {surface, area, force,
names, types, groupByStatus})` answers "how many X" in one call without paging:
rows are grouped by name and type, split per engine status when
`groupByStatus: true`, sorted by descending count, and unknown names or types
fail the call instead of returning an empty tally. `belts` pages transport belts and both lane contents. `belts` is
transport-only by contract: underground belts, splitters, loaders and linked
belts have different line counts and are read with `worldLogistics` or
`factoryGraph` (whose belt nodes already carry `direction` and `lanes`), or as
a linear run with `beltStretch`. Both expose
`total` with `limit`/`offset`. `removeMany` mines up to 1000 explicit,
reachable targets addressed by unit number or by name plus a position inside the
entity (neutral rocks, trees and resource tiles included) and reports per-target
failures without aborting the batch:
`results` holds one row per successful removal and `items` aggregates everything
recovered across the batch. Corpse targets (for example stumps left by earlier
harvests) are destroyed rather than mined and yield no items. `mine` targets resources, trees, rocks and simple
entities, which have no unit numbers, so a unit-number-only target is rejected
and `mine` needs the prototype name plus a position inside the entity. Neutral
rocks, trees and resource tiles are accepted, `items` reports the mined stacks,
and a mine the engine reports as successful but leaves in place is an error.
`safety` observes enemy and damaged entities, optional position pollution, and
player alerts; it does not predict attacks. Alerts require a player name.

### Topology, diagnosis and planning

`beltRun` walks the engine's real belt connections from the belt covering a
position: branches, merges, underground pairs, splitters, loaders and linked
belts. It returns a directed edge list, `sources`/`sinks`, `inserters` whose
targets are returned belts, and `boundaryEdges`
for connections that leave the returned node set. `sources`/`sinks` are
structural terminals, not stall or dead-end diagnoses, and `maxEntities`
(1–20000) bounds traversal with `truncated` set when belts were omitted.
`includeLineLinks` adds transport-line edges on request; they only widen the
walk otherwise.
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
tile. `inserters` lists every arm whose pickup or drop target is a belt in the
stretch, read from one scan over the stretch bounds padded by 2.5 tiles and
carrying each inserter's engine status, so a full stretch plus an idle or blocked
consumer is one call; each `ends` row also carries `downstream`, the entity
occupying the expected tile or the first tile toward the next aligned belt, so a
boundary names the machine, chest or belt that waits there.
`factoryGraph(area, force, surface, compact, includeLineLinks)` returns machines,
belts, chests, inserters and fluid-network entities of one force intersecting an
area in one snapshot, including inserters outside the area whose actual target is
inside it (arms are scanned 2.5 tiles beyond the area); `boundaryEdges` identify
every endpoint that leaves the set. Fluid entities include offshore pumps, pipes,
pipe-to-ground, pumps, boilers, generators, heat exchangers and storage tanks,
with directed `fluid` edges from the engine's pipe connections. Belt nodes carry
`direction` and per-line `lanes` contents, so topology and what each lane holds
come back in one call. `compact: true` also fills `compactEdges`, `compactBoundaryEdges` and
`compactInserters`, which reference endpoints by key (`surface:unitNumber`, or
`surface:type:name:x:y` for entities without a unit number) instead of repeating
full endpoint objects. Transport-line links are
internal connections that can join belts along one line instead of by adjacent
tiles; they only extend traversal unless `includeLineLinks` is set, in which case
`beltRun`, `factoryGraph`, `beltComponents` and `beltRuns` also emit `line` edges
(these never feed `sources`/`sinks`). Neither query measures throughput.

`beltComponents` groups every belt in an area into weakly connected components
in one snapshot and classifies each one, replacing a seed-per-component audit:
`belts` counts the component's belts, `nodes`/`edges` follow the same engine
connections as `factoryGraph`, `sources`/`sinks` are structural terminals
inside the area, `inserters` lists every arm whose pickup or drop target is one
of the belts (once per component, so an arm linking two components appears in
both), and `items`/`itemCount` aggregate the in-transit stock on its lanes. An
arm target is resolved by the tile its arm position lands in, not by the raw
point: a belt drop reads 30.699 for the tile at 30.5, so a point lookup would
leave a working run looking `dead`. The
inserter scan is padded past the area edge and the machine-drop scan by the
largest machine radius, so a drill centred outside the area still counts as a
feeder.
`dead` is true when nothing feeds or drains the component: no inserter target
and no machine drop target touches it. `stallEnds` are sinks no inserter picks
from, so items stop there, and `starveHeads` are sources nothing drops onto.
`mixed` is true when at least one belt carries more than one prototype or
quality on one side (same rule as `BeltStretchTotal.mixed`), and
`sharedPickupSources` counts belt tiles that two or more inserters pick from,
which is the contention signal a whole-base arm audit used to need another
`factoryGraph` scan for.
Components are sorted dead first, then by belt count, and paged with
`limit`/`offset`; `deadOnly` keeps just the dead ones. Because this is a
read-only single-snapshot analysis, its area may be up to 1024 tiles per side
instead of the 1024-tile paging cap, so a whole base is one call. `maxEntities`
(1–20000) bounds each traversal and sets that component's `truncated`;
`beltTotal` counts belts found before grouping.

Lane reads in `worldLogistics`, `factoryGraph` and `beltStretch` carry the
engine transport-line name and its side (`left_line`/`right_line`, splitter
lines, an underground belt's surface and `secondary_*` tunnel lines).
`LogisticsEndpoint` also exposes `direction`, so a directed walk does not have to
reconstruct it from edges. `belts` only returns transport belts, where `line1`
is the left line and `line2` the right line.

`adjacency` answers "are these actually connected": it lists every entity in an
area (at most 1024×1024 tiles) whose axis-aligned bounding box lies within
`tolerance` (0–4 tiles) of another, plus the engine connection kind when one
exists. `connection` is empty for pairs that merely touch or sit near each other
— the usual misaligned pipe, boiler, offshore pump or inserter case — and
non-empty values are `belt`, `underground`, `linked`, `loader`, `pickup`, `drop`
and `fluid`. `gap` is the bounding-box distance in tiles (0 when touching).
`limit` (1–10000) bounds nodes and pairs with `truncated` set when either was
omitted. `kinds` keeps only those engine entity types as nodes — a fluid or belt
wiring audit passes `["pipe","boiler","steam-engine","transport-belt"]` instead
of drowning in poles, trees and inserters whose geometry overlaps — and
`onlyUnconnected` drops every pair the engine reports as connected, so `limit`
bounds only suspicious rows. Both filters apply before the pair limit and an
unknown type is an error. Live measurement over the 520×500 base: unfiltered
sweep hit the 2000-pair cap truncated, while `kinds` + `onlyUnconnected` returned
120 rows untruncated. Geometric adjacency never proves flow; use `factoryGraph`,
`beltRun` or `fluidNetwork` for engine truth.

`inspect { logistics }` resolves inserters whose engine pickup or drop target is
the inspected entity, with belt lane contents and a separate snapshot tick. It
scans all surface inserters, so long-handed and modded long-range inserters are
included. `inspect { logistics { beltRuns } }` walks the belt components reached
from those actual targets and errors if a target changed between snapshots.
`logistics.circuit` is null when an inserter has no control behavior.
`LogisticsEndpoint.inserterSettings` returns filter slots, filter mode, stack
override, engine active state and circuit settings; it is null for non-inserters.

Inserter pickup/drop vectors: `inspect { settings { inserter { pickupVector { x y }
dropVector { x y } customVectorsAllowed } } }` reads the pickup and drop points as
offsets from the inserter centre in map axes (x east, y south) for its current
direction; `setInserterSettings(input: {..., pickupVector: {x, y}, dropVector:
{x, y}})` writes them in the same frame. Verified on Factorio 2.0.77: the engine
silently ignores the write unless the prototype sets `allow_custom_vectors`,
which no vanilla or Space Age inserter does (a default east inserter reads pickup
`(1, 0)`), so the mutation fails with an `allow_custom_vectors=false` error instead
of reporting success; it also fails if the read-back differs from the request.
The engine does not limit the point to the inserter's reach (a `(9, 9)` offset is
accepted). Vectors rotate with the entity: turning an east inserter with pickup
`(1, 0.25)` south makes it `(-0.25, 1)`, so write vectors after `setDirection`.

`machines { machines { diagnostics } }` bundles one snapshot of per-machine
diagnosis: `shortfall` (demand for one next craft from input inventories and
compatible fluid boxes, quality-aware), `throughput` (theoretical crafts and
expected base products per second at the current crafting speed, excluding
productivity, downtime and shortages; `actualCraftsPerSecond` is null because a
single snapshot cannot measure it), `blocked` (`NONE`, `MISSING_RECIPE`,
`MISSING_INGREDIENTS`, `MISSING_RESEARCH`, `MISSING_FUEL`, `MISSING_POWER`,
`OUTPUT_BLOCKED`, `DISABLED`, `RESOURCE_DEPLETED` for `no_minable_resources`,
`OTHER`, plus the raw engine status name so unknown future statuses stay
visible) and `power`
(electric state, network id, engine "connected to a producing network" flag;
`satisfaction` is null because Factorio 2.0 exposes no per-machine demand
ratio). `diagnostics` is non-null, so a machine whose entity vanished between
snapshot and nested read fails the whole query rather than returning partial data.
`mining` gives the current target prototype, its remaining amount (null for
infinite resources), the theoretical units per second and the engine productivity
bonus that rate excludes; `burner` gives fuel stacks, the energy left in the
burning item, the burning item name, and seconds of fuel remaining.

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

`itemBalance(input: {surface, force, item, area?, window})` answers "is this item
short, and who starves" in one call by composing the `itemFlow` scan, the item
and fluid flow statistics for the window, and the same stock scan as `stock`.
`producedPerMinute`/`consumedPerMinute` are observed force-wide flow, `net` is
observed production minus observed consumption, and `theoreticalSupply`/
`theoreticalDemand` are summed per-endpoint potentials rather than measurements,
so the two pairs can disagree in sign and size. `stock` groups every counted
factory, ground, belt and player row for the item and ignores fuel, modules and
equipment exactly like `stock`. `producers`/`consumers` are the `itemFlow` rows
without its pagination, capped at 1000 each with `truncated` set when either list
was cut, and `starvedConsumers` holds consumers whose engine status reports
missing ingredients or fuel plus fuel consumers holding none of the item. Burner
fuel rates are upper bounds and idle furnaces contribute their previous-recipe
potential, so demand can exceed observed consumption.

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
block), `lamp` (color, always-on, circuit color mode), `container` (raw `bar`
index plus the derived `allowedSlots` for the requested `inventory`),
`infinity` (filter list and `removeUnfilteredItems` for infinity containers and
cargo wagons), `logistic` (request sections and slots, with a null `max` meaning infinite),
`trainStop` (name, nullable train limit where null is unlimited, priority, color),
`rocketSilo` (part count,
engine status, automatic launch), and `circuit` (raw control behavior flags and
conditions). Sections are null for prototypes that do not support them, and
reading never creates a control behavior.

`entitySettings(input: {surface, force, area, names?, types?, inventory,
limit})` reads that same `SettingsResult` for every matching entity in one Lua
chunk and returns the rows as `entities`, replacing the
two round trips per entity that `entities { inspection { settings } }` costs;
`area` is required, entities on another force are skipped rather than failing the
query, `matched` counts what the engine found before `limit`, and rows are sorted
by unit number (unnumbered entities last by position) so repeated calls compare
positionally.

Writes are explicit, non-transactional mutations that resolve the acting
character, enforce reach only when `FACTORIO_ENFORCE_REACH=1`, and return the
post-write state in the same tick. A splitter filter routes matching items to the output chosen by
`outputPriority`: setting a filter without one defaults it to `left`, and an
explicit `none` is rejected because the filter would be inert; an omitted
comparator keeps the engine default (`=`). The writes are
`setSplitterSettings`, `setInserterSettings`, `setLampSettings`,
`setContainerBar`, `configureInfinityContainer`, `setLogisticRequests`,
`setConstantCombinator`, `setTrainStop`, `setRocketSilo`,
`launchRocket`, and `setCircuitBehavior`. Omitted fields keep their current
values; `clear`, `clearConditions`, `clearSections`, `clearTrainsLimit` and
`bar: 0` reset to the engine default (no filter, no condition, no sections, no
limit, no bar limit). `setContainerBar` takes `allowedSlots` instead of a raw bar
index: null removes the limit, 0 blocks every slot, and a value above the
inventory's slot count fails, while the readback reports both the engine's stored
`bar` index (`allowedSlots + 1`) and the derived `allowedSlots`.
`configureInfinityContainer` replaces every filter of an infinity container in
one array assignment, so a rejected entry cannot leave it half-rewritten; an
empty `filters` list clears them, item and quality prototypes
are validated first, `count` is a whole number through 4294967295, and `mode` is
`AT_LEAST`, `AT_MOST` or `EXACTLY`. Filters share the container's slots: each
AT_LEAST or EXACTLY filter reserves `ceil(count / stack size)` slots (AT_MOST
requires no stock) and an oversized filter set fails before writing, instead of
silently leaving later filters empty. That is the sandbox item-source path: a
configured container supplies the item indefinitely, and an inserter or
`transfer ... direction: TO_ACTOR` can withdraw it (character main inventories
are not addressable as a transfer target, but taking from the container works).
`setInserterSettings` replaces the
whole filter set when `filters` is given, so include every slot you want kept.
`setConstantCombinator` replaces a constant combinator's sections the same way
(at most 20; count 0–2147483647 becomes the signal's `min`), with `enabled`
toggling the output without touching the sections.

Signals are typed throughout: `SignalInput.type` is the `SignalType` enum
(`ITEM`, `FLUID`, `VIRTUAL`, `ENTITY`, `RECIPE`, `SPACE_LOCATION`,
`ASTEROID_CHUNK`, `QUALITY`), so `steam` is a fluid and `signal-A` a virtual
signal, and reads and writes share the same `SignalID` and `CircuitCondition`
shapes. Lamp `colorMode` is the engine enum (`COLOR_MAPPING`, `COMPONENTS`,
`PACKED_RGB`); colors switch off only through `useColors: false`. A train stop's
`trainsLimit` is nullable on read (null means unlimited) and
`clearTrainsLimit: true` removes the limit without touching other fields.

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
and optionally the pause flag, returning the tick, effective speed and paused
state read back in the same tick; omitting `paused` leaves the current pause
state untouched. `status` reports the tick, the global `speed` and `paused`
flag, and each connected player's name, surface, position, and controlled
character unit number (`character`, null for a god controller). This is an
administrative knob: it affects every player and machine on the server.

`teleport(input: {actor, surface, position, snapToGrid})` moves acting character
to finite coordinates through engine teleport call and returns resulting
position. `surface` is optional and defaults to the actor's current surface, so
an actor on another planet or platform is not silently sent to nauvis; a named
surface must exist. A cross-surface move is issued on the player object, because
the engine only teleports players, cars, and spidertrons across surfaces, so a
detached character is rejected. It bypasses travel; build reach still applies
afterwards.

`useCapsule(input: {actor, item, target, count})` uses capsule item from player
character main inventory at reachable target and reports created entities.
Player actor only: detached characters cannot use player-input capsules.

`shoot(input: {actor, target, count})` fires player character weapon for `count`
simulation ticks through navigation mod `factorio_ai_actions` job. Mutation
waits and reports ammo spent. Connected player controlling character required.

`repair(input: {actor, target, count})` consumes repair packs from acting
character main inventory and restores health in one scripted step. It reports
before/after health and packs used; a normal pack restores 600 HP (the
prototype's durability times its repair speed, 300 × 2), or 300 when the
prototype publishes no durability, and `count` caps the packs spent.

### Construction, deconstruction and robot logistics

`placeGhosts(input: {surface, force, entities[], tiles[]})` plans a build by
creating entity and tile ghosts at explicit map positions, so a layout needs no
blueprint string round trip. Each entity entry carries `name`, an exact center
`position`, optional `direction` (0–15, default 0), optional `quality`, and
optional `recipe`/`recipeQuality` set on the ghost's crafting machine; each tile
entry carries a tile prototype and whole-tile `x`/`y`. One call accepts up to
10000 entities plus 20000 tiles. Nothing is consumed, reach-checked or
collision-checked, and ghosts may overlap existing entities exactly like
blueprint ghosts; entries continue past per-entry failures, and a failed recipe
assignment destroys the ghost it was set on. `ghosts` lists every created ghost
with its engine prototype, inner prototype, quality, position and unit number,
`errors` holds up to 20 typed failures (`LOOKUP` for an unknown
entity/tile/quality/recipe prototype, `ENGINE` when the engine refused), and
construction robots or `buildGhosts` still have to build the plan.

`inspect` audits one planned ghost, so a build plan can be verified before any
item is spent: on an `entity-ghost` target the snapshot adds `ghostName` (the
prototype the ghost would build, read from `ghost_prototype.name` with
`ghost_name` as a fallback), `ghostQuality` (the target entity's quality) and
`ghostRecipe` (the recipe set on a crafting-machine ghost), alongside the usual
`direction` and position. All three are null for a real entity and for a ghost
whose prototype cannot be read, so a mixed list of ghosts and built entities
compares them by `ghostName ?? name`.

`buildGhosts(input: {actor?, source?, sourceInventory?, force, surface, area, mode, limit})` revives entity
ghosts in area in one tick instead of sending construction robots, but it does
charge for the build: each ghost's `items_to_place_this` cost is withdrawn from
the actor's main inventory first and then from the optional `source` entity
inventory (`sourceInventory` picks which inventory of it, `CHEST` by default),
all or nothing per ghost and at the ghost's own quality. `actor` or `source` is
required; an actorless call without a source is rejected before any game call.
A ghost the stock cannot cover is skipped, its shortfall summed into `missing`,
and reported with error code `MATERIALS`; a ghost the engine refuses (a blocked
tile, for example) is reported as `COLLISION` and its items are refunded, so no
stock is voided. `consumed` reports what was actually spent, `collidedItems`
lists ground items that revived entities displaced (moved into the actor's main
inventory as overflow when `actor` is given), `entities` lists successes and
`errors` holds up to 20 typed failures. `REVIVE` (default) raises script revive
events, `SILENT` revives without any event, sound or smoke. It revives entity
ghosts only, not tile ghosts.

`deconstructArea` scans the requested force plus neutral, so trees, rocks, cliffs
and ground items are selectable (characters and ghosts never are), and accepts
optional `names` and `types` prototype filters, a `limit` cap, and
`instant: true`, which mines reachable targets as the actor (detached characters
included). Targets the actor cannot mine are emptied
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

`upgradeArea(input: {actor?, force, surface, area, mappings[], limit})` marks
entities in an area for upgrade with up to 1000 explicit `from` → `to`
prototype mappings, each with an optional target quality, so a belt or inserter
tier change can be planned without unsafe fast replace; it writes one
`order_upgrade` per matching entity because the 2.0 script API cannot attach
mappings to an upgrade planner item. Every visited entity row reports `MARKED`,
`ALREADY_MARKED` (left untouched), `LOOKUP` (unknown target prototype or
quality) or `REFUSED` (engine refusal with its message), and only newly marked
entities count in `orders`. `cancelUpgradeArea(input: {area, limit, ...})` cancels
those orders and counts them in `canceled`.

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
empties every box. `before`, `after`, `capacity`, `boxesAffected` and `inserted`
come from the same post-write snapshot, and `scope` says what the amounts cover:
one engine segment (so other entities' fluid in that segment is included), the
entity's own boxes, or a mix when an entity has boxes of both kinds.

`fluidSegments(input: {surface, force, area, maxBoxes})` reads every fluidbox in
an area of at most 1024 tiles per side and groups it by engine fluid segment in
one call, so a contaminated segment, a full refinery output, a filter mismatch or
a starved input shows up without one inspection per entity. Segments carry their
id, the largest member capacity, the highest member temperature, amounts per
fluid, and each member box (entity, unit number, position, box index, engine
production type, filter); a box outside any segment (fluid wagon, fluid turret
buffer, circuit-connected storage) forms its own single-box segment. `maxBoxes`
(1–20000, default 500) bounds visited boxes and `truncated` reports the cut.

`fluidStock(input: {surface, force, area?, fluid?})` aggregates fluid contents of
pipes, tanks, fluid-using machines and fluid wagons, with independently paginated
locations and totals. Each location reports its fluidbox index, engine production
type, filter, segment id, amount, temperature (when the engine exposes it) and
capacity, so a row can be fed back to `setFluid`. Totals are grouped by fluid and
category (currently only `stored`).

### Trains and placement directions

`trains(input: {surface, force, area?, station?})` lists rolling stock with
engine state (`RUNNING`, `BLOCKED`, `IDLE`, `OTHER` plus the raw code), manual
mode, speed, schedule records, cargo and fluids. Each train carries its Lua
`trainId` (an engine id, not a unit number) and its `carriages` with their
unit numbers, so a loco need not be found to address the rest.
`setTrainSchedule` replaces a train's schedule (or `clear: true`), and
`trainControl` sets manual mode and can send the train to a named schedule
station. Both accept either `target` (a locomotive or carriage) or `trainId` and
return the post-write train snapshot; reach is enforced only for an entity target
and only when `FACTORIO_ENFORCE_REACH=1`. Schedule reads expose the typed
`waitConditions` list plus `waitCondition` (the first common condition) and
`waitConditionRawJSON` (the complete unmodified engine list for unsupported or
modded fields from the same snapshot). Schedule writes accept typed wait
conditions only; no JSON string input is parsed.

`trainStops(surface, force)` lists every train stop of one force as a single-tick
snapshot sorted by unit number: `backerName` (the name trains match),
`trainsLimit` (null means unlimited), `trainsCount` (trains the engine
associates, which can double-count a braking train), `assignedTrains` (scheduled
stops) and pathfinder `priority`. It is read-only and needs no actor or reach.


Placement inputs (`place`, `placeMany`, `placeBlueprint`,
`createBlueprintFromEntities`) accept one canonical engine `direction: Int`
from 0 through 15, defaulting to 0. This represents 4-way, 8-way, and 16-way
prototypes without contradictory aliases. Cardinal-name conversion belongs in
clients. `setDirection(input: {actor, target, direction})` writes existing
entity direction and always verifies the readback: when the direct write is
ignored (Factorio 2.0.77 rails; transport belts accept it) it walks the shorter way round in
quarter-turn `rotate` steps, up to three, re-checking each one, fails with an
actionable message when no scripted path reaches the target, and otherwise
fails. The result carries `tick`, `before`, `requested`, the final `direction`
and `turns` (0 when the write itself worked), so a change can be told apart
from an ack that happened in no tick. For an inserter the direction is its
pickup side (0=north, 4=east, 8=south, 12=west) and the drop is the opposite
tile.

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
{target, color, connector})` returns signals visible on an entity's
red or green network plus connection counts; `connector` defaults to
`circuit_<color>` and on a combinator must be its output connector to read the
network the combinator drives.
`circuitNetworks(input: {surface, force, area, limit, memberLimit})` lists every
circuit network reachable from the entities in an area, grouped by engine
`networkId` and wire color, with the whole network's signals and its member
connectors (power/copper connectors are ignored), in one Lua chunk.
`safety` reports `evolutionFactor` with its time/pollution/kill
components, `totalPollution`, per-type `killCounts`, and a `basePosition` (the
area center, or the force spawn when no area is given) that ranks enemies by
distance and gives each enemy and damaged entity a `distance` from it.

### Item writes, quality and inspection metadata

`dropItem(input: {actor, surface, item, quality, count, position, target})` spills
items as a ground stack (`GROUND`) or inserts them into a transport belt lane
(`BELT`), removing them from actor inventory first and treating a spill of nothing
as a hard failure, so a failed drop leaves the inventory untouched. A ground drop
is created with no force and belts disabled, so it is never marked for
deconstruction (older builds did mark spilled stacks this way); a `BELT` drop
needs a transport belt or linked belt on the tile. The result reports `requested`,
`moved`, `remaining` (what stayed in the inventory), `groundPositions` for stacks
actually created, and `line` for a belt drop.

Quality is no longer hardcoded to `normal`: `place`, `placeMany`, `transfer`,
`fuel`, `beltTake`, `equip`, `placeTile` and `dropItem` accept quality prototype
name. Placement inputs accept `fastReplace` to replace a fast-replaceable entity
instead of failing on collision; a plain placement on an occupied tile reports
`COLLISION` with the occupant's name instead of stacking a second entity on the
tile. `placeMany` returns entity unit number, name,
type, and position for each success. `transfer.inventory` is typed
`InventoryKind`, including roboport, wagon, car, and spidertron inventories;
`transferDirection` in result uses same closed enum accepted by input.

`nearby` accepts explicit `order` (`DISTANCE` or `UNIT_NUMBER`), zero-based
`offset`, and `limit` (1–10000). `pageInfo` always reports `total`, `limit`,
`offset`, and `hasMore`; ties use unit number, name, position, then type.
`nearby` also accepts `unitNumber`, which anchors the scan on that entity's
current position on the requested surface. The entity must exist there: a
missing or foreign-surface anchor is an error, and `unitNumber` wins over
`position`, so entity-anchored and point scans are both one RCON call.
`EntityInspection` adds `unitNumber`, `nameTag`, `tagsJSON`,
`combinatorParameters` and `parametersRawJSON`, plus `undergroundNeighbour`,
`beltNeighbours` and `fluidboxNeighbours`. Arithmetic and decider combinators
use typed parameter objects; unsupported parameter-bearing prototypes retain
unmodified engine data only in `parametersRawJSON`.
`setCombinatorParameters(input: {actor, target, parameters})` replaces either
arithmetic or decider parameters. Exactly one parameter family is required;
signal-vs-constant operands are mutually exclusive, arithmetic operations are
limited to Factorio's `* / + - % ^ << >> AND OR XOR`, and decider condition and
output lists contain 1–1000 entries. Invalid input fails before RCON.
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
pre-change contents plus anything engine releases. The target must be an
assembling machine or a furnace; every other entity is rejected. Assembling
machines (and chemical plants, oil refineries and other prototypes whose engine
type is `assembling-machine`) take the recipe through the engine directly.
Furnaces cannot: `LuaEntity::set_recipe` is assembling-machine only in Factorio
2.0, and a furnace chooses its recipe from the item in its input, so the furnace
path moves one unit of the recipe's item ingredient (and `recipeQuality`) from
the actor's main inventory into the furnace input and leaves it there to be
smelted. That ingredient is reported as `item` and `count`; the call fails when
the actor inventory has none, the recipe has no item ingredient, or the furnace
selects a different recipe. Example: `setRecipe(input: {actor: {player:
"wricardo"}, target: {entity: "stone-furnace", unitNumber: "9261"}, recipe:
"steel-plate"})` needs one iron plate in the character inventory and starts
steel smelting.
`clearRecipe(input: {actor, target})` drains machine into actor and clears recipe;
it accepts assembling machines only, since the engine clears no furnace recipe.

## Factorioform (desired-state plans)

Factorioform tracks which manifest address controls which live entity. That
mapping ("state") lives in the save's script storage
(`storage.factorio_ai_player_factorioform`), one entry per manifest name, so it
travels with the world whose unit numbers it records. It is per save: blueprint
export does not copy it, and a state read from another save is meaningless.

- `factorioformState(name:)` returns that manifest's surface, force, update tick
  and every `{address, unitNumber, entity, position, direction, quality}`; this
  is the exportable equivalent of a Terraform state file. Without `name` it
  returns one summary per manifest (`resourceCount`, empty `resources`).
- `factorioformImport(input: {name, surface, force, address, target, overwrite})`
  adopts an existing entity under an address without changing the world. It
  refuses an address already mapped to a different unit unless `overwrite: true`,
  and always refuses a unit already mapped to another address in the manifest.
  Limits: 200 manifests, 20000 resources per manifest.
- `factorioformForget(input: {name, addresses})` drops addresses (empty list =
  whole manifest) and never touches entities.

Example: `mutation { factorioformImport(input: {name: "demo", address:
"chest.one", target: {entity: "wooden-chest", position: {x: 40.5, y: 40.5}}})
{ resource { address unitNumber } } }`.

Inserter manifests accept `inserter { pickup_offset = { x, y }  drop_offset = { x, y } }`
(GraphQL `pickupVector`/`dropVector`, offsets from the entity centre in map axes).
Validation rejects them on prototypes without `allow_custom_vectors`; a direction
change re-emits both offsets after `setDirection` because rotation rotates them.

### Manifest evaluation context

Manifests are HCL native syntax (`*.ftf`). HCL supplies only expression syntax;
everything below is defined by Factorioform (`EvalFactorioform`, in
`factorioform_eval.go`) and is fully evaluated at plan time, so the same inputs
always give byte-identical output.

- `variable "x" { type, default, description, validation { condition, error_message } }`:
  `type` uses HCL type constraints (`object({x = number})`, `list(string)`, ...).
  Values come from the caller (GraphQL variables, CLI `--var k=v`,
  `--var-file x.ftfvars.hcl`) else `default`; no value and no default is an
  error, as are undeclared names and type mismatches. A false `validation`
  condition fails with its `error_message`.
- `locals { name = expr }` may use `var.*`, other locals and static attributes
  of resources. Locals and resources are evaluated in dependency order; a
  reference cycle is an error naming both ends.
- `count` (whole number >= 0, `count.index`) or `for_each` (map, object or set
  of strings, `each.key`/`each.value`) on `resource` and `wire` blocks, never
  both. Addresses are `kind.name[i]` / `kind.name["key"]`; `for_each` instances
  are ordered by key. Each block is limited to 100000 instances.
- `dynamic "<block>" { for_each, content }` for repeated nested config blocks
  (for example `section`, `slot`).
- `direction.north|east|south|west` (0/4/8/12) plus the 16-direction names such
  as `direction.northeast` (2).
- Resource references `resource.<kind>.<name>[key]` expose only the static
  attributes `entity`, `position`, `direction`, `quality` and `address` (belts:
  `entity`, `address` and `path` tiles with `x`, `y`, `position`, `direction`,
  `address`). Wire `from`/`to` take any expression that yields a resource
  reference or belt tile (see Modules) and must name a declared instance.
- Functions: `range length concat flatten merge keys values lookup min max
  floor ceil abs format join coalesce contains distinct reverse slice element
  zipmap setproduct tostring tonumber tobool tolist toset tomap`, plus
  `offset(pos, dx, dy)` and `grid(origin, cols, rows, dx, dy)` (row-major list
  of positions).

Deliberately absent: providers, data sources, `terraform {}` blocks, remote
module sources, live data or unit numbers in references, and every file,
environment, time, uuid or random function (`file`, `env`, `timestamp`, `uuid`,
...), because plans must be deterministic.

### Modules

A module is a directory of `*.ftf` files, instantiated like a Terraform local
module:

```hcl
module "iron" {
  source = "./modules/mining_line"   # literal "./dir" or "../dir", relative to this file
  origin = { x = -30, y = -10 }      # every other attribute is a child variable
  lines  = var.lines
}
resource "inserter" "tap" {
  position = offset(module.iron.furnaces[0].position, 2, 0)   # module.<name>.<output>
  ...
}
```

- The module's `variable` blocks are its inputs (typed, defaults and
  `validation` apply; a missing required input is an error at the module
  block). `output "name" { value = expr }` hands values back as
  `module.<name>.<output>`; `count` modules give a tuple
  (`module.row[0].out`), `for_each` modules an object (`module.col["a"].out`).
  `count`/`for_each` follow the resource rules. `--var`, `--var-file` and GraphQL
  `variables` set the root module's variables only.
- Scope is isolated: a module sees only its own variables, locals, resources
  and wires, and the `factorioform` header belongs to the root module (a module
  inherits manifest name, surface and force).
- Resource addresses inside a module carry the instance path:
  `module.iron.furnace.smelter[0]`, `module.ore["copper"].belt.line`,
  `module.a.module.b.chest.c`. Root-level addresses are unchanged. Addresses
  are at most 256 characters; modules nest at most 8 levels; the whole tree is
  limited to 100000 resource and module instances.
- Moving existing resources into a module changes their address, so the plan
  shows DELETE for the old address and CREATE for the new one. To keep the live
  entities, `factorioformForget` the old addresses first: the next plan then
  shows ADOPT for the unmanaged entities, and `factorioformRefresh` records
  them under the module addresses.
- Resource references also carry `address`, the absolute instance address (belt
  path tiles: `<belt>.path[i]`). A wire endpoint is any expression that yields
  such a value, so a resource can cross a module boundary: expose it with
  `output "feed" { value = resource.inserter.feed }` and wire
  `from = module.iron.feed`, or pass it into a module as an input
  (`anchor = resource.chest.pole`, `variable "anchor" { type = any }`) and wire
  `to = var.anchor`. A module cannot reach another module's internals.
- `source` must be a relative directory. Through GraphQL the files are supplied
  by name, so give module files directory-qualified names
  (`modules/mining_line/main.ftf`); a module is every supplied file whose
  directory the resolved source names, the root module is the files in
  `rootDir` (default: names without a directory), and a file nothing refers to
  is an error.

### Plans (`factorioformPlan`, `factorioform plan`)

`factorioformPlan(input: {files: [{name, content}], rootDir, variables, actor})` is
read-only: it evaluates the manifest files (merged by directory), validates entities
against engine prototypes, reads the live area the manifest covers plus the
entities recorded in state, diffs desired against state against live and
returns `{formatVersion, manifest, planId, tick, surface, force, status,
summary, diffs, steps, materials}`. A manifest error comes back as one GraphQL
error listing every `file:line:col: summary: detail` (also in
`extensions.diagnostics`). `diffs` actions: NOOP, CREATE, UPDATE, REPLACE,
DELETE, DRIFT (in state, gone live), ADOPT (unmanaged live entity matches),
CONFLICT (footprint blocked), PENDING (ghost or marked for deconstruction);
wires are their own rows. `status` is CONVERGED, CHANGES, DRIFT_DETECTED or
CONFLICT (steps are still emitted for the non-conflicting rows).

Each step is a complete mutation document plus `variables` (`{input: ...}`) that
gqlcli runs as written, in order: disconnect wires, deletes, replace removals,
creates (`placeMany` per item+quality, `placeBelts` per straight same-direction
run with DIRECT routing, `place` with `undergroundType` for undergrounds),
per-entity config (`setRecipe`, `transfer` MODULES `TO_ACTOR` then `TO_ENTITY`,
`setInserterSettings`, `setSplitterSettings`, `setContainerBar`,
`setLogisticRequests`, `setCircuitBehavior`, `setConstantCombinator`,
`setCombinatorParameters`, `setDirection`), then connect wires. Entities that
do not exist yet are addressed by prototype name plus center position, others
by unit number. With `actor` the plan injects it into every `variables.input`;
without it each step sets `requiresActor: true` and the caller adds
`input.actor`. `materials` lists the items the actor must hold (placements and
module additions); refunds from removals are not subtracted.

`planId` is a sha256 over the normalized desired state, the recorded state and
the diff rows. It excludes the tick, so an unchanged world gives an unchanged id
and any change to the manifest, state or live entities gives a new one.

Workflow: `factorioformPlan` -> run each step's document+variables ->
`factorioformRefresh` -> `factorioformPlan` again must report `CONVERGED` with
no steps. ADOPT rows need no step; refresh records them in state. After a DRIFT
(or REPLACE) the first refresh only drops the dead state entry (`unmatched`) and
reports status `CHANGES`; the recreated entity then shows as ADOPT, so refresh a
second time to record it before the final plan.

`testdata/factorioform/example.ftf` is a complete manifest (variable, `count`
assembler row with modules, filtered inserter, filtering splitter, for-expression
belt path with a corner, underground pair, constant combinator wired red to the
inserter). It is the fixture of `TestFFExampleManifest` and of the real-engine
`TestIntegrationFactorioformE2E`, which runs plan -> execute -> refresh ->
CONVERGED and then provokes UPDATE, DRIFT, ADOPT, NOOP (after import), REPLACE,
CONFLICT, PENDING and DELETE (`just test-integration`).

CLI (same document as the GraphQL result, printed as JSON, read-only; needs
`FACTORIO_RCON_PASSWORD`): options before the directory —
`factorio-ai-player factorioform plan [--var k=v ...] [--var-file f]
[--actor-player NAME] [--format json|text] DIR`. `DIR` holds the factory's
root module: its `*.ftf` files, merged like Terraform — variables, locals and
resources are shared across every file in the directory (one `factorioform`
header per directory). The CLI also reads every directory the `module` blocks
point at, transitively. `--dir DIR` is the same as the positional `DIR`;
there is no per-file option, so keep independent manifests in separate directories. Example:
`factorioform plan --format text demo/` plans `demo/main.ftf`, which
instantiates `demo/modules/mining_line` for the iron, copper and coal patches.
Operations are logged with source `cli`.

## Factory graph (Memgraph)

`graph` exports every entity of one force and every engine connection between
them into a snapshot directory, then loads it into Memgraph for Cypher queries.
Exports do not change game entities, but **briefly pause the shared save by
default** for all game reads. The prior pause state is restored on success,
error or cancellation before snapshot disk writes and Memgraph loading; an
already paused game stays paused. Opt out with `--pause=false` (CLI) or
`pause:false` (export mutation input). Design, schema, coverage and a query
cookbook: [`docs/memgraph-schema.html`](docs/memgraph-schema.html).

```sh
just graph-up      # Memgraph bolt://127.0.0.1:7687 and Lab http://127.0.0.1:3000 (Docker)
just graph-sync    # pause for game reads, restore prior state, then write and load
just graph-shell   # mgconsole
```

`graph export` takes `--force` (default `player`), repeatable `--surface`
(default every surface the force owns entities on), `--leaf-max` (estimated
entities per leaf read, default 2000), `--concurrency` (1-6, default 4),
`--pause` (default true; `--pause=false` leaves pause state untouched),
`--out` (default `.factorio/graph/snap-*`, newest `--keep` 5 kept, `LATEST`
names the newest) and `--line-links`. The world is split into disjoint leaves
covering every non-empty chunk; each leaf is one RCON call. Without pausing,
each leaf is a single-tick view but the whole export can span ticks. A
census before and after the scan classifies each surface as `complete`,
`drift` (the world changed during the scan) or `incomplete` (a hole); an
incomplete export exits non-zero and `graph load` refuses it unless
`--allow-incomplete` is given. Robots and transient effects are counted but not
exported; vehicles and characters are read in one separate call. A splitter is a
`:Belt`/`:Splitter` node with `inputPriority`/`outputPriority`, and each of its
two outputs is a separate `BELT_OUT` edge carrying `fromPort` `left` or `right`
(`toPort` on the edge entering it); an edge's port is resolved from the two
bounding boxes, so it does not depend on engine neighbour-key order.

`graph load` (`--in DIR`, default the `LATEST` snapshot; `--bolt` or
`MEMGRAPH_BOLT`, `MEMGRAPH_USER`, `MEMGRAPH_PASSWORD`) applies
`memgraph/schema.cypherl`, MERGEs nodes by id and relationships by key, then
deletes everything in the snapshot's surface/force scopes that the snapshot
does not contain. `graph sync` runs both and then resumes the simulation
(clearing any pause lease left by an interrupted export), so a sync always ends
with the game running; `graph schema` applies only the schema.

The same export and load are available over GraphQL (schema `memgraph.graphqls`):
`mutation { exportMapSnapshotToMemgraph { snapshot status nodes edges prunedNodes } }`
takes `force`, `surfaces`, `leafMax`, `concurrency`, `pause`, `includeLineLinks` and
`allowIncomplete`, runs one export at a time, and does not load an `incomplete`
snapshot unless `allowIncomplete` is set. For an unpaused export, use
`mutation { exportMapSnapshotToMemgraph(input: {pause: false}) { snapshot status } }`.
`query { memgraph { query(input: {cypher:
"MATCH (m:Machine) RETURN m.unit, m.status", limit: 100}) { columns rows truncated } } }`
runs one read-only Cypher statement (optional `params` object, `limit` up to
10000, `timeoutMs` up to 60000). `sync: true` exports a fresh snapshot from the
live save and loads it before running the statement, so one query answers from
current data; it uses the export mutation's defaults (including `pause:true`),
takes seconds, and is not covered by `timeoutMs`. To avoid pausing, export with
`pause:false` separately, then query without `sync:true`. The statement must start with `MATCH`,
`OPTIONAL MATCH`, `WITH`, `UNWIND`, `RETURN`, `EXPLAIN` or `PROFILE` and may not
contain write clauses or `CALL`; that is a lexical guard plus a Bolt read
transaction, not a Memgraph permission, so use a read-only Memgraph user
(`MEMGRAPH_USER`/`MEMGRAPH_PASSWORD`) if `serve` is shared. The Bolt target is
server configuration, never a GraphQL input. `query { memgraph { schema { labels
{ name nodes properties { name count } } relationships { name edges properties
{ name count } } snapshot { id status force surfaces nodes edges tick } } } }` returns
the loaded graph's labels, relationship types, property keys and latest loaded
snapshot without writing Cypher; it is derived from data, so never-loaded
labels and properties are absent and declared indexes/constraints stay in
`memgraph/schema.cypherl`.

Supply-chain reads answer the common "what feeds this machine" questions without
writing Cypher, upstream (`supplyChain`) and downstream (`downstreamChain`) (schema `memgraph_trace.graphqls`, code `memgraph_trace.go`):

- `memgraph { supplyChain(input: {unitNumber: "370"}) { summary { ... } outline ... } }`
  traces everything upstream of a Machine, Chest or Belt (kind detected, no type
  to pass): every inserter, belt or belt stretch, and supplier machine or chest down
  to the origins. `resolution: BELT` (default) is exact; `STRETCH` collapses each
  belt run to one node and is coarse (over-approximates suppliers, omits
  recirculation loops). `maxHops` (1-16) and `maxPaths` (1-10000) bound the work;
  `truncated` reports a cut. `graphCypher` returns the same chain as Cypher path
  objects to paste into Memgraph Lab (`http://127.0.0.1:3000`, Graph Results).
- `memgraph { ingredientCoverage(input: {unitNumber: "370"}) { gaps machines { ... } } }`
  lists each recipe ingredient of a machine, and optionally every machine upstream
  of it, with the inserters that can supply it; `gaps` names ingredients with no
  feed path. Topology only, not stock or rate.
- `memgraph { downstreamChain(input: {unitNumber: "143"}) { summary { ... } outline ... } }`
  is the mirror of `supplyChain`: where a machine, chest or belt's output goes,
  through the inserters (and the belt line after the drop point) that take from it,
  down to the sinks (final consumers, chests, dead-end belts). `supplyChain` is
  upstream only and never walks downstream. Exact belt resolution only; the outline
  uses `->` for "feeds", and `summary.attention` flags `sink:belt` lines nothing
  picks from.

Planning and diagnosis reads built on the same graph (schema `memgraph_insights.graphqls`,
code `memgraph_rootcause.go`, `memgraph_nominalrate.go`, `memgraph_removalimpact.go`,
`memgraph_chaincapacity.go`, `memgraph_routebetween.go`, `memgraph_itemsources.go`):

- `rootCause(input: {unitNumber})`: why a machine is stalled, as the deepest upstream
  causes (`ORIGIN_BELT`, `NO_FEEDING_ARM`, `MACHINE_TROUBLE`); shortage states are
  symptoms, never causes.
- `nominalRateBalance(input: {item, limit})`: nominal per-minute production vs
  consumption per item from the graph's `perMinute` edges, worst net first. Topology
  only; the top-level `itemBalance` is the live-stock view.
- `removalImpact(input: {unitNumber})`: which downstream machines lose an ingredient
  if the node is removed (every supply route passes through it); cascades.
- `chainCapacity(input: {unitNumber})`: nominal output of the chain feeding a machine
  and the stages that cap it. Inserter and belt throughput are not modelled.
- `routeBetween(input: {from, to})`: the downstream routes from one node to another;
  `connected: false` is a normal result.
- `itemSources(input: {item, nearX, nearY})`: producers, chests (`CONTAINS`),
  infinity chests and carrying belt stretches for an item, nearest first.
- `areaGraph(input: {area: {leftTopX, leftTopY, rightBottomX, rightBottomY}})`: every
  entity whose position is in the box, the edges among them, their connected
  components (largest first) and the edges crossing the box edge (`boundary`, IN/OUT).
  Power edges are omitted unless `includePower`; BeltStretch nodes are not entities and
  are not listed. Sides up to 1024 tiles, `maxNodes` up to 10000, `truncated` on a cut.
- `beltStretches(input: {item, mixedOnly, endKind, nearX, nearY, limit})`: every belt
  stretch (a run ended by a splitter or side-load) with the items on each lane; all
  filters optional, none returns all. Contents are the export-time lane sample, not a
  rate. Findings: `MIXED_LANE`, and `NO_CONSUMER` (carries items, flows into no other
  stretch and no inserter picks from it).
- `beltStretchMap(input: {area, filter, mode: ITEM|STRETCH})`: the same
  stretches inside a required area as ASCII rows (north first, one character per tile, area at most 1024 per side and 65536 tiles) plus a legend; ITEM mode draws
  one letter per item, `#` mixed lane, `.` empty stretch.

Responses are shaped for an LLM to read top-down, and new Memgraph reads should
follow the same rules: a `summary` and a pre-rendered `outline` first; entities
listed once in `nodes` with paths and edges as id references instead of repeated
objects; findings (`attention`, `gaps`) already classified with a short reason;
`truncated` and `snapshot { ageSeconds }` on every result so staleness and cuts
are explicit; enums and ids rather than free text for anything to branch on. Use
`--format toon` (the gqlcli default) for the cheapest rendering of the lists.

Leaf reads call `graph_leaf`/`graph_mobile` on the navigation mod's
`factorio_ai_topology` interface. A save running an older mod gets the same Lua
inline, which is correct but takes over a second per leaf: a multiplayer server
replicates each console command to its clients, and the delay grows with the
command's size and entropy. Restart Factorio to load the mod ops.

## Security

RCON grants administrative access and is unencrypted. Do not expose it to the internet or give untrusted agents the password. `.factorio/password` has restrictive permissions, but sufficiently privileged local processes can read it; Factorio also receives it as a command-line argument. A holder can execute arbitrary administrative Lua outside this CLI. The `serve` endpoint exposes the same mutations without authentication; keep it bound to localhost. Factorio may reject the first Lua command pending confirmation, producing an empty response; the CLI's completion command may already confirm and disable achievements. Check the server log before retrying a mutation: an interrupted or failed response does not prove the action did not run.

Lua commands share response handling: reported Lua exceptions are surfaced as server errors, but an error or missing response does not guarantee a mutation was rolled back. Inventory rollback after an unexpected engine failure is best-effort; inspect the affected inventories and world before retrying.

When a client is connected, Factorio can return the RCON completion marker before the command result. The CLI waits for the result and sends another marker to collect its remaining packets. A timeout still leaves mutation outcome unknown.
- `factoryHealth`, `powerAudit`, `fuelAudit`, `inserterAudit`, `sinkAudit` (each `input: {surface, limit}`) return one ranked finding list (`MemgraphAudit`: summary, per-code counts, findings with severity, message, related nodes, impact, truncated, snapshot). `factoryHealth` sweeps the whole graph (NO_FEEDING_ARM for item ingredients, ORIGIN_BELT, MACHINE_TROUBLE, NO_CONSUMER) sorted by downstream impact. `powerAudit`: UNPOWERED, ISOLATED_POLE, SINGLE_POLE_NETWORK, NO_GENERATOR (topology only, no overload). `fuelAudit`: NO_FUEL_ARM, FUEL_NOT_ARRIVING (an empty fuel slot is a sample; fuel list is a fixed heuristic). `inserterAudit`: VOID_PICKUP (infinity chest with no filters and `removeUnfilteredItems`, exported on Chest nodes), SELF_LOOP, BELT_TO_BELT, DEAD_SOURCE, FILTER_MISMATCH. `sinkAudit`: VOID_SINK, FULL_OUTPUT_NO_CONSUMER, BUFFER_FULL. `fluidChain(input: {unitNumber, maxNodes})` walks FLUID_LINK (nodes, links, sources, sinks, DEAD_END_PIPE, ISOLATED_FLUID_ENTITY, NO_SOURCE; no fluid levels). `cellSummary(input: {limit})` groups machines into production cells (a belt line feeding 3+ machines is a main belt and never joins cells) with recipes, counts and a status histogram.
- `factoryReport(input: {surface, limit})` is the one-call factory report and needs no argument. It merges factoryHealth, powerAudit, fuelAudit, inserterAudit and sinkAudit with structural checks (STRETCH_NO_INPUT, MIXED_LANE, INSERTER_NO_SOURCE, INSERTER_NO_DESTINATION, MACHINE_NO_RECIPE, GHOSTS_PENDING), the lab feed check (LAB_PACK_UNREACHABLE, a pack made in the factory that no feeding path delivers to a lab; every lab is assumed to want every made pack) and NOMINAL_DEFICIT (nominal consumption above nominal production, excluding items with infinity sources). Findings are de-duplicated by code and node, ordered by severity then impact, and carry the `audit` that raised them. `sections` lists every audit with its finding count or the error that stopped it, so a failed audit is visible; the report only errors when every audit fails. Topology and exported status at the snapshot, not a rate.
