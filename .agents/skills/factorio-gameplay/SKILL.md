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
| Running gqlcli, scripts, waiting, error handling, `--strict`, CWD trap | [references/gqlcli-workflow.md](references/gqlcli-workflow.md) |
| Which query/mutation to use; semantics of each; new ops (`factoryBriefing`, `connectEntities`, `placeNextTo`, `nearestPlacement`, `mapArea`, `prototype`, `waitUntil`, `fuelMany`, `transferMany`) | [references/api-map.md](references/api-map.md) |
| Where to put things: coordinates, footprints, **inserter direction rule**, belts/lanes, pole reach, worked cell layouts | [references/spatial-and-geometry.md](references/spatial-and-geometry.md) |
| Starting a save, stage ladder, tech order, red-science cell, fuel discipline | [references/bootstrap-and-progression.md](references/bootstrap-and-progression.md) |
| Rates, ratios, machine counts, recipes, power sizing, rocket totals | [references/production-math.md](references/production-math.md) |
| Something is stalled/slow: triage, status table, decision order | [references/diagnostics.md](references/diagnostics.md) |
| Verified engine/API traps and known defects | [references/engine-traps.md](references/engine-traps.md) |
| Oil, pipes, contamination, fluid repair | [references/fluids-and-oil.md](references/fluids-and-oil.md) |
| Research control, trigger techs, lab feeding | [references/research-and-science.md](references/research-and-science.md) |
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

1. **Read the reference for your task first** (table above) and confirm the live contract (`gqlcli queries|mutations --args`, `gqlcli describe`, `gqlcli validate`) before the first use of an operation.
2. **Observe, plan, act, verify, in that order.** Never run a long mutation chain against assumed state; a live read after the write is the only proof.
3. **Exact engine names.** Items, recipes, technologies, entities, fluids, inventories come from `prototype`, `itemRecipes`, `researchInsights`, `recipe`. Never translate a display name from memory.
4. **Address entities with `entity` AND `unitNumber`** (`{entity:"lab", unitNumber:"60"}`), or `entity` + exact center `position`. `{unitNumber}` alone fails (`prototype is not resolvable by unit number alone; pass entity too`). Keep the unit numbers placement returned.
5. **Compute geometry, do not guess it.** Inserter `direction` points to the PICKUP side, drop is opposite; odd footprints centre on `.5`, even on integers. Re-read `pickupPosition`/`dropPosition` after placing. See `spatial-and-geometry.md`.
6. **Keep changes bounded.** One machine, one belt segment, one cell, then inspect and measure. Clone only a verified block.
7. **Re-read the target immediately before every write.** Other agents share this save; unfamiliar unit numbers, structures and tags are theirs.
8. **`save` before any authorized restart.** `shutdown` does not save; `just restart` can silently switch saves.
9. **Record evidence for every claim:** tick, unit numbers, measured rate, and the read that proved it. `announce` a milestone only after that proof.
10. **Stop and report anomalies** (unexpected error, null where data is promised, success with unchanged state, behaviour contradicting these docs) with the exact document, variables, tick and observed-vs-expected. Never mask one with a workaround, throwaway script or retry loop.
11. **Automate before scaling, and never accept manual labour as the solution.** Mining, smelting, crafting, hauling, fuelling and loading all come from machines on a belt or logistic network; hand work is allowed only to bootstrap, unblock a stall, or make a one-off the factory cannot build yet. A repeated manual action is a missing cell or mall item — build that cell, then do the batch job once. Every burner entity MUST have an automated fuel path, and every intermediate MUST have a drained consumer before its producer is cloned.
12. **Leverage robots once they exist.** After `construction-robotics`/`logistic-robotics` and a powered roboport network, construction, repair, deficit logistics and repeated fills MUST go through ghosts + robots or requester chests rather than manual placement — but only over verified coverage with materials and power in place.

### SHOULD

1. Batch reads into one GraphQL document; replace polling with `waitUntil`; never busy-poll.
2. Progress with numbers: measure rates as deltas (`scripts/measure-rate.js`, `production`), compute the implied ceiling before adding capacity.
3. Bound every buffer (`setContainerBar`) and give every output a drained consumer: a full chest or belt halts its producer.
4. Route with purpose-built operations — `connectionPlan`/`connectEntities`, `placeNextTo`, `nearestPlacement`, `probePlacement`, `placeMany`, `transferMany`, `fuelMany`, `waitUntil` — instead of ad-hoc tile-by-tile work.
5. Keep construction supply separate from production supply, and stage scarce materials with bounded reservations.
6. Leave corridor and expansion space; a small main bus or explicit routing plan beats a tightly packed block.
7. Do remote work from detached `characterSpawn` workers; leave the human's character where it is.
8. Fix the single upstream root cause found by `factoryBriefing` triage, then re-measure the same fields.
9. Roll robots out in that order: roboport coverage first (`probePlacement prototype:"roboport" mode:GRID`, centres < 50 apart on both axes), then power, then robot and repair-pack supply, then ghosts/requester chests; a mall or construction cell that robots must feed has to exist before the ghost wave. See `robots-and-construction.md`.

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
4. **Never stop, restart, `shutdown`, reinstall the mod, change `setGameSpeed`, or switch saves** during gameplay work on the shared server.
5. **Never delete, re-delete, re-flag or drain** unfamiliar entities, chart tags, storage or logistics contents.
6. **Never use admin shortcuts for normal play** (`teleport`, `setGameSpeed`, `setFluid`, `buildGhosts`, `unlockAllTechnologies`, `setEntityTags`) — sandbox or recovery only, with explicit authority and a record.
7. **Never bypass gqlcli** with `go run` or raw RCON, and never call `shutdown` to end a session.
8. **Never leave labs idle** while research is affordable: queue the next technology, and check `status.paused`/`speed` before calling anything stalled.
9. **Never accept a mutation acknowledgement, a placed ghost, or a queued craft as proof** of the outcome it claims.
10. **Never transfer `rocket-part`**; the silo creates it.

## Session start (30 seconds)

```sh
cd /Users/wricardo/go/src/github.com/wricardo/factorio-ai-player   # gqlcli needs .gqlcli.json from CWD, else 404
gqlcli query '{ status { tick speed paused players { name surface position { x y } actor { character hasCharacter } } } }'
gqlcli script --strict=false --file .agents/skills/factorio-gameplay/scripts/briefing.js --arg '{"limit":15}'
```

- `.gqlcli.json` env `local` = `http://127.0.0.1:8080/query`. Add or fix it with `gqlcli config add-env --name local --url ...` and `config set-default`; never `config init --force` over an existing file. Load the `gqlcli` skill for the full CLI.
- gqlcli is the only interface. If the endpoint is down, report it (check `just logs`, `just graphql`); never switch to `go run` or raw RCON.
- Discover the live contract before first use of any operation: `gqlcli queries --args --desc`, `gqlcli mutations --args --desc`, `gqlcli describe Mutation.placeMany --args --depth 1`, `gqlcli validate --mutation '...'`.
- Check `status.paused` and `speed`: a paused game advances nothing.

## Core loop

1. **Baseline:** tick, actor, inventory, targets, research, power; `factoryBriefing` for attention items.
2. **Binding constraint:** recipe/tech, stock, space, power, input flow, output capacity (`production-math.md` section 8).
3. **Smallest complete change:** one functional unit with its fuel/power/input/output.
4. **Stabilize:** `waitUntil` (status, fluid, inventory, research, production, tick) instead of polling; re-read after at least one machine cycle.
5. **Prove:** status leaves the bad state, rate moves, inserters have real endpoints. If proof fails, diagnose the changed path before adding capacity.

Prefer the purpose-built operations: `connectionPlan`/`connectEntities` for belt/pipe/pole runs, `placeNextTo` for adjacent buildings, `nearestPlacement`/`probePlacement(SITES)` for sites, `prototype` for sizes/stack/fuel, `mapArea` for text maps, `fuelMany`/`transferMany`/`placeMany`/`removeMany` for batches, `waitUntil` to wait.

## Shared GraphQL shapes

```graphql
ActorInput { player: String, character: UnitNumber }       # exactly one
PositionInput { x: FiniteFloat!, y: FiniteFloat! }
EntityRefInput { surface: String = "nauvis", entity: String, unitNumber: UnitNumber, position: PositionInput }
FactoryAreaInput { leftTopX leftTopY rightBottomX rightBottomY }   # most reads cap at 256 tiles per side
PageInfo { total limit offset hasMore }
```

- Actors: `{player:"name"}` = that player's current character; `{character:"123"}` = any character incl. detached. `characterSpawn` makes a free empty worker; `playerRestoreCharacter` gives a god-controller player a character; `characters(detachedOnly:false)` lists all.
- Directions are engine ints 0..15: north `0`, east `4`, south `8`, west `12`. Axes: x east, y south.
- `UnitNumber` values are strings. Items default to `normal` quality.
- Reach is permissive by default (`FACTORIO_ENFORCE_REACH=1` enforces; restart `serve` to change). Mining/harvest still need the actor within ~1-2 tiles in practice.
- Every successful action mutation prints a generated in-game message; `message` overrides it; `announce(message)` posts free chat.

## Query authoring: validate before acting

Most failed calls in real sessions are invented field names, not engine failures. Compose from `gqlcli describe Type --args`, `gqlcli validate`, or a saved `.gqlcli.json` op; never guess.

- `ActorInput { player, character }` has no `actor` field. Use `inventory(input:{player:"x"})`, not `{actor:{...}}`.
- `RecipeInput { actor, recipe }`; there is no `item` or `name` field.
- `EntityRefInput { surface?, entity?, unitNumber?, position? }`; no `type`. Prefer `entity` + `unitNumber`; `unitNumber` alone is rejected.
- Response nesting: `EntityPage.entities[]`, `EntitySnapshot.entity`, `Entity.inspection(actor: ActorInput!)`. `Entity` has no `recipeName`; `EntityInspection` has no `power` or `type`.
- `StockLocation` uses `stack { name quality count }` and `inventoryKind`; no `item`, `count`, `name`, or `inventory`. `StockInput.category` accepts `STORED`, `IN_TRANSIT`, `GROUND`, or `PLAYER`; omit for all.
- `ElectricNetworkInsight` uses `productionWatts` / `consumptionWatts` and `id`; no `production`, `consumption`, or `networkId`.
- `ProductionItem.item` and `StockTotal.item`, not `name`. `FactoryOverview` has `machineTick` / `productionTick`, not `tick`.
- `ResearchInsightsTechnology.prerequisites { name researched }` and `effects { type recipe dataJSON }`. `ResearchState.currentResearch` reports the running technology (`technology` echoes the input); status is `RESEARCHED`, not `COMPLETED`.
- Enums are unquoted: `AVAILABLE`, `CHEST`, `TO_ACTOR`. `TransferDirection` is `TO_ENTITY | TO_ACTOR`.
- `TransferInput` requires `actor, target, inventory, direction, item, count`; no `from`, `to`, or `announce` fields.
- `WorldResourcesInput.resourceName`; `WorldResourceEntry` has no `bounds` (patches do).
- Areas use `leftTopX/Y` and `rightBottomX/Y`, never `min/max`. Areas are at most 256 tiles per side; `mapArea` maxRows/maxColumns are 1..64; `probePlacement` samples at most 16384 tiles.
- Object fields need sub-selections: `position {x y}`, `status {rawName category terminal rawCode}`, `prerequisites {name researched}`, `effects {type recipe dataJSON}`, `from { ... }`.
- Run `gqlcli validate` (or `--validate-only`) before executing. Use `gqlcli query -f json` when piping to jq. `gqlcli script` has no `--format`/`--jq`; `-f` there means `--file`.

Older `serve` builds also reject `remove`/`mine`/`beltTake` addressed with an `EntityRefInput` surface (`flag provided but not defined: -surface`), and `place` can commit then fail with `decode place response ... ActionResult.direction of type int`. Read back the entity/inventory; use `removeMany` or `placeMany`, or rebuild `serve` if authorized. See `references/engine-traps.md`.

## Essential recipes

```graphql
# observe: one document, one round trip
{ status { tick speed paused players { name } }
  characters(input:{surface:"nauvis", detachedOnly:false}) { characters { unitNumber player position { x y } } }
  nearby(input:{surface:"nauvis", position:{x:0,y:0}, radius:40, order:DISTANCE, limit:100}) {
    pageInfo { total hasMore } entities { name type unitNumber position { x y } distance } } }

# place + verify identity
mutation { place(input:{actor:{character:"123"}, surface:"nauvis", position:{x:40.5,y:60.5}, item:"assembling-machine-1", direction:0}) { name unitNumber position { x y } } }

# batch place (<=1000); underground belts MUST carry undergroundType INPUT/OUTPUT
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
mutation { waitUntil(input:{timeoutSeconds:60, condition:{ machineStatus:{ target:{entity:"assembling-machine-1", unitNumber:"456"}, statuses:["working"] } } }) { satisfied timedOut tick last { status } } }
```

`gqlcli describe` any operation before copying an example into a consequential mutation. More recipes (resources, routing, belts measurement, research, diagnose) are in `api-map.md` and the topic references.

## Play efficiently

Time is the scarce resource; production advances whether or not the actor is busy.

- **Critical path first:** start with the deepest unfinished dependency (oil -> plastic -> advanced circuits -> processing units for a rocket) and let it run while shorter tasks finish. `scripts/tech-path.js` gives the research critical path.
- **Never leave labs idle.** Queue the next technology; check `status.paused`.
- **Measure with numbers:** rates are deltas (`scripts/measure-rate.js`, `production` windows). Compute the implied ceiling (machines x rate) before blaming supply. Fix one constraint, re-measure, then the next.
- **Buffers are stops, not supply:** a full chest/belt halts its producer; measured 0/min can be a full sink. Bound every buffer (`setContainerBar`; usable slots = bar-1). Every output needs a drained consumer.
- **Balance whole chains:** throughput = weakest link; every product of a multi-output machine needs a consumer; keep construction supply separate from production supply.
- **Stage scarce materials deliberately:** bound reservations, release them when consumed, build the destination before filling its buffer.
- **Scale the proven thing:** clone a verified cell; use ghosts + robots for repetition only with coverage/materials/power confirmed; upgrade logistics only after production is sufficient.
- **Close objectives:** final assemblies need every ingredient at once; read live recipe ratios; a successful action can reset the machine; record tick, unit numbers, rates and the proof that completed the objective.
- Batch reads into one GraphQL document; avoid per-entity fan-out; do not busy-poll.

## Playbooks (short; details in the references)

**Survey and choose a site** (`bootstrap-and-progression.md`, `spatial-and-geometry.md`): `scripts/survey.js` for patches; `worldResources(groupPatches:true)`; `mapArea`; `probePlacement(SITES)` / `nearestPlacement`; check pole reach, belt/pipe route and expansion room. Proof: footprint legal, resource present, no conflicts, bounds and endpoints recorded.

**Obtain materials:** `stock` and `inventory`, then `recipe`/`itemRecipes`/`billOfMaterials`; `mine`/`harvestArea` for raw; `craft(wait:true)` only for bootstrap; `transfer` from known containers. Re-read inventory after queued actions.

**Build a production cell** (`spatial-and-geometry.md` section 6): recipe discovery, bill of materials, probe, obtain all items first, place machine, `setRecipe`, place inserters/belts from endpoint toward machine, power, bounded output, then `inserter-audit.js` and a status/progress proof. Never add a second machine until the first works.

**Mining and smelting:** patch + `nearbyDrills`, drill output directly into a furnace or belt, one drill first, then the row; fuel path included; measure belt rate. **Power:** one complete chain (pump -> boiler -> engine -> poles) with continuous fuel and water, then prove `electricNetworks` production vs load for >= 1800 ticks.

**Route belts/pipes:** `connectionPlan` then `connectEntities` (ASTAR for obstacles); verify with `beltStretch`; measure with `beltStretchMeasure`; never connect two fluids.

**Diagnose a stall:** `factoryBriefing` -> status table -> decision order in `diagnostics.md`; fix the smallest root cause; compare the same fields after stabilization.

**Research and science, oil, rocket, robots/blueprints, circuits/trains, multi-agent/tickets:** see the matching reference. The rocket victory condition is a verified launch (`rocket-and-endgame.md`).

## Common traps (full list: engine-traps.md)

- `{unitNumber}` alone is rejected; send `entity` too. `placeMany` underground belts need `undergroundType`.
- Inserter direction = pickup side. Wrong by one tile, or on top of a machine edge, is the most common build failure.
- `setDirection`/`rotate` do not work on belts/rails in 2.0.77; place with the final direction.
- `setContainerBar bar:1` leaves 0 usable slots (`transfer` shows `moved:0`).
- Script-set requester slots fill to `min`, not `max`.
- `gqlcli` needs the repo root as CWD; mutation scripts need `--strict=false`; `script` has no `--jq`.
- `researchInsights.prerequisites` is `[{name researched}]`; trigger techs (`researchTriggerJSON`) complete by crafting/mining/building.
- Older `serve` builds fail decoding `circuitNetwork` wire colours (`"red is not a valid WireColor"`) and inserter prototype vectors (`inserterDrop` array vs `Position`). The working tree normalizes both; rebuild `serve` to use them. Until then use the workarounds in the references and report anomalies.
- `production` has no fluids; `itemFlow` rates are theoretical; `stock` is not global storage; full belt != throughput.
- `placeBlueprint` makes ghosts, `buildGhosts` is an admin shortcut, `clearArea` drops nothing, `shutdown` does not save.
- A cargo pod on a loaded silo is the pre-launch pod, not proof of launch; read silo status and `rocketParts`.
- The headless server cannot render screenshots; a connected graphical client is required.
- Burner cells without an automated fuel path stall (`no_fuel`); treat fuel supply as part of the cell.
