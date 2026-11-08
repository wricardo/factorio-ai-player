---
name: factorio-gameplay
description: "Gameplay handbook for driving Factorio 2.0 through the factorio-ai-player GraphQL API with gqlcli: session start, observe/plan/act/verify loop, geometry and placement, bootstrap-to-rocket progression, production math, diagnosis, fluids/oil, research, construction, multi-agent operations, with dense references and ready-made scripts."
---

# Factorio gameplay via factorio-ai-player (gqlcli)

Play Factorio through the repository's GraphQL API (`http://127.0.0.1:8080/query`) with `gqlcli`, from raw-resource collection to a verified rocket launch. This file is the router: the MUST/SHOULD contract, the core loop, short playbooks, and an index of dense references. **Read the reference for your task before acting** (table below).

Authority order: live game state and the live schema (`gqlcli describe ...`) > repository docs (`README.md`, `*.graphqls`) > this skill > memory. Factorio 2.0.77 with Space Age, quality and the navigation mod; version, mods, research state and save settings change requirements. Discover before acting.

## Read this next (task -> reference)

| Task | Read |
|---|---|
| End-to-end build order from empty map to verified rocket launch (phases, gates, research sequence, parallel tracks) | [`factorio-scratch-to-rocket`](../factorio-scratch-to-rocket/SKILL.md) |
| Running gqlcli, scripts, waiting, error handling, `--strict`, CWD trap | [references/gqlcli-workflow.md](references/gqlcli-workflow.md) |
| Which query/mutation to use; semantics of each; new ops (`factoryBriefing`, `factoryAlerts`, `factoryDefects`, `inspectMany`, `entitySettings`, `itemBalance`, `connectEntities`, `buildConnection`, `buildCell`, `placeNextTo`, `nearestPlacement`, `mapArea`, `prototype`, `waitUntil`, `navigationActive`, `circuitNetworks`, `fluidSegments`, `trainStops`, `upgradeArea`, `setConstantCombinator`, `fuelMany`, `transferMany`, `transferBetween`, `beltPurge`, `setRecipeEnabled`, `playerState`, `spacePlatforms`, `blueprintBooks`) | [references/api-map.md](references/api-map.md) |
| Where to put things: coordinates, footprints, **inserter direction rule**, belts/lanes, pole reach, worked cell layouts | [references/spatial-and-geometry.md](references/spatial-and-geometry.md) |
| Starting a save, stage ladder, tech order, red-science cell, fuel discipline | [references/bootstrap-and-progression.md](references/bootstrap-and-progression.md) |
| Rates, ratios, machine counts, recipes, power sizing, rocket totals | [references/production-math.md](references/production-math.md) |
| Something is stalled/slow: triage, status table, decision order | [references/diagnostics.md](references/diagnostics.md) |
| Verified engine/API traps and known defects | [references/engine-traps.md](references/engine-traps.md) |
| Oil, pipes, contamination, fluid repair | [references/fluids-and-oil.md](references/fluids-and-oil.md) |
| Research control, trigger techs, lab feeding, the ordered **rocket research sequence** | [references/research-and-science.md](references/research-and-science.md) |
| Silo, ingredients, readiness, launch, post-launch | [references/rocket-and-endgame.md](references/rocket-and-endgame.md) |
| Blueprints, ghosts, roboports, robot mall, teardown, damage rebuild | [references/robots-and-construction.md](references/robots-and-construction.md) |
| Wires, combinators, signal probing, trains | [references/circuits-and-trains.md](references/circuits-and-trains.md) |
| Workers, tickets, narration, shared-server rules, escalation | [references/multi-agent-and-operations.md](references/multi-agent-and-operations.md) |
| Automation ladder and mall strategy | [references/automation-and-mall.md](references/automation-and-mall.md) |
| Main bus, modular blocks, cutovers | [references/layout-and-factory-evolution.md](references/layout-and-factory-evolution.md) |
| Defense, ammo, repair, threats | [references/combat-and-defense.md](references/combat-and-defense.md) |

Ready-made gqlcli scripts (tested, in `scripts/`): `briefing.js`, `survey.js`, `measure-rate.js`, `inserter-audit.js`, `place-row.js` (dry run by default), `tech-path.js`. See the script table in `gqlcli-workflow.md`.

## Contract: MUST / SHOULD / MUST NOT

Normative. MUST = required, violating it makes the action wrong. SHOULD = default, deviate only with a stated reason. MUST NOT = hard stop, no exceptions under normal play.

### MUST

1. **Read the reference for your task first** (table above), run `gqlcli op list` once, and confirm the live contract (`gqlcli queries|mutations --args`, `gqlcli describe`, `gqlcli validate`) before the first use of an operation. The saved ops are the gameplay toolbox: look them up for the action you want (read state, inspect, place a belt, build a cell, wait) and play through `--op <name>` instead of composing an equivalent request.
2. **Observe, plan, act, verify, in that order.** Never run a long mutation chain against assumed state; a live read after the write is the only proof.
3. **Exact engine names.** Items, recipes, technologies, entities, fluids, inventories come from `prototype`, `itemRecipes`, `researchInsights`, `recipe`. Never translate a display name from memory.
4. **Address entities with `entity` AND `unitNumber`** (`{entity:"lab", unitNumber:"60"}`), or `entity` + exact center `position`. `{unitNumber}` alone fails (`prototype is not resolvable by unit number alone; pass entity too`). Keep the unit numbers placement returned.
5. **Compute geometry, do not guess it.** Inserter `direction` points at its **PICKUP** side, so it drops on the opposite tile: `0` pickup north / drop +y, `4` pickup east / drop -x, `8` pickup south / drop -y, `12` pickup west / drop +x (verified live on Factorio 2.0.77 by `pickupPosition`/`dropPosition` on u1211, u1623, u1692, ticks 1507030-1513866). A **mining drill** is the other way round — `direction` is its DROP side: `0`=north (-y), `4`=east (+x), `8`=south (+y), `12`=west (-x), the same sense as belts (live ticks 2919457 and 3672835; `spatial-and-geometry.md` section 5). Belt `direction` in this save is **inverted from older notes on the y axis**: `8` moves items toward +y and `0` toward -y, while `4`=+x and `12`=-x as usual (verified by observed pack movement and `beltComponents` `stallEnds`). **An underground entrance accepts a feed only from directly behind it** (the tile opposite its facing): a perpendicular belt cannot feed a tunnel mouth, and a tunnel entirely inside a one-tile corridor has no usable exit. Odd footprints centre on `.5`, even on integers. Re-read `pickupPosition`/`dropPosition` and `beltStretch` after placing. See `spatial-and-geometry.md`.
6. **Keep changes bounded.** One machine, one belt segment, one cell, then inspect and measure. Clone only a verified block.
7. **Re-read the target immediately before every write.** Other agents share this save; unfamiliar unit numbers, structures and tags are theirs.
8. **`save` before any authorized restart.** `shutdown` does not save; `just restart` can silently switch saves.
9. **Record evidence for every claim:** tick, unit numbers, measured rate, and the read that proved it. `announce` a milestone only after that proof.
10. **Narrate your thinking continuously in short phrases.** Before each bounded change say what you are about to do and why; on a failure, stall or surprise say what went wrong and what you will check next; after verification say what worked and the next step. Target <= 60 characters per `announce`, e.g. `"iron cell: place 3 furnaces"`, `"drill 214 has no coal"`, `"checking belt east of furnace"`, `"copper cell online: 45/min"`, `"next: power the second row"`. Silence is a defect: the human cannot see your reasoning, only chat.
11. **Stop and report anomalies** (unexpected error, null where data is promised, success with unchanged state, behaviour contradicting these docs) with the exact document, variables, tick and observed-vs-expected. Never mask one with a workaround, throwaway script or retry loop.
12. **Automate before scaling, and never accept manual labour as the solution.** Mining, smelting, crafting, hauling, fuelling and loading all come from machines on a belt or logistic network; hand work is allowed only to bootstrap, unblock a stall, or make a one-off the factory cannot build yet. A repeated manual action is a missing cell or mall item — build that cell, then do the batch job once. Every burner entity MUST have an automated fuel path, and every intermediate MUST have a drained consumer before its producer is cloned.
13. **Leverage robots once they exist.** After `construction-robotics`/`logistic-robotics` and a powered roboport network, construction, repair, deficit logistics and repeated fills MUST go through ghosts + robots or requester chests rather than manual placement — but only over verified coverage with materials and power in place.
14. **Feed every machine automatically.** A production machine's inputs MUST arrive from a producing cell or mine over a belt, or from requester-chest logistics on a powered logistic network once one covers it; its output MUST go to a drained consumer. A machine whose only supply is a chest, or a hand transfer, is not automated production — it is a stall waiting for the next manual fill. Chests are bounded output buffers, bootstrap fuel stores and hand-off storage, never a machine's input supply unless the player explicitly directs that chest.

### SHOULD

1. Batch reads into one GraphQL document; replace polling with `waitUntil`; never busy-poll.
2. **Never block a shell command on `sleep` for more than 10 seconds.** Real waiting belongs in `waitUntil` (bounded call), a second query, or a `setGameSpeed` bump — not in shell timers. Long `sleep`s burn the tool deadline, look like a hang, and let the world drift while you are blind. One `sleep <= 10` is fine for a poll gap; anything longer means the wait is a game-time wait and must go through `waitUntil` or accelerated speed.
3. **Raise `setGameSpeed` instead of idling** when the next step needs game time (a furnace to smelt, a craft queue to drain, a belt to fill, a rate window to elapse). Ask for the multiplier you need (2-10 is usual; <=1000 is legal), keep it as short as the wait requires, then set it back to the previous `status.speed` and record both. Announce `"speed 4: waiting 600 ticks"` and `"speed back to 1"`. Prefer `waitUntil` at normal speed for anything under ~1 minute of wall time; scale speed only when the wait itself is the bottleneck. `speed:0` or `paused:true` freezes everyone, so restore it in the same breath.
4. Progress with numbers: measure rates as deltas (`scripts/measure-rate.js`, `production`), compute the implied ceiling before adding capacity.
5. Bound every buffer (`setContainerBar`) and give every output a drained consumer: a full chest or belt halts its producer.
6. Route with purpose-built operations — `connectionPlan`/`connectEntities`, `placeNextTo`, `nearestPlacement`, `probePlacement`, `placeMany`, `transferMany`, `fuelMany`, `waitUntil` — instead of ad-hoc tile-by-tile work.
7. Keep construction supply separate from production supply, and stage scarce materials with bounded reservations.
8. Leave corridor and expansion space; a small main bus or explicit routing plan beats a tightly packed block.
9. Do remote work from detached `characterSpawn` workers; leave the human's character where it is.
10. Fix the single upstream root cause found by `factoryBriefing` triage, then re-measure the same fields.
11. Roll robots out in that order: roboport coverage first (`probePlacement prototype:"roboport" mode:GRID`, centres < 50 apart on both axes), then power, then robot and repair-pack supply, then ghosts/requester chests; a mall or construction cell that robots must feed has to exist before the ghost wave. See `robots-and-construction.md`.

### SHOULD NOT

1. Should not add capacity before the current machine has been shown to be the limit.
2. Should not upgrade belts or buildings merely because a faster tier unlocked; speed does not repair bad ratios, geometry or routing.
3. Should not replace low-tier mall cells when the upgraded recipe still consumes their item; add downstream instead.
4. Should not build the consumer late: do not size pack production before the labs that eat it exist.
5. Should not read `stock` as global storage, `logisticNetworks.contents` as stock (it can go negative), or theoretical `itemFlow` as measured throughput.
6. Should not box-delete by entity name over a broad area; remove in layers, feeds first, verified by counts.
7. Should not treat large belt contents as flow: stock on a jammed stretch moves nothing (`beltStretchMeasure` gives crossings).
8. Should not leave a cleared area, drained buffer or dead inserter unreported; record deltas you did not cause.
9. Should not keep hand-feeding, hand-mining or hand-crafting a job that repeats: that is an unbuilt cell, not a workflow.
10. Should not wave ghosts or requester chests over an area without coverage, power and supply verified first; unmet logistics silently queues and the "build" never happens.

### MUST NOT

1. **Never let two item types — or two qualities of one item — share a single belt lane.** A mixed lane is contamination, not throughput: whichever item backs up jams the other, and the jam looks like an unrelated downstream failure. Check `beltStretch` `left.mixed`/`right.mixed` (or per-belt `lanes`) after every merge, splitter, side-load and underground pair, and fix by giving each item its own lane, a splitter, or its own belt.
2. **Never mix fluids** in one pipe segment or corridor: crude, water, heavy, light, petroleum gas, lubricant, sulfuric acid each stay in their own network. See `fluids-and-oil.md`.
3. **Never blind-retry a mutation.** Operations are not transactional; an error, timeout or lost response does not prove rollback. Read the entity/inventory first.
4. **Never stop, restart, `shutdown`, reinstall the mod, pause the game, or switch saves** during gameplay work on the shared server. `setGameSpeed` is the one exception, under SHOULD 3: raise it for a bounded wait, announce it, and restore the previous speed.
5. **Never delete, re-delete, re-flag or drain** unfamiliar entities, chart tags, storage or logistics contents.
6. **Never use admin shortcuts for normal play** (`teleport`, `setFluid`, `buildGhosts`, `unlockAllTechnologies`, `setEntityTags`) — sandbox or recovery only, with explicit authority and a record. `setGameSpeed` is carved out for bounded waits (SHOULD 3), never as a substitute for fixing throughput.
7. **Never bypass gqlcli** with `go run` or raw RCON, and never call `shutdown` to end a session.
8. **Never leave labs idle** while research is affordable: queue the next technology, and check `status.paused`/`speed` before calling anything stalled.
9. **Never accept a mutation acknowledgement, a placed ghost, or a queued craft as proof** of the outcome it claims.
10. **Never transfer `rocket-part`**; the silo creates it.
11. **Never feed a production machine's inputs from a chest, and never hand-load its inventory** unless the player explicitly directs that chest. Chests are bounded output buffers, bootstrap fuel stores and hand-off storage, not input supply: ingredients MUST arrive from a producing cell or mine by belt, or by **requester-chest logistics on a powered logistic network** (MUST 13) — a requester chest the network fills automatically is automation, a provider/wooden chest someone loads is not. A chest-fed cell hides distribution failures — the chest empties and the machine reports `item_ingredient_shortage` while the item sits in storage elsewhere — and persists only as long as someone remembers to refill it. Allowed chests: bounded output chests (`spatial-and-geometry.md` §6.2), burner fuel chests (`bar 2-3`) that are **belt- or requester-fed** (or hand-filled only during bootstrap, stage ≤ 2, per `bootstrap-and-progression.md` §4), and construction staging chests (SHOULD 7). When the player directs a chest, record the direction and replace it with a belt or automated supply as soon as a route exists.
12. **Never hand-shuttle production input or output in bulk.** `transfer`/`transferMany`, actor carrying and chest-to-chest shuttling used to keep production running are bootstrap, stall recovery and one-off staging — not a supply method. A batch a belt, robot network or producing cell could carry is a missing automated path: if the same item must move again, or a machine/chest has to be refilled, build the route or the producing cell instead, and never hand-haul ore, plates, packs or intermediates. Allowed one-offs and setup work are **not** this rule: bootstrapping before automation exists, unblocking one stalled machine, worker craft-ingredient staging (`robots-and-construction.md` §2, `multi-agent-and-operations.md` §2), loading construction/logistic robots into roboports, bulk-loading provider chests for construction (MUST 13), delivering a construction kit, and actor-to-actor hand-offs through a wooden chest (`multi-agent-and-operations.md` §2). Player-directed bulk moves are the only other exception; record the direction, the quantity, and the automated path that should replace it. A repeated `transferMany` batch feeding or draining production in one session is a missing cell, not a workflow — state why the automated path cannot exist yet rather than scaling the manual one.

## Session start (30 seconds)

```sh
cd /Users/wricardo/go/src/github.com/wricardo/factorio-ai-player   # gqlcli needs .gqlcli.json from CWD, else 404
gqlcli op list                                                     # saved named ops first, hand-written docs second
gqlcli query '{ status { tick speed paused players { name surface position character } } }'
gqlcli script --strict=false --file .agents/skills/factorio-gameplay/scripts/briefing.js --arg '{"limit":15}'
```

- `.gqlcli.json` env `local` = `http://127.0.0.1:8080/query`. Add or fix it with `gqlcli config add-env --name local --url ...` and `config set-default`; never `config init --force` over an existing file. Load the `gqlcli` skill for the full CLI.
- gqlcli is the only interface. If the endpoint is down, report it (check `just logs`, `just graphql`); never switch to `go run` or raw RCON.
- **The saved ops are how you play.** `gqlcli op list` (~48 named ops in `.gqlcli.json`) maps directly onto gameplay moves — look/read: `status`, `entities`, `entity`, `nearby`, `inspect`, `inspect-many`, `stock`, `power`, `production`, `fluids`, `research`, `briefing`, `factory-alerts`, `belt-stretch`, `dead-belts`, `fluid-segments`, `trains`, `ghosts`, `construction`, `occupancy`; act: `connection-plan`, `build-connection`, `place-belt`, `place-item`, `place-many`, `build-cell`, `set-recipe`, `transfer`, `remove-many`, `queue-research`, `spawn-character`, `wait-until`. Playthrough is `gqlcli query --op <name> --variables '{"input":{...}}'` / `gqlcli mutation --op <name> --input '{...}'`; `gqlcli op show --name <name>` prints the op's document so you can see its arguments. Pick the op for the move you want rather than assembling a request from scratch.
- Discover the live contract before first use of any operation: `gqlcli queries --args --desc`, `gqlcli mutations --args --desc`, `gqlcli describe Mutation.placeMany --args --depth 1`, `gqlcli validate --mutation '...'`.
- Check `status.paused` and `speed`: a paused game advances nothing.

## Core loop

1. **Baseline:** tick, actor, inventory, targets, research, power; `factoryBriefing` for attention items.
2. **Binding constraint:** recipe/tech, stock, space, power, input flow, output capacity (`production-math.md` section 8).
3. **Smallest complete change:** one functional unit with its fuel/power/input/output.
4. **Stabilize:** `waitUntil` (status, fluid, inventory, research, production, ticks) instead of polling; re-read after at least one machine cycle.
5. **Prove:** status leaves the bad state, rate moves, inserters have real endpoints. If proof fails, diagnose the changed path before adding capacity.

Prefer the purpose-built operations: `connectionPlan`/`connectEntities` for belt/pipe/pole runs, `placeNextTo` for adjacent buildings, `nearestPlacement`/`probePlacement(SITES)` for sites, `prototype` for sizes/stack/fuel, `mapArea` for text maps, `fuelMany`/`transferMany`/`placeMany`/`removeMany` for batches, `waitUntil` to wait.

## Shared GraphQL shapes

```graphql
ActorInput { player: String, character: UnitNumber }       # exactly one
PositionInput { x: FiniteFloat!, y: FiniteFloat! }
EntityRefInput { surface: String = "nauvis", entity: String, unitNumber: UnitNumber, position: PositionInput }
FactoryAreaInput { leftTopX leftTopY rightBottomX rightBottomY }   # most reads cap at 1024 tiles per side
PageInfo { total limit offset hasMore }
```

- Actors: `{player:"name"}` = that player's current character; `{character:"123"}` = any character incl. detached. `characterSpawn` makes a free empty worker; `characterDelete` retires a detached, empty, playerless one; `playerRestoreCharacter` gives a god-controller player a character; `characters(detachedOnly:false)` lists all.
- Directions are engine ints 0..15: north `0`, east `4`, south `8`, west `12`. Axes: x east, y south.
- `UnitNumber` values are strings. Items default to `normal` quality.
- Reach is permissive by default (`FACTORIO_ENFORCE_REACH=1` enforces; restart `serve` to change). Mining/harvest still need the actor within ~1-2 tiles in practice.
- Every successful action mutation prints a generated in-game message; `message` overrides it; `announce(message)` posts free chat.

## Query authoring: validate before acting

Most failed calls in real sessions are invented field names, not engine failures. Play through the saved ops where one exists (`gqlcli op list`, then `--op <name>`); for anything else compose from `gqlcli describe Type --args` or `gqlcli validate`; never guess.

- `ActorInput { player, character }` has no `actor` field. Use `inventory(input:{player:"x"})`, not `{actor:{...}}`.
- `RecipeInput { actor, force, recipe }`; there is no `item` or `name` field. `actor` is optional: without it (`force` defaults to `player`) the read still succeeds but `craftableCount` is null.
- `EntityRefInput { surface?, entity?, unitNumber?, position? }`; no `type`. Prefer `entity` + `unitNumber`; `unitNumber` alone is rejected.
- Response nesting: `EntityPage.entities[]`, `EntitySnapshot.entity`, `Entity.inspection(actor: ActorInput)`. The actor is optional; omit it for a force-agnostic read of neutral, enemy or other-force entities. `Entity` has no `recipeName`; `EntityInspection` has no `power` or `type`.
- `StockLocation` uses `stack { name quality count }` and `inventoryKind`; no `item`, `count`, `name`, or `inventory`. `StockInput.category` accepts `STORED`, `IN_TRANSIT`, `GROUND`, `PLAYER`, or `WORKER` (detached characters, carrying their unit number); omit for all.
- `ElectricNetworkInsight` uses `productionWatts` / `consumptionWatts` / `id` plus `maxProductionWatts`, `loadRatio` (observed load ratio, null when no producer is in the area), `polesInArea`, accumulator charge/capacity and `lowPowerConsumers`; no `production`, `consumption`, or `networkId`. `window` selects its flow window and is accepted only by `electricNetworks`.
- `ProductionItem.item` and `StockTotal.item`, not `name`. `FactoryOverview` has `machineTick` / `productionTick`, not `tick`.
- `ResearchInsightsTechnology.prerequisites { name researched }` and `effects { type recipe dataJSON }`. `ResearchState.currentResearch` reports the running technology (`technology` echoes the input); status is `RESEARCHED`, not `COMPLETED`.
- Enums are unquoted: `AVAILABLE`, `CHEST`, `TO_ACTOR`. `TransferDirection` is `TO_ENTITY | TO_ACTOR`.
- `TransferInput` requires `actor, target, inventory, direction, item, count`; no `from`, `to`, or `announce` fields.
- `WorldResourcesInput.resourceName`; `WorldResourceEntry` has no `bounds` (patches do).
- Areas use `leftTopX/Y` and `rightBottomX/Y`, never `min/max`. Areas are at most 1024 tiles per side; `mapArea` maxRows/maxColumns are 1..1024; `probePlacement` samples at most 262144 tiles.
- Object fields need sub-selections: `position {x y}`, `status {rawName category terminal rawCode}`, `prerequisites {name researched}`, `effects {type recipe dataJSON}`, `from { ... }`.
- Run `gqlcli validate` (or `--validate-only`) before executing. Use `gqlcli query -f json` when piping to jq. `gqlcli script` has no `--format`/`--jq`; `-f` there means `--file`.

The working tree accepts `surface` on `remove`, `mine`, and `beltTake` (covered by `TestCommandFlagsAcceptEntityRef`). An older `serve` process still running from before that fix rejects the same calls with `flag provided but not defined: -surface`; rebuild and restart it, or address the entity by position on the actor's surface meanwhile. Likewise an old `serve` can commit a `place` and then fail decoding `ActionResult.direction`; read the entity back before retrying. See `references/engine-traps.md`.

## Essential recipes

```graphql
# observe: one document, one round trip
{ status { tick speed paused players { name } }
  characters(input:{surface:"nauvis", detachedOnly:false}) { characters { unitNumber player position { x y } } }
  nearby(input:{surface:"nauvis", position:{x:0,y:0}, radius:40, order:DISTANCE, limit:100}) {
    pageInfo { total hasMore } entities { name type unitNumber position { x y } distance } } }

# place + verify identity
mutation { place(input:{actor:{character:"123"}, surface:"nauvis", position:{x:40.5,y:60.5}, item:"assembling-machine-1", direction:0}) { name unitNumber position { x y } } }

# batch place (<=10000); underground belts MUST carry undergroundType INPUT/OUTPUT
mutation { placeMany(input:{actor:{character:"123"}, surface:"nauvis", item:"transport-belt",
  placements:[{position:{x:41.5,y:60.5},direction:4},{position:{x:42.5,y:60.5},direction:4}]}) {
  placed failed errors { index code message target { position { x y } } } entities { name unitNumber position { x y } } } }

# recipe, inventory movement, inspection (note entity + unitNumber)
mutation { setRecipe(input:{actor:{character:"123"}, target:{entity:"assembling-machine-1", unitNumber:"456"}, recipe:"iron-gear-wheel", drain:ABORT}) { name recipe unitNumber } }
mutation { transfer(input:{actor:{character:"123"}, target:{entity:"assembling-machine-1", unitNumber:"456"}, inventory:INPUT, direction:TO_ENTITY, item:"iron-plate", count:20}) { moved item inventory } }
{ inspect(input:{actor:{character:"123"}, target:{entity:"assembling-machine-1", unitNumber:"456"}}) {
    name type statusName recipe craftingProgress unitNumber
    inventories { input { name count } output { name count } } fluids { name amount }
    logistics { inserters { inserter { name position { x y } pickupPosition { x y } dropPosition { x y } } pickup { name } drop { name } } } } }

# wait, then measure
mutation { waitUntil(input:{timeoutSeconds:10, condition:{ machineStatus:{ target:{entity:"assembling-machine-1", unitNumber:"456"}, statuses:["working"] } } }) { satisfied timedOut tick last { status } } }
```

Saved-op variable examples (run `gqlcli op show --name <op>` for the document; set `GQLCLI_SCHEMA_CACHE_TTL=0` or `--refresh-schema` after a schema change):

```sh
# batch inspection: per-target errors, stable unitNumber ids; read-only
gqlcli query --op inspect-many --variables '{"input":{"force":"player","targets":[{"unitNumber":"346"},{"unitNumber":"999999"}]}}'
# plan a belt stretch (read-only), then build it (dryRun:true plans only, result is null)
gqlcli query --op connection-plan --variables '{"input":{"surface":"nauvis","force":"player","from":{"x":41.5,"y":60.5},"to":{"x":47.5,"y":60.5},"kind":"BELT"}}'
gqlcli mutation --op build-connection --variables '{"dryRun":true,"input":{"actor":{"character":"123"},"surface":"nauvis","force":"player","from":{"x":41.5,"y":60.5},"to":{"x":47.5,"y":60.5},"kind":"BELT"}}'
# one belt tile needs an explicit direction (0 N, 4 E, 8 S, 12 W)
gqlcli mutation --op connect-entities --variables '{"input":{"actor":{"character":"123"},"surface":"nauvis","kind":"BELT","from":{"x":41.5,"y":60.5},"to":{"x":41.5,"y":60.5},"direction":4}}'
# machine + routed belt + recipe in one call; dryRun:true validates and writes nothing
gqlcli mutation --op build-cell --variables '{"input":{"actor":{"character":"123"},"dryRun":true,"entries":[{"ref":"gear","place":{"item":"assembling-machine-1","position":{"x":40.5,"y":60.5}}},{"ref":"feed","connection":{"from":{"x":44.5,"y":60.5},"to":{"x":48.5,"y":60.5},"kind":"BELT"}},{"ref":"r","recipe":{"ref":"gear","recipe":"iron-gear-wheel"}}]}}'
# plan a layout locally: footprints, directions, unit numbers, optional water
gqlcli query --op occupancy --variables '{"input":{"area":{"leftTopX":30,"leftTopY":50,"rightBottomX":60,"rightBottomY":70},"includeTerrain":true}}'
```

`gqlcli describe` any operation before copying an example into a consequential mutation. More recipes (resources, routing, belts measurement, research, diagnose) are in `api-map.md` and the topic references.

## Play efficiently

Time is the scarce resource; production advances whether or not the actor is busy.

- **Critical path first:** start with the deepest unfinished dependency (oil -> plastic -> advanced circuits -> processing units for a rocket) and let it run while shorter tasks finish. `scripts/tech-path.js` gives the research critical path; `research-and-science.md` section 6 gives the ordered rocket sequence (red/green backlog first, cheap gates next, `rocket-silo` last while the silo kit is built).
- **Never leave labs idle.** Queue the next technology; check `status.paused`.
- **Measure with numbers:** rates are deltas (`scripts/measure-rate.js`, `production` windows). Compute the implied ceiling (machines x rate) before blaming supply. Fix one constraint, re-measure, then the next.
- **Buffers are stops, not supply:** a full chest/belt halts its producer; measured 0/min can be a full sink. Bound every buffer (`setContainerBar allowedSlots`; null = no limit). Every output needs a drained consumer.
- **Balance whole chains:** throughput = weakest link; every product of a multi-output machine needs a consumer; keep construction supply separate from production supply.
- **Stage scarce materials deliberately:** bound reservations, release them when consumed, build the destination before filling its buffer.
- **Scale the proven thing:** clone a verified cell; use ghosts + robots for repetition only with coverage/materials/power confirmed; upgrade logistics only after production is sufficient.
- **Close objectives:** final assemblies need every ingredient at once; read live recipe ratios; a successful action can reset the machine; record tick, unit numbers, rates and the proof that completed the objective.
- Batch reads into one GraphQL document; avoid per-entity fan-out; do not busy-poll.

## Playbooks (short; details in the references)

**Survey and choose a site** (`bootstrap-and-progression.md`, `spatial-and-geometry.md`): `scripts/survey.js` for patches; `worldResources(groupPatches:true)`; `mapArea`; `probePlacement(SITES)` / `nearestPlacement`; check pole reach, belt/pipe route and expansion room. Proof: footprint legal, resource present, no conflicts, bounds and endpoints recorded.

**Obtain materials:** `stock` and `inventory`, then `recipe`/`itemRecipes`/`billOfMaterials`; `mine`/`harvestArea` for raw; `craft(wait:true)` only for bootstrap (`expand:true` for a whole hand-craft chain, `mode:UP_TO` so a timed-out call is resumed by re-issuing it; dry-run with `craftPlan`; read `cheatMode` on `player` first, since a cheat-mode actor crafts free and instantly); `transfer` from known containers. Re-read inventory after queued actions. Where is an item: `gqlcli query --op stock --variables '{"input":{"item":"inserter"}}'` (saved `stock` op, rows carry `unitNumber`); `category: STORED` = chests/machines you can take from, `PLAYER`/`WORKER` = carried, `IN_TRANSIT` = belt traffic, not retrievable.

**Build a production cell** (`spatial-and-geometry.md` section 6): recipe discovery, bill of materials, probe, obtain all items first, place machine, `setRecipe`, place inserters/belts from endpoint toward machine, power, bounded output, then `inserter-audit.js` and a status/progress proof. Never add a second machine until the first works.

**Mining and smelting:** patch + `nearbyDrills`, drill output directly into a furnace or belt, one drill first, then the row; fuel path included; measure belt rate. **Power:** one complete chain (pump -> boiler -> engine -> poles) with continuous fuel and water, then prove `electricNetworks` production vs load for >= 1800 ticks.

**Route belts/pipes:** `connectionPlan` then `connectEntities` (ASTAR for obstacles); verify with `beltStretch`; measure with `beltStretchMeasure`; never connect two fluids.

**Diagnose a stall:** `factoryBriefing` -> status table -> decision order in `diagnostics.md`; fix the smallest root cause; compare the same fields after stabilization.

**Factorioform state:** `factorioformState(name:)` reads the manifest address -> unit number mapping, `factorioformImport` adopts a live entity under an address (refuses a remap without `overwrite:true` and a unit already mapped elsewhere), `factorioformForget` drops addresses (empty list = whole manifest) without touching entities. It lives in the save's script storage: per save, not copied by blueprint export.

**Inserter vectors:** `setInserterSettings` `pickupVector`/`dropVector` (offsets from the inserter centre in map axes; read back via `inspect{settings{inserter{pickupVector dropVector customVectorsAllowed}}}`) only work on prototypes with `allow_custom_vectors` (none in vanilla/Space Age; the call errors otherwise). Vectors rotate with `setDirection`, so write them after it. Manifests use `inserter { pickup_offset = {x,y} drop_offset = {x,y} }`.

**Factorioform plans:** `factorioformPlan(input:{files:[{name,content}],variables,actor})` is read-only and returns `status`, `diffs`, ordered `steps` and `materials`. Run each step's `document` with its `variables` through gqlcli in order (pass `actor` in the plan or add `input.actor` when `requiresActor`), then call `factorioformRefresh` and plan again: `CONVERGED` with no steps means done. `planId` changes whenever the manifest, state or world changes. ADOPT rows have no steps; refresh records them. After DRIFT/REPLACE the first refresh only prunes the dead unit (status stays `CHANGES`); refresh again to record the recreated entity. A full worked manifest lives in `testdata/factorioform/example.ftf`. Manifest errors arrive as `file:line:col` lines in the GraphQL error. CLI: `factorio-ai-player factorioform plan --file x.ftf [--var k=v] [--actor-player NAME]` prints the same JSON.

**Research and science, oil, rocket, robots/blueprints, circuits/trains, multi-agent/tickets:** see the matching reference. The rocket victory condition is a verified launch (`rocket-and-endgame.md`).

## Common traps (full list: engine-traps.md)

- `{unitNumber}` alone is rejected; send `entity` too. Underground belt halves need `undergroundType` `INPUT` on the entrance and `OUTPUT` on the exit, but they pair across calls: a facing half placed later completes the tunnel.
- Inserter direction points at its PICKUP side (`0`:pickup north/drop +y, `4`:east/-x, `8`:south/-y, `12`:west/+x). Belt direction is inverted on y in this save (`8`=+y, `0`=-y; `4`=+x, `12`=-x). Verified live on 2.0.77, ticks 1507030-1513866. **Mining drills differ: `direction` is the DROP side** — `0` drops north (-y), `4` east (+x), `8` south (+y), `12` west (-x), i.e. the belt sense, opposite the inserter (live ticks 2919457, 3672835; `spatial-and-geometry.md` section 5).
- Underground entrances accept a feed only from directly behind (opposite the facing); a perpendicular belt cannot feed a tunnel mouth, and a tunnel inside a 1-tile-wide corridor cannot exit it.
- `setDirection` **does** work on transport belts in this build: a direct write returned `direction` changed with `turns:0`, both on an empty belt and on one carrying an item (live, tick ~1.22M). Rails are reported to ignore it, so `setDirection` falls back to `rotate` steps there and `turns>0` tells you the fallback ran; place rails with the final direction. `placeBlueprint` only rotates in 90° steps, so its `direction` must be 0/4/8/12. A **single-tile** `connectEntities` run requires an explicit `direction` (0/4/8/12) because the route implies none; a run of two or more tiles rejects it. Lay corners as two-tile runs.
- `setContainerBar allowedSlots:0` leaves 0 usable slots (`transfer` shows `moved:0`); null removes the limit. `transfer` also reports `bar`, `toBeDeconstructed`, and `reason` (`BAR_LIMITED`, `NO_CAPACITY`, `NO_SOURCE`, `MARKED_FOR_DECONSTRUCTION`, `FILTERED`).
- Script-set requester slots fill to `min`, not `max`.
- `gqlcli` needs the repo root as CWD; mutation scripts need `--strict=false`; `script` has no `--jq`.
- `researchInsights.prerequisites` is `[{name researched}]`; trigger techs (`researchTriggerJSON`) complete by crafting/mining/building, and `startResearch` **refuses** them (`technology is a trigger technology (...)`), so perform the trigger action instead of queueing. Never queue behind a technology whose prerequisite is unresearched: `queueResearch` rejects it.
- `circuitNetwork` now emits `RED`/`GREEN` itself and accepts `connector` (default `circuit_<color>`, a combinator's INPUT connector); `circuitNetworks` lists a whole area's networks by `networkId`. Older `serve` builds still need the `normalizeGraphQL` colour rewrite (inserter prototype vectors `inserterDrop` array vs `Position` are normalized the same way). Rebuild `serve` before relying on either. `SignalInput.type` is the `SignalType` enum (`ITEM`, `FLUID`, `VIRTUAL`, `ENTITY`, `RECIPE`, `SPACE_LOCATION`, `ASTEROID_CHUNK`, `QUALITY`), not a lowercase string.
- `production` returns `items` and `fluids` and accepts `itemNames`; `itemFlow` rates are theoretical; `stock` is not global storage; full belt != throughput.
- Batch reads beat per-row fan-out: `inspectMany` (1-1000 targets, per-row `error`), `entitySettings`, `factoryAlerts`, `itemBalance`, `fluidSegments`, `trainStops`. Forced nested fields over big lists serialize behind the CLI's six-connection RCON cap.
- `place`/`placeMany` without `fastReplace` refuse a tile that already holds an entity whose collision mask intersects the placed prototype's mask (`COLLISION`, occupant named) before `create_entity`, because `create_entity` ignores collision boxes and would stack a second entity on the same tile; the test is tile-wide and mask-based, so a neighbouring footprint that clips the tile blocks, while ghosts, tile ghosts, item stacks, resources and corpses (doodad layer, tiles only: a tree stump never blocks a belt or pipe) are never occupants; `fastReplace` first proves the tile replaceable (`can_fast_replace` or a legal manual placement) and otherwise reports `COLLISION`, with the replaced entity and its contents going to the acting character. `placeNextTo` runs the same occupancy test on each nudged candidate and skips an occupied one. All three probe the tile after a refused creation, because the engine can raise or return nil after committing the entity: a committed entity is reported as placed instead of refunding (and duplicating) the item.
- `dropItem` ground drops carry no force, so they are never marked for deconstruction; the result reports `remaining`, `groundPositions`, and `line`. `placeTile` reports `previousTile` and recovered `items`; `removeMany` returns per-target `results` plus `items`.
- `mine` addresses resources/trees/rocks by name + position only (a unit number is rejected); `beltTake` works on any transport-belt tier.
- `repair` restores 600 HP per normal repair pack (prototype durability × repair speed); `teleport` defaults to the actor's surface and only moves players across surfaces; `shoot` reports rounds fired and force.
- `placeBlueprint` makes ghosts from a string (`entityCount`/`skipped` in the result), `placeGhosts` makes them from explicit entries, `buildGhosts` builds in one tick but charges the ghost cost from the actor main inventory and/or a source inventory, reporting `MATERIALS` or `COLLISION` per ghost, `clearArea` drops nothing, `shutdown` does not save.
- A cargo pod on a loaded silo is the pre-launch pod, not proof of launch; read silo status and `rocketParts`.
- The headless server cannot render screenshots; a connected graphical client is required.
- Burner cells without an automated fuel path stall (`no_fuel`); treat fuel supply as part of the cell.
