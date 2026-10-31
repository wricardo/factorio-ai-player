# Factorio AI Player

A Go CLI for controlling and inspecting Factorio 2.0 through admin RCON. The public gameplay interface is GraphQL; `just` manages the server. Achievements are not a project concern: operations execute server Lua and disable achievements for the save.

## Server

Requirements: Go 1.25+, [just](https://github.com/casey/just), OpenSSL and Factorio. On macOS the defaults use `/Applications/factorio.app/Contents/MacOS/factorio` and `~/Library/Application Support/factorio/saves/ai-player.zip` **directly**. Close any other Factorio instance using the same data directory first. Back up the save before automation.

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

Startup overrides: `FACTORIO_BIN` (absolute executable path), `FACTORIO_SAVE` (absolute existing save path), `FACTORIO_GAME_PORT` (default 34197), `FACTORIO_RCON_PORT` (default 27015), `FACTORIO_STATE_DIR` (default `.factorio`), and `FACTORIO_MOD_DIR` (optional Factorio mod directory). Use the same state directory to manage an instance. A non-default RCON port also requires `--addr 127.0.0.1:PORT` before CLI subcommands.

## Autonomous character

A disconnected Factorio player can queue crafting, but its queue did not advance in Factorio 2.0.77 even on an unpaused server. `characterCreate` instead creates a **detached Factorio character entity**, not a connected player. It transfers the disconnected player's main-inventory stacks into that character without creating items. Other player inventories and equipment are not transferred. The character exists in the world, can take damage, and is saved with the map.

`characterSpawn` creates a detached character with no player at the closest non-colliding character position within 10 tiles of finite coordinates on an existing surface and force. It costs no items, the character starts with empty inventories and its own reach, and the returned `character` is its unit number. Characters created this way persist in the save and are never cleaned up automatically, so destroy unused ones. Every actor input accepts that unit number, so spawned characters can be driven independently: `craft`, `mine`, `remove`, `transfer`, `equip`, placement mutations, and `navigateStart`.

`playerRestoreCharacter(input: {player})` creates and attaches a character for a connected player that controls none — a sandbox god controller, for example — and returns its unit number, surface, and position. It leaves the player's main inventory untouched, so free-crafted items stay reachable through `player:` inputs, and it rejects a player that already controls a character. `Actor.character` reports a player's character unit number. Most mutations now accept either actor directly (`rotate`, `setRecipe`, `clearRecipe`, `pickup`, `pickupArea`, placement, transfer, actions); `navigateStart` still requires an explicit character unit number, so use the reported value instead of a `nearby` scan.

`characters(input: {surface, detachedOnly, limit, offset})` lists characters across a whole surface, sorted by unit number; `detachedOnly` defaults to true, and `detachedOnly: false` includes connected players' characters, whose `player` name is reported. It has no 256-tile area limit, unlike the `entities` query, but scans the whole surface on each call. Detached characters cannot chat, shoot, or open GUIs because those are player-driven.


Save the detached character's returned unit number as a string; it is not a player name. Nearby results are capped at 100 and not sorted. Mining, removal, pickup, and character-backed placement and transfers require reach. Mining uses Factorio's mining operation and real drops/depletion but is **instantaneous**, not timed hand-mining. Movement uses the `navigateStart` pathfinding job; there is no raw walking-input mutation.

`craft` uses normal crafting queue and consumes ingredients. `requested` is the requested number of crafts, not items: a recipe that yields two products produces twice as many items; `started` may be smaller. By default, mutation returns immediately; queued crafts may still be pending. `place` uses a normal-quality item from inventory and checks collision. `inspect` reports entity state, applicable inventories (including machine modules, turret ammo, and character main/guns/ammo/armor/trash), fluid contents, and force-wide production totals for that surface (not per-machine totals). `transfer` moves normal-quality items between the character and an entity inventory (`input`, `output`, `fuel`, `chest`, `modules`, or `turret-ammo`); it reports actual `moved`. `inspect`, `transfer`, `fuel`, `remove`, `rotate`, `setRecipe`, `beltTake`, and each `removeMany` removal accept `unitNumber` in place of their coordinates, and `beltRun`/`beltThroughputStart` accept it in place of the belt x/y. Only characters resolve by a unit number alone, so pass the prototype `entity` name alongside it (`place`, `entities`, `nearby`, and `entity` all return unit numbers; `beltTake` implies `transport-belt`). `mine` and `pickup` stay coordinate-only because resources, trees, rocks, and dropped item stacks have no unit number, and placement, area, and navigation inputs address positions rather than entities. The `setRecipe` mutation configures an empty assembling machine; it still needs power and ingredients. Empty Lua tables may serialize as JSON objects rather than arrays.

`equip` moves normal-quality items between the actor's main inventory and a `guns`, `ammo`, `armor`, or `trash` slot; use `direction: "to-main"` to unequip. Armor slots accept only one item, so unequip before swapping. `inspect` reports the character's main, guns, ammo, armor, and trash inventories. Equipping does not fire weapons; shooting is not exposed.

Set `wait: true` to have the server poll until the actor's entire crafting queue is empty, including crafts queued before this request. `waitTimeoutSeconds` defaults to 60 and must be 1–3600. On timeout, mutation returns `craftingComplete: false`, `waitTimedOut: true`, latest `queueSize`, and `waitedSeconds`; it does not cancel queued work. Without wait, these status fields are null. Example: `mutation { craft(input: {character: "123", recipe: "iron-plate", count: 10, wait: true, waitTimeoutSeconds: 120}) { started queueSize craftingComplete waitTimedOut waitedSeconds } }`.

`cancelCraft` cancels up to `count` crafts starting at the one-based queue `index` and returns the remaining queue. Canceling a craft can also cancel later crafts whose ingredients depended on it. `cancelResearch` stops the force's current research and returns the resulting state; the engine removes dependent technologies from the research queue.

`placeMany` places one normal-quality item at each of up to 1000 explicit positions in a single RCON call. `placements` entries are entity-center coordinates with an optional direction (default north). Per-position failures are counted and the first 20 appear in `errors`; the batch continues after a failure. It is not transactional, so inspect the world before retrying. Player placement uses the player's current character; both player and character actors require that character to be on the surface and within build reach of each position.

`placeTile` places one tile item such as landfill, concrete, or foundation after checking the item's prototype placement rule against the current tile and rejecting tiles blocked by entities. It consumes one normal-quality item and returns the placed tile name. Tile items whose condition spans more than one tile are rejected. The acting character, or the selected player's current character, must be on the surface and within build reach.

`createBlueprint` exports a blueprint string from blueprintable entities inside an area; `includeModules`/`includeFuel` default on, tiles/trains/station names default off. `createBlueprintFromEntities` builds the same string from explicit blueprint-relative entities and tiles instead of copying the world; supports entity `name`, position, direction, quality, recipe, and mirror flag. `placeBlueprint` imports that string and places **entity ghosts** at finite coordinates, rotated by `direction`; it does not consume items and does not build real entities. Real construction needs construction bots or manual building. Blueprint strings are limited to 2,000,000 characters.

The `rotate` mutation rotates a reachable entity by 1–3 clockwise quarter turns via its `turns` input. Inserter placement direction specifies its pickup side, not its drop side. `inspect` reports inserter pickup/drop positions and transport-belt lane contents. `beltTake` moves up to `count` normal-quality items from a reachable belt into the acting character's inventory; supply either `character` or `player`, and the acting character must be within reach of the belt. If an unexpected partial insertion occurs, the remainder is spilled near the character.

`inspect { logistics { tick inserters { inserter { name position { x y } pickupPosition { x y } dropPosition { x y } heldStack { name count } } pickup { name type position { x y } lanes { index items { name count } } } drop { name type position { x y } lanes { index items { name count } } } } } }` resolves inserters whose engine pickup or drop target is the inspected entity. Endpoints can be machines, belts, containers, other entities, or null; belt endpoints include lane contents. The resolver scans inserters across the surface, including long-handed and modded long-range inserters, without a distance limit. It performs a separate RCON read, so `logistics.tick` may differ from the inspection snapshot; a removed or replaced entity can make the nested read fail.

`clearArea` immediately destroys tree entities and simple entities whose prototype names contain `rock`, within the required rectangular area; centers must lie inside the inclusive bounds. It removes entities directly, without mining drops. Each dimension must be positive and at most 256 tiles. Response reports `treesRemoved` and `rocksRemoved`. Example: `mutation { clearArea(input: {surface: "nauvis", area: {leftTopX: 0, leftTopY: 0, rightBottomX: 32, rightBottomY: 32}}) { surface treesRemoved rocksRemoved } }`.

`remove` mines one entity on the actor's current surface, using the normal mining operation so the actor recovers the entity and its contents. It requires exactly one `player` or `character`, the entity prototype name, and its center coordinates; the actor must be able to reach the entity, and characters at the target position cannot be removed. Non-minable entities fail. `items` reports stacks added to the actor's main inventory; anything it could not hold spills near it and is not listed. Example: `mutation { remove(input: {player: "wricardo", entity: "stone-furnace", x: 40, y: 64}) { surface entity type position {x y} items {name quality count} } }`.

`harvestArea` accepts exactly one `player` or `character` and scans that actor's current surface. It mines reachable trees and rock-named simple entities with the normal mining operation, putting drops in the actor's main inventory. `items` reports inventory gains; `outOfReach` counts targets beyond reach and `failed` counts mining failures, including non-minable entities or insufficient inventory capacity. Uses same 256-tile-per-side area limit. Example: `mutation { harvestArea(input: {character: "123", area: {leftTopX: 0, leftTopY: 0, rightBottomX: 32, rightBottomY: 32}}) { treesMined rocksMined outOfReach failed items {name quality count} } }`.

`pickupArea` accepts a character unit number and collects reachable dropped item stacks in its current surface. Partial stacks stay on ground when inventory capacity runs out; `itemsPickedUp` and `itemsRemaining` aggregate counts by item and quality. `stacksOutOfReach` counts stacks left outside reach. Uses same area limit. Example: `mutation { pickupArea(input: {character: "123", area: {leftTopX: 0, leftTopY: 0, rightBottomX: 32, rightBottomY: 32}}) { stacksFound stacksOutOfReach itemsPickedUp {name quality count} itemsRemaining {name quality count} } }`.

`deconstructArea` and `cancelDeconstructArea` place or cancel deconstruction orders for every deconstructible entity in a required rectangular area, applying the force's deconstruction planner semantics (trees and neutral entities included). They need no acting character or reach, remove nothing themselves, and leave ghosts for construction robots, so with no bots or roboport coverage nothing changes. `force` defaults to `player`; the same 256-tile-per-side limit applies. `deconstructEntities` and `cancelDeconstructEntities` do the same for up to 200 explicit targets, each addressed by `unitNumber` (pass `entity` too for prototypes that are not resolvable by unit number alone) or by prototype name plus center coordinates, without a force or reach check. All four return `orders` (new orders placed or canceled), `skipped`, and up to 20 per-target `errors`; area requests never report errors because the engine applies the whole box. `inspect.toBeDeconstructed` reports whether an entity is currently marked. Example: `mutation { deconstructEntities(input: {targets: [{unitNumber: "303", entity: "transport-belt"}]}) { orders skipped errors } }`.

When finished, wait for all crafting to finish and return the character's main inventory to the disconnected player before joining as that player. Then stop the server.

Return fails if either crafting queue is nonempty, the player is connected, the player's main inventory lacks enough empty slots, or the character has items outside its main inventory. It destroys the detached character after successful transfer. Save the unit number across CLI invocations and server restarts; losing it does not automatically return items.

For player-backed inspection, crafting, placement, fueling, transfer, and belt pickup, supply the player name instead of the character ID in the relevant input. Every entity interaction requires the acting character to be within reach: for a player actor that is the player's current character, so placement, fueling, transfer, and belt pickup are not remote-administrative. Offline-player crafting may remain pending. `startResearch` starts an enabled, unresearched technology whose prerequisites are researched; the engine queues it behind the current research. A lab's science packs are its `input` inventory, so `transfer` feeds a lab and `inspect` reports them. Combat and full factory planning are not yet implemented.

### Destination navigation

Destination navigation requires the included Factorio mod. Back up the save, then run `just navigation-install` while the server is stopped. The recipe links the mod into `~/Library/Application Support/factorio/mods` on macOS and refuses to overwrite an existing installation. Set `FACTORIO_MOD_DIR` for a different mod directory, and use the same value when starting the server. Keep the checkout at the same path so the link remains valid; restart Factorio after code changes. Enable the mod if the installation's mod list has disabled it. Connected clients must have matching mods. Installing or removing a mod changes the save's mod set.

`navigateStart` starts a persistent job for a detached character or a connected player's current character, using coordinates, a `targetEntity`, or a `targetCharacter` on its current surface. `tolerance` defaults to 0.5 tiles; `timeoutTicks` defaults to 3600 game ticks. By default it returns immediately. Set `wait: true` to wait for a terminal job state; `waitTimeoutSeconds` limits that wall-clock wait to 1–3600 seconds (default 60). On wait timeout the mutation returns the latest observed job, which may still be active; it does not cancel the movement. Check `status` rather than assuming arrival. The job's separate tick-based timeout still applies. If the request is interrupted or a status read fails, the error includes the job ID when known; the job may continue. Use the job ID with the `navigation` query and `navigateCancel` mutation. Use `go run . mutations --filter navigate --args --desc` and `go run . describe NavigateStartInput --desc` for the schema; `go run . describe NavigationJob --desc` lists outcomes.

`navigateStart` also accepts `targetEntity` instead of coordinates to walk next to a static entity such as a chest, assembling machine, or inserter. Pass its unit number; the goal is the closest point on the entity's bounding box inflated by `tolerance`, so the character stops beside the entity instead of trying to stand inside it, and the job ends as `TARGET_MISSING` when the entity is removed or moves to another surface. The target is resolved once at start: buildings without a unit-number lookup flag are found by scanning the character's surface, and the box is cached, so re-checking a still-valid target costs one surface scan per second at most. Characters are rejected here — use `targetCharacter` to chase them.

`navigateStart` also accepts `targetCharacter` instead of coordinates, which keeps the destination on that character's current position so the walker follows it. The target must be another character on the same surface, and `tolerance` must then be at least 1 because characters collide. The job re-requests a path when the target drifts more than 3 tiles from the planned goal, ignores the target entity while pathfinding, reports its unit number as `targetCharacter`, and ends as `TARGET_MISSING` when the target disappears or changes surface.

The mod requests a Factorio path and follows waypoints on game ticks, without an RCON call per step. It retries pathfinding when stalled, stops on arrival, cancellation, timeout or failure, and retains job state in saves. Only one navigation job per character is allowed. For a connected player, navigation controls walking until it ends; manual movement input may compete with scripted direction, so cancel the job to stop navigation. Disconnecting or changing the controlled character cancels its job and stops scripted walking. A paused server cannot advance a job or its tick-based timeout. Paths can become blocked as the world changes, and a path does not guarantee interaction reach. Inspect job status and character position before retrying after an uncertain RCON response. The mod is not installed into or activated on an already-running server automatically.

## GraphQL

GraphQL runs in-process through gqlcli; no GraphQL HTTP server is required. Game operations use RCON: set `FACTORIO_RCON_PASSWORD` to the server's password. The default address is `127.0.0.1:27015`; use `--addr HOST:PORT` **before** the GraphQL command to override it. Schema discovery runs locally and does not need a running server.

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

### In-game notifications

Every mutation prints an in-game notification after it succeeds. `BeltThroughputStartInput` has no notification field, and `save`/`shutdown` take no input. `navigateCancel` exposes the option as a field argument.

- By default the notification is a generated summary derived from the mutation result, such as `placed stone-furnace at (3, 4)` or `cleared nauvis: 2 trees and 1 rocks removed`.
- `message: String` prints this text verbatim with `game.print` instead of the generated summary, so all connected players see it in chat.

`navigateCancel` accepts the same option as a field argument: `navigateCancel(id: ID!, message: String)`.

The `announce(message: String!)` mutation prints a free-form message to all connected players; use it to narrate plans or reasoning before acting. Unlike mutation notifications, it fails when the message is blank or the RCON print fails.

Notifications are best-effort and sent in a second RCON command after the mutation succeeds; a failed notification does not change the mutation result. Detached characters have no owning player, but `game.print` reaches every connected player. Example: `mutation { place(input: {character: "123", x: 3, y: 4, item: "stone-furnace"}) { name position {x y} } }` prints `placed stone-furnace at (3, 4)`; `announce(message: "mining iron next")` prints that sentence.

### Relationships and snapshots

Nested relationships can combine several reads in one operation. `Actor.force`
identifies the actor's force; recipe reads use the specified actor's force.
`Actor.nearby` searches around the actor, with an optional radius (default 10),
name, and type. `ConnectedPlayer.actor` provides the same actor relationships
from each player in `status.players`. `entity(unitNumber: ID!, surface: String = "nauvis")` fetches one entity
record directly by unit number; it includes surface and position, so its nested
`inspection(actor)` relationship works without a coordinate lookup. Direct
lookup only works for prototypes flagged by the engine (vehicles, trains,
characters, some hubs); other prototypes are found by scanning `surface`
(default `nauvis`), so pass the correct surface for buildings on other planets.

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
rather than relying on partial totals. Both locations and totals are paginated.

`electricNetworks` reports one-minute network-wide production and consumption
averages in watts; selected area restricts poles, not the network's statistics.
`powerConsumers` reports observed energy, network and status. Current network
satisfaction and per-entity instantaneous production are not exposed.
`worldLogistics` inspects belts, inserters and fluid boxes; `worldResources`
returns resource tiles, obstacles, water and nearby drills. Both require an area
of at most 256 tiles per dimension. Nearby drills do not imply mining coverage.
`worldPlacement` performs the engine placement check, not inventory or reach.
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
`entities` pages every entity in an area with optional prototype-name and type
filters; `belts` pages transport belts and both lane contents. Both expose
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
`factoryGraph` returns machines, belts, chests, inserters and fluid-network
entities intersecting an area in one snapshot, including inserters outside the
area whose actual target is inside it; `boundaryEdges` identify every endpoint
that leaves the set. Fluid entities include offshore pumps, pipes,
pipe-to-ground, pumps, boilers, generators, heat exchangers and storage tanks,
with directed `fluid` edges from the engine's pipe connections. Neither query
measures throughput.

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

`teleport(input: {player|character, surface, x, y, snapToGrid})` moves the acting
character to finite coordinates through the engine teleport call, returning the
resulting position. It bypasses travel and is a sandbox convenience; build reach
still applies afterwards.

`useCapsule(input: {player, item, x, y, count})` uses a capsule item from the
player character's main inventory at a target within reach, for example a
cliff explosive on a cliff, and reports the entities the engine created. Player
actor only: detached characters cannot shoot or use capsules.

`shoot(input: {player, targetUnitNumber|x,y, count})` fires the player
character's weapon for `count` simulation ticks through the navigation mod's
`factorio_ai_actions` job interface; the mutation waits for the job and reports
ammo spent. It requires the updated mod (save reload) and a connected player
controlling the character, and it is not a synchronous one-shot.

`repair(input: {player|character, entity|unitNumber, count})` consumes repair
packs from the acting character's main inventory and restores health in one
scripted step (the engine's repair action is asynchronous). It reports
before/after health and packs used; base pack durability is 300 HP.

### Construction, deconstruction and robot logistics

`buildGhosts(input: {player|character|force, surface, area, mode, limit})`
revives entity ghosts in an area. Script revival bypasses items and construction
robots, so both modes are explicit sandbox shortcuts: `REVIVE` raises built
events, `SILENT` does not. `missing` reports items displaced by collisions, not
shortages, and `entities` lists what was built. Blueprint placement still only
creates ghosts.

`deconstructArea` now accepts optional `names` and `types` prototype filters, a
`limit` cap, and `instant: true`, which mines reachable targets as the actor
(detached characters included) and destroys the rest, reporting recovered
`items` from the actor inventory delta. `cancelDeconstructArea`,
`deconstructEntities` and `cancelDeconstructEntities` are unchanged.
`EntityInspection.toBeDeconstructed` reports planner state.

`logisticNetworks(input: {surface, force, area?, networkId?})` reports each
network served by roboports in scope: id, cell count, logistic and construction
robot totals/availability, contents, and requester/provider point counts.
Roboports created by scripts only join a network when built with the built
event, so use API placement rather than raw Lua when testing.

### Fluids

`setFluid(input: {player|character, entity|unitNumber, fluid, amount,
temperature, mode})` writes one reachable entity's fluidboxes. `SET` clears then
inserts (clamped to capacity), `ADD` inserts into existing content and reports
the accepted amount, and `CLEAR` empties every box. `before`, `after`,
`capacity` and `boxesAffected` come from the same post-write snapshot. This is
the recovery path for polluted or mixed fluid segments.

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

Placement inputs (`place`, `placeMany`, `placeBlueprint`,
`createBlueprintFromEntities`) accept `directionCode: Int` 0–15 alongside the
cardinal `direction` string; the code wins when given. This is required for
8-way prototypes such as rails. `setDirection(input: {…, direction})` writes an
existing entity's direction, verifies the readback, falls back to one quarter
turn when applicable, and otherwise fails with an actionable message: Factorio
2.0.77 silently ignores scripted direction writes for belts and rails, so place
those with `directionCode` instead.

### Character gear, requests and research

`Actor.equipmentGrid` reads the armor grid and `equipGrid(input: {player|
character, item, count, direction: TO_GRID|TO_MAIN})` moves items between the
main inventory and that grid one equipment at a time; armor with a grid must be
equipped first.

`Actor.logisticRequests` and `setActorLogisticRequests(input: {player|character,
sections, clear})` read and replace the character's own requester sections using
the same section/slot shape as containers. `setLogisticRequests` also accepts
rocket silos now, selecting the silo requester point.

`queueResearch(input: {force, technology})` appends a technology to the queue,
`clearResearchQueue` empties it while current research continues, and
`unlockAllTechnologies(input: {force, mode: RESEARCH|ENABLE})` is an explicit
sandbox shortcut.

### World, circuit and safety reads

`surfaces(force)` lists every surface with index, planet, total pollution and
evolution factor. `circuitNetwork(input: {entity|unitNumber, color})` returns the
signals visible on an entity's red or green network plus connection counts.
`safety` now also reports `evolutionFactor` with its time/pollution/kill
components, `totalPollution`, and per-type `killCounts`.

### Item writes, quality and inspection metadata

`dropItem(input: {player|character, item, quality, count, x, y, target})` spills
items as a ground stack (`GROUND`) or inserts them into a transport belt lane
(`BELT`), removing them from the actor inventory first and rolling back on
failure.

Quality is no longer hardcoded to `normal`: `place`, `placeMany`, `transfer`,
`fuel`, `beltTake`, `equip`, `placeTile` and `dropItem` accept a quality
prototype name, which matters because the managed save runs with the builtin
quality feature enabled. Placement inputs also accept `fastReplace` to replace a
fast-replaceable entity instead of failing on the collision. `placeMany` returns
`entities` with the unit number, name, type and position of every successful
placement. `transfer` accepts `inventoryKind` (an enum) as a typed alternative
to the inventory string, including roboport, wagon, car and spidertron
inventories.

`nearby` accepts `order` (`DISTANCE` or `UNIT_NUMBER`), `offset` and `limit`
(1–1000), reports the total match count, and returns each entity's `distance`.
`EntityInspection` adds `unitNumber`, `nameTag`, `tagsJSON`, `parametersJSON`
(control-behavior parameters such as combinator descriptions) and the
`undergroundNeighbour`, `beltNeighbours` and `fluidboxNeighbours` links.
Factorio 2.0.77 exposes no underground-neighbour API, so `undergroundNeighbour` is
always null there and says nothing about pairing; `beltNeighbours` and
`fluidboxNeighbours` report the engine neighbours when it has them.
`setNameTag` writes or clears the map label. `setEntityTags` exists but fails
loudly on normal entities: Factorio 2.0.77 stores tags only as blueprint/ghost
metadata.

### Recipes

`setRecipe` accepts a `player` actor, `recipeQuality` and a `drain` mode:
`ABORT` keeps the original empty/idle contract, `TO_ACTOR` refuses machines
holding fluids and drains input/output into the actor before the change, and
`SPILL` clears fluids and spills the drained contents at the machine. Returned
stacks come from the pre-change contents plus anything the engine releases.
`clearRecipe(input: {player|character, entity|unitNumber})` drains the machine
into the actor and clears the recipe.

## Security

RCON grants administrative access and is unencrypted. Do not expose it to the internet or give untrusted agents the password. `.factorio/password` has restrictive permissions, but sufficiently privileged local processes can read it; Factorio also receives it as a command-line argument. A holder can execute arbitrary administrative Lua outside this CLI. The `serve` endpoint exposes the same mutations without authentication; keep it bound to localhost. Factorio may reject the first Lua command pending confirmation, producing an empty response; the CLI's completion command may already confirm and disable achievements. Check the server log before retrying a mutation: an interrupted or failed response does not prove the action did not run.

Lua commands share response handling: reported Lua exceptions are surfaced as server errors, but an error or missing response does not guarantee a mutation was rolled back. Inventory rollback after an unexpected engine failure is best-effort; inspect the affected inventories and world before retrying.

When a client is connected, Factorio can return the RCON completion marker before the command result. The CLI waits for the result and sends another marker to collect its remaining packets. A timeout still leaves mutation outcome unknown.
