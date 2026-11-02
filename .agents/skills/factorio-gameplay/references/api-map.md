# API map: which operation for which job

Live schema checked 2026-11-01; counts drift, so rediscover with `gqlcli queries --args --desc` and `gqlcli mutations --args --desc` (or `go run . queries --desc`). Operations are grouped by intent, with the semantic trap that cost time in practice.

## 1. Pick by intent

| I want to... | Use | Instead of |
|---|---|---|
| Know what is broken right now | `factoryBriefing` (attention list, machines, power, research, stockouts) | calling `machines` + `safety` + `powerConsumers` separately |
| See the layout of an area as text | `mapArea` | many `entities` pages |
| Find where a footprint fits | `nearestPlacement`, `probePlacement(mode:SITES)` | trial placement |
| Learn size, stack size, fuel value, reach | `prototype(name)` | memory or test placements |
| Lay a belt/pipe/pole run | `connectionPlan` then `connectEntities` | `placeMany` with hand-computed directions |
| Put a building next to another | `placeNextTo` | footprint arithmetic |
| Wait for a game condition | `waitUntil` | repeated reads |
| Fuel / fill many machines | `fuelMany`, `transferMany` (up to 1000 targets, one RCON call) | N x `fuel`/`transfer` |
| Place N copies of one item | `placeMany` (up to 1000) | N x `place` |
| Prove a belt really moves items | `beltThroughputStart` / `beltStretchMeasure` | belt fullness |
| Prove which entity feeds which | `factoryGraph`, `inspect{logistics}`, `beltStretch` | visual guess |

## 2. Queries

### Session, actors, inventory
| Operation | Notes |
|---|---|
| `status` | Tick, `speed`, `paused`, connected players. Run first. |
| `player` / `inventory` / `queue` / `playerInsight` | Actor-rooted: pass actor fields directly: `inventory(input:{player:"name"})` or `{character:"123"}`. `recipe(input:{actor:{...},recipe:"x"})` wraps the actor. `playerInsight` bundles player+inventory+queue+recipe+reach. |
| `characters` | Whole-surface list; `detachedOnly:true` is the default and omits connected players' characters. A connected player's character is at `status.players[].actor.character`. |
| `nearby` | Distance-ordered, paginated entity list around a point. Not an exhaustive scan. |

### Entities and topology
| Operation | Notes |
|---|---|
| `entities` | Whole surface by default (omit `area`) or any positive area; `name`/`type`/`force` filter, `limit` to 10000. Rows carry `status`, `statusName`, `health`. Resource/tree/rock rows have `unitNumber:null`; address by position. |
| `entityCount` | "How many X / which are dry" in one call: `names`/`types`/optional `area`, `groupByStatus:true` splits rows per engine status. Unknown names/types error instead of returning zero. |
| `entity` | One entity by `unitNumber` or `entity`+`position`. |
| `inspect` | Full state: status, inventories, fluids, recipe, settings, `logistics` (inserters touching it), `fluidNetwork`, `beltNeighbours`, `fluidboxNeighbours`. Needs actor + target. **Target `{unitNumber}` alone fails (`prototype is not resolvable by unit number alone; pass entity too`): always send `{entity:"<prototype>", unitNumber:"<n>"}`** (verified live; same for `transfer`, `repair`, most entity-addressed mutations). |
| `worldLogistics` | One belt/inserter/etc. type per call (`type:"inserter"` matches `burner-inserter` as well; unsupported types such as `roboport` error: use `entities`). |
| `factoryGraph` | One snapshot of machines, belts, chests, inserters and their pickup/drop edges in <=1024 tiles. Best structural read. |
| `adjacency` | Geometric touching vs real engine connection. Area <=64. Finds misaligned pipes/boilers/inserters. |
| `belts` | Transport-belt lane contents only; no undergrounds/splitters/loaders. |
| `beltRun` / `beltStretch` | Structure. `beltStretch` totals per side, collapses underground pairs, reports `ends[]` with `gap:true` for a break that should continue (MISSING_BELT, BLOCKED, HEAD_ON, UNPAIRED_UNDERGROUND). |
| `mapArea` | Text map; `layer: ALL\|FACTORY\|RESOURCES`; legend characters are assigned **per call**, always read `legend`. Every cell covered by an entity footprint shows it (a 3x3 machine fills nine cells) and the highest-priority entity wins a shared cell; further prototypes collapse into one `?` row; empty `.`, water `~`. |

### Planning and world
| Operation | Notes |
|---|---|
| `worldResources` | `groupPatches:true` returns patches (`totalAmount`, bounds, centroid, `nearbyDrills`). Filter with `resourceName`. |
| `worldPlacement` / `probePlacement` / `nearestPlacement` | Engine collision legality only. `probePlacement` modes: `BLOCKED_TILES` (default), `GRID` (row strings 1/0), `SITES` (largest rectangles). Unreliable for non-standard prototypes (rocket-silo, pumpjack); empty `blockers` with `blocked>0` means a prototype requirement (resource/terrain) failed. |
| `connectionPlan` | Dry-run route. `routing: DIRECT` (straight or one bend) or `ASTAR` (bounded search, can go under obstacles with underground belt/pipe-to-ground). Reports `steps[]` with per-tile `direction`, `existing`, `undergroundType`, typed `conflicts[]`, `needed`, `undergroundNeeded`, `stopReason`. |
| `prototype` | Static data: `tileWidth/Height`, `collisionBox`, `stackSize`, `fuelValue`, `craftingSpeed`, `supplyArea`, `wireReach`, `maxUndergroundDistance`, `beltSpeed`. Older `serve` builds fail decoding inserter `inserterPickup`/`inserterDrop` ("array into Position"); the working tree normalizes the engine `Vector`. Use geometry in `spatial-and-geometry.md` until rebuilt. |
| `billOfMaterials` / `itemRecipes` / `recipe` | Recursive demand, recipes producing an item (any enabled state), single recipe + craftable count. `complete:false` = bounded expansion, not impossible. |
| `itemFlow` | Configured producers/consumers with **theoretical** rates. Unreliable for chemical plants/refineries; compute from live recipe `energy` and machine speed instead. |

### Health and measurement
| Operation | Notes |
|---|---|
| `factoryBriefing` | Attention kinds: `NO_FUEL NO_POWER NO_AMMO NO_INGREDIENTS NO_RESOURCES OUTPUT_FULL NO_RECIPE LOW_TEMPERATURE BLOCKED DISABLED FLUID_SHORTAGE NO_RESEARCH ENEMY DAMAGED GHOST_BACKLOG DECONSTRUCTION LOW_STOCK`; severities `CRITICAL/WARNING/INFO`. Sections are separate RCON reads (different ticks). Ghost section needs `area`; stockouts need `watchItems`; both are skipped and named in `warnings` otherwise. `factoryAlerts` is the same status scan without the composed sections. |
| `factoryOverview` / `production` / `machines` | Production windows are `ONE_MINUTE`, `TEN_MINUTES`, `ONE_HOUR`; `production` returns `items` and `fluids` and `itemNames` trims both. `machines` carries `diagnostics{blocked shortfall throughput mining burner power}`, where `blocked` includes `RESOURCE_DEPLETED`. |
| `stock` | Observed counts in supported containers, machines, belts, inserter hands, ground stacks and detached workers; **excludes** wagons, vehicles, robots, roboports, turrets, linked chests, trash, fluids. Not global stock. Optional `category: STORED\|IN_TRANSIT\|GROUND\|PLAYER\|WORKER` filters locations, totals, and item rows before aggregation/paging. |
| `powerConsumers` | `onlyIssues:true`, `status:"no_power"`, `connectedToProducingNetwork:false`, `networkId`, area. `connectedToProducingNetwork:false` does not prove "no network". |
| `electricNetworks` | `productionWatts`/`consumptionWatts`/`id`, not `production`/`consumption`/`networkId`; `satisfaction` may be null. Statistics span the whole network, not just the area. |
| `beltThroughput` | Observe a started measurement (retained up to 128 records). |
| `fluidStock` | Fluid locations and totals; per-pipe amount = network_fluid/network_capacity*100, so equal amounts identify one segment. |
| `safety` | Enemies, damaged friendly entities, pollution, evolution, kill counts. |

### Research, construction, trains, markers
`researchInsights` (status `ALL RESEARCHED AVAILABLE LOCKED IN_PROGRESS`; `prerequisites{name researched}`, `researchUnitCount`, `researchTriggerJSON` for trigger techs), `scienceBudget`, `ghosts`, `logisticNetworks`, `constructionOrders`, `trains`, `circuitNetwork` (per-entity network signals plus `connector`, `networkId`, `networkCircuitCount`), `circuitNetworks` (area -> networks grouped by `networkId` with members and signals), `entitySettings` (area/names/types/limit -> per-entity settings in ONE call), `surfaces`, `chartTags`, `navigation(id)`. See `circuits-and-trains.md`.

## 3. Mutations

### Actors and travel
`characterSpawn` (free detached worker, empty inventory), `characterDelete` (retire a detached, empty, playerless character; rejects player-owned or nonempty), `characterCreate` / `characterReturn` (disconnected player only), `playerRestoreCharacter` (connected god-controller player), `navigateStart` (`position` xor `targetCharacter` xor `targetEntity`; `targetEntity` is the shared `EntityRefInput`, name+position preferred, resolved on the walker's surface; `wait:true`; tolerance 0.5-10, >=1 for chase), `navigateCancel`, `navigationActive(actor)` (read), `teleport` (admin; surface defaults to the actor's, cross-surface needs a player-controlled character).

### Build, remove, repair
`place`, `placeMany` (<=1000; `undergroundType INPUT/OUTPUT` pairs across calls, an occupied tile is `COLLISION` unless `fastReplace`, which proves replaceability and returns the replaced entity + contents to the actor), `placeNextTo` (same occupancy test per nudged candidate), `connectEntities` (`from`/`to` tiles or `fromEntity`/`toEntity` + side/lane; `BELT_ADJACENCY` unless `allowBeltAdjacency`), `placeTile` (`previousTile`, `items`), `remove`, `removeMany` (<=200; per-target `results` + `items`), `mine` (instant; name + position only, no unit number), `clearArea` (no drops), `harvestArea` (drops), `repair` (600 HP per normal pack), `rotate`, `setDirection` (ignored by the engine for belts/rails: place with the right direction), `placeBlueprint` (`direction` 0/4/8/12; `entityCount`/`skipped`), `upgradeArea` / `cancelUpgradeArea`.

### Items and machines
`craft` (`wait:true` for bootstrap), `cancelCraft`, `fuel`, `fuelMany` (`topUpTo` is idempotent and reports `skipped`), `transfer` (`bar`, `toBeDeconstructed`, `reason`), `transferMany`, `equip`, `equipGrid`, `pickup`, `pickupArea`, `beltTake` (any transport-belt tier), `dropItem` (`BELT` target seeds a lane; `GROUND` refuses belt tiles, drops with no force so items are never marked for deconstruction, and reports `remaining`/`groundPositions`), `setRecipe` (`drain: ABORT|TO_ACTOR|SPILL`; assembling machines and furnaces, and a furnace is primed with one unit of the recipe's ingredient from the actor inventory), `clearRecipe` (assembling machines only).

### Settings and circuits
`setSplitterSettings`, `setInserterSettings`, `setLampSettings`, `setContainerBar` (`allowedSlots`; null removes the limit), `setLogisticRequests` (null `max` = infinite), `setActorLogisticRequests`, `setConstantCombinator`, `setTrainStop` (`clearTrainsLimit` for unlimited), `setEntityTags` (fails on normal entities in 2.0.77), `setNameTag`, `setCircuitBehavior`, `setCombinatorParameters`, `connectWire`, `disconnectWire`.

### Research, trains, fluids, rocket, server
`startResearch`, `queueResearch`, `cancelResearch`, `clearResearchQueue`, `unlockAllTechnologies` (admin), `setTrainSchedule`, `trainControl`, `setFluid` (admin), `setRocketSilo`, `launchRocket`, `setGameSpeed` (admin, affects everyone), `announce`, `save`, `shutdown` (**does not save**; never call), `beltThroughputStart`, `beltStretchMeasure`, `addChartTag`, `removeChartTag`, `screenshot`, `waitUntil`, blueprint/ghost/deconstruction family (`createBlueprint`, `createBlueprintFromEntities`, `placeBlueprint`, `placeGhosts`, `buildGhosts`, `deconstructArea`, `cancelDeconstructArea`, `deconstructEntities`, `cancelDeconstructEntities`).

## 4. Semantics worth memorizing

- **Actor identity:** `{player:"name"}` or `{character:"unit"}`, exactly one. Player actors need a character (`hasCharacter:true`, `controllerType` normal). Sandbox/god controller: `playerRestoreCharacter`. If a player controls an unusual controller type, `player:` can fail with "actor has no main inventory"; address `actor:{character:"<unit>"}` instead.
- **Reach** is **off by default** (`FACTORIO_ENFORCE_REACH=1` enables it; `serve` must be restarted to change). With reach off, build/transfer work at any distance, but `mine`/`harvestArea` success was empirically tight (~1-2 tiles) in some saves: if `outOfReach` appears, navigate first.
- **Quality:** most item mutations accept `quality`; default normal.
- **Snapshots:** every read has a `tick`. Nested forced-resolver fields are separate RCON reads and can be a few ticks later than their parent.
- **Batch ops continue past failures**, return counts and typed `errors[]`; none is transactional.
- **Notifications:** every successful action prints a generated in-game message; `message` overrides it. Read-only ops and `waitUntil` print nothing.

## 5. Validated examples for the newer operations

All documents below pass `gqlcli validate`; the read-only ones were run against the live server. Substitute your own ids and coordinates, and `describe` before consequential mutations.

```graphql
# Plan a belt run around obstacles (read-only). ASTAR may use underground belts; `needed` counts tiles still to place,
# `existing` steps cost nothing. possible:false => read conflicts[] (kind NO_PATH, COLLISION, DIRECTION_MISMATCH, FLUID_ADJACENCY, ...).
{ connectionPlan(input:{surface:"nauvis", force:"player", kind:BELT, routing:ASTAR, bend:AUTO,
    from:{x:10.5,y:-60.5}, to:{x:20.5,y:-55.5}, allowUnderground:true, allowFluidAdjacency:false,
    preferExisting:true, searchMargin:12, maxExpandedTiles:5000, maxUndergroundDistance:5}) {
  possible stopReason cost needed undergroundNeeded
  conflicts { kind position { x y } message blockers }
  steps { position { x y } direction existing undergroundType } } }

# Apply the same plan (refuses and changes nothing when conflicts exist or items are short; net-zero on refusal)
mutation { connectEntities(input:{actor:{character:"1"}, surface:"nauvis", force:"player", kind:BELT, routing:ASTAR,
    bend:AUTO, from:{x:10.5,y:-60.5}, to:{x:20.5,y:-55.5}, allowUnderground:true, allowFluidAdjacency:false, preferExisting:true}) {
  possible stopReason placed skipped failed errors { index code message } conflicts { kind message }
  entities { name unitNumber position { x y } } } }
```

`kind`: `BELT`, `PIPE`, `POWER_POLE_CHAIN` (direct only). Defaults: `transport-belt`, `pipe`, `medium-electric-pole`; set `item`/`undergroundItem` for other tiers. Endpoints snap to tile centres. Belt `steps[].direction` is the direction the tile needs; underground steps come in INPUT/OUTPUT pairs. Check actor stock for `needed` + `undergroundNeeded` halves first.

```graphql
# Footprint-aware adjacent placement: gap = whole free tiles between bounding boxes; direction must be 0/4/8/12
mutation { placeNextTo(input:{actor:{character:"1"}, surface:"nauvis", relativeTo:{entity:"assembling-machine-1", unitNumber:"1"},
    side:EAST, gap:1, item:"assembling-machine-1", direction:0}) { name unitNumber position { x y } relativeTo side gap } }

# Nearest legal footprint to an origin (engine placement only; ignores inventory and reach)
{ nearestPlacement(input:{surface:"nauvis", force:"player", prototype:"assembling-machine-1", origin:{x:10,y:-60}, direction:0,
    maxRadius:30, avoidResources:true, avoidWater:true, requirePowerCoverage:false, requireWaterAdjacency:false}) {
  found position { x y } distance blockers terrain tile } }

# Text map (legend chars change per call)
{ mapArea(input:{surface:"nauvis", force:"player", area:{leftTopX:-10,leftTopY:-20,rightBottomX:30,rightBottomY:10},
    maxColumns:40, maxRows:30, layer:ALL}) { cellSize truncated rows legend { char name type count unitNumber } } }

# Static prototype data (fails today for inserter, burner-inserter, long-handed-inserter: decode defect)
{ prototype(name:"electric-mining-drill") { name entity { type tileWidth tileHeight craftingSpeed supplyArea wireReach } item { stackSize fuelValue placeResult } } }

# Batch fueling and transfers: 1-1000 explicit targets, one RCON call, no duplicates, exact countPerTarget or that target fails
mutation { fuelMany(input:{actor:{character:"1"}, item:"coal", countPerTarget:5,
    targets:[{entity:"burner-mining-drill", unitNumber:"386"}, {entity:"stone-furnace", unitNumber:"385"}]}) {
  fueled failed moved errors { index code message } results { index entity unitNumber moved requested } } }
mutation { transferMany(input:{actor:{character:"1"}, item:"iron-plate", countPerTarget:10, inventory:CHEST, direction:TO_ENTITY,
    targets:[{entity:"wooden-chest", unitNumber:"1"}]}) { moved failed errors { index code message }
  results { index entity unitNumber moved requested available capacity } } }

# Prove a belt moves items (zero crossings = jam or empty source; sample FIRST|MIDDLE|LAST tile of the stretch)
mutation { beltStretchMeasure(input:{target:{entity:"transport-belt", unitNumber:"1"}, sample:MIDDLE, durationTicks:600, wait:true}) {
  stretch { left { itemCount } right { itemCount } truncated ends { reason gap blocker } }
  measurement { itemsPerMinute lanes { lane itemsPerMinute crossedItems { name itemsCrossed } } } } }
```

`beltThroughputStart`/`beltStretchMeasure` accept normal transport-belt tiles only (not splitters, undergrounds, loaders) and need the navigation mod's belt-flow protocol. A geometry change during the window invalidates the sample.
