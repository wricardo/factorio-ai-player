---
name: factorio-gameplay
description: "Gameplay handbook for Factorio through factorio-ai-player GraphQL: how to survey, gather, automate, build, route, power, diagnose, research, configure, and launch a rocket."
---

# Factorio gameplay via factorio-ai-player

Gameplay reference for completing Factorio tasks through the repository's GraphQL API, from raw-resource collection through verified rocket launch.

Repository schemas and live game state outrank this document. Factorio version, enabled mods, Space Age, research, recipe changes, quality, and save settings can change requirements. Discover before acting.

## Supporting guides

Read the guide relevant to the task before planning a factory change:

- [Automation and mall strategy](references/automation-and-mall.md): automation-first progression, early construction mall, bounded buffers, mall growth, and protection of science supply.
- [Layout, main bus, and factory evolution](references/layout-and-factory-evolution.md): main-bus choices, expansion space, modular blocks, transport selection, and safe factory cutovers.
- [Combat, defense, and damage recovery](references/combat-and-defense.md): threat assessment, automated defenses, player combat, ammunition supply, and repair strategy.

These guides provide strategy. Repository schemas and live state still define available operations and actual requirements.

## Non-negotiable operating rules

1. **Observe, plan, act, verify.** Never issue a long mutation chain against assumed state.
2. **Never blind-retry a mutation.** Operations are not transactional. An error or lost response does not prove rollback.
3. **Use exact engine names.** Discover recipes, technologies, prototypes, inventories, qualities, and fields. Never translate display names from memory.
4. **Prefer stable identity.** Keep `unitNumber` from placement or reads and address entities through `EntityRefInput`.
5. **Keep changes bounded.** One machine, one belt segment, or one production block; then inspect and measure.
6. **Protect shared state.** Do not stop/restart server, delete unrelated structures, drain shared storage, or alter global speed without need and authority.
7. **Separate normal play from administrative shortcuts.** `teleport`, `setGameSpeed`, `setFluid`, `buildGhosts`, and `unlockAllTechnologies` bypass normal progression. Use them only in deliberately chosen sandbox play or state recovery.
8. **Automate recurring demand.** Hand crafting is for bootstrap, recovery, or rare singletons. Repeated manual crafting or loading means the factory needs a production cell, supply route, or mall item.

## Authority and discovery

GraphQL contract changes faster than prose. Prefer `gqlcli` through the local HTTP API for gameplay, discovery, validation, filtering, batching, and dependent scripts. Load the `gqlcli` skill before using it; its command surface is richer than the repository's `go run . query|mutation` wrappers.

Gameplay GraphQL endpoint: `http://localhost:8080/query`.

Prefer a project `.gqlcli.json` environment over repeating `--url` or relying on shell state. At session start, check for `.gqlcli.json`. If it does not exist, create and configure it through gqlcli:

```sh
gqlcli config init
gqlcli config add-env --name local --url http://localhost:8080/query
gqlcli config set-default --name local
```

`config add-env` adds or updates the named environment. Never run `gqlcli config init --force` against an existing file; that can destroy curated operations, scripts, headers, and environments. When `.gqlcli.json` already exists, inspect it with `gqlcli config list`, preserve its operations and scripts, and update only the `local` environment or default when incorrect:

```sh
gqlcli config add-env --name local --url http://localhost:8080/query
gqlcli config set-default --name local
gqlcli config list
```

For a one-off session where project configuration cannot be written, use:

```sh
export GRAPHQL_URL="http://localhost:8080/query"
```

Discover the live contract before the first action and whenever an operation rejects an expected field:

```sh
gqlcli queries --args --desc
gqlcli mutations --args --desc
gqlcli describe PlaceInput --args --desc
gqlcli describe Mutation.launchRocket --args --desc
gqlcli describe EntityInspection --args --desc
```

Use gqlcli for execution:

```sh
gqlcli query '{ status { tick speed paused players { name surface position { x y } } } }'
gqlcli mutation 'mutation { announce(message:"Factory inspection started") }'
```

gqlcli practices:

- Inspect `.gqlcli.json` operations when present; reuse curated operations and scripts before writing new ones.
- Use built-in `--jq` for response selection instead of external JSON pipelines.
- Use `validate` or `--validate-only` before consequential operations.
- Use `batch` only when operations are independent and known up front.
- Use `script` with `gql.each` for dependent reads, loops, branching, or controlled concurrency.
- Gameplay mutations remain non-transactional through HTTP. Never blind-retry or treat a failed response as rollback proof.
- Nested `@goField(forceResolver)` fields can issue extra RCON reads. Treat them as separate snapshots.
- Keep `http://localhost:8080/query` local. The service controls Factorio through administrative RCON.

The repository Go CLI is fallback only when the HTTP service or gqlcli is unavailable:

```sh
export FACTORIO_RCON_PASSWORD="$(cat .factorio/password)"
go run . query '{ status { tick } }'
go run . mutation 'mutation { announce(message:"Fallback CLI active") }'
```

For fallback commands, global `--addr HOST:PORT` precedes the command; default RCON address is `127.0.0.1:27015`. The Go CLI uses `--format json`, `-v/--variables`, and `--var-file`; `-f` means `--file`, and it has no built-in `--jq`.

## Shared GraphQL shapes

```graphql
ActorInput {
  player: String
  character: UnitNumber
} # exactly one

PositionInput { x: FiniteFloat!, y: FiniteFloat! }

EntityRefInput {
  surface: String = "nauvis"
  entity: String
  unitNumber: UnitNumber
  position: PositionInput
}

FactoryAreaInput {
  leftTopX: FiniteFloat!
  leftTopY: FiniteFloat!
  rightBottomX: FiniteFloat!
  rightBottomY: FiniteFloat!
}

PageInfo { total: Int!, limit: Int!, offset: Int!, hasMore: Boolean! }
```

Actor rules:

- `{player:"name"}` targets connected player's current character.
- `{character:"123"}` targets any character unit number, including detached characters.
- `playerRestoreCharacter` gives sandbox/god controller a character without moving main inventory.
- `characterSpawn` creates an empty detached character near requested point.
- `characterCreate` requires a disconnected player and moves that player's main inventory into detached character.
- `characterReturn` returns detached character inventory and destroys detached character.
- `characters` discovers detached and player-controlled characters.
- Unit numbers are GraphQL `UnitNumber`; pass them as quoted strings when uncertain about client integer precision.

Entity reference rules:

- Prefer `{unitNumber:"..."}`. Add `surface` when needed.
- Otherwise provide both exact prototype and center position: `{entity:"stone-furnace", position:{x:40.5,y:60.5}}`.
- Resource entities, trees, rocks, and dropped stacks often lack unit numbers. Use exact positions.
- Directions are canonical engine values `0..15`: north `0`, east `4`, south `8`, west `12`.

## Reach, notifications, and safety

Reach checks are **disabled by default** because RCON is administrative. `FACTORIO_ENFORCE_REACH=1` enables build and interaction reach enforcement. A long-running `serve` process must restart to observe environment changes; CLI calls read it per operation.

Navigation remains useful for legitimate play, observation, and player-only actions. `navigateStart` accepts a position, `targetCharacter`, or `targetEntity`, and only one job may run per character. Poll `navigation(id)` until terminal state.

Successful action mutations print generated in-game notifications. Most mutation inputs expose optional `message`; nonblank `message` replaces generated text. Old `announce:Boolean` does not exist. `announce(message)` sends free-form chat. `beltThroughputStart` has no notification field.

RCON credentials are administrative secrets. Mutations can alter save and disable achievements. Save before risky experiments only when authorized; never shut down shared server casually.

## Current operation map

Snapshot: 40 queries and 68 mutations. Run discovery instead of trusting counts after schema changes.

### Queries

| Domain | Operations | Use |
|---|---|---|
| Session and actors | `status`, `player`, `characters`, `inventory`, `queue`, `nearby` | Tick, speed, players, characters, inventory, craft queue, local entities. |
| Entity inspection | `entity`, `inspect`, `entities`, `belts` | Identity, inventories, fluids, status, recipe, settings, bounded scans, belt contents. |
| Recipes and planning | `recipe`, `itemRecipes`, `billOfMaterials`, `itemFlow` | Craftability, producing recipes, recursive demand, configured producers/consumers. |
| Factory health | `factoryOverview`, `production`, `machines`, `stock` | Production windows, machine states, diagnostics, item locations. |
| Topology and flow | `beltRun`, `factoryGraph`, `adjacency`, `beltThroughput` | Structural connections, misalignment, measured lane flow. |
| World planning | `worldResources`, `worldLogistics`, `worldPlacement`, `probePlacement` | Resource patches, obstacles, logistics entities, collision-safe routing. |
| Power and fluids | `electricNetworks`, `powerConsumers`, `fluidStock` | Network output/load, unpowered consumers, fluid totals and locations. |
| Research | `researchInsights`, `scienceBudget` | Available technology, queue, pack demand and lab shortages. |
| Construction | `ghosts`, `logisticNetworks`, `constructionOrders` | Ghost backlog, robot capacity, deconstruction/upgrade work. |
| Trains and circuits | `trains`, `circuitNetwork` | Train schedule/cargo/state and red/green network signals. |
| World metadata | `surfaces` | Surface and planet metadata. |
| Navigation | `navigation` | Poll navigation job. |
| Player aggregate | `playerInsight` | Player, inventory, queue, recipe, and optional target reach in one read. |

Important read semantics:

- `nearby` is paginated and ordered (`DISTANCE` or `UNIT_NUMBER`), not an exhaustive area scan.
- `stock` totals supported inventories, belts, and ground stacks; it is not universal storage truth.
- `itemFlow` rates are theoretical. `beltThroughput` is measured flow.
- `factoryGraph`, `beltRun`, and `adjacency` report structure, not throughput.
- `billOfMaterials.complete:false` means bounded or unresolved expansion, not impossible production.
- `ghosts.buildable` checks observed materials only. It does not reserve stock or prove robot coverage, collision freedom, or delivery.
- `electricNetworks.satisfaction` can be null because runtime API does not expose instantaneous satisfaction. Use production/consumption plus machine and consumer status.
- Forced relationship fields such as `EntityInspection.settings`, machine `diagnostics`, logistics, and fluid networks are separate snapshots.

### Mutations

| Domain | Operations | Use |
|---|---|---|
| Actors and movement | `characterCreate`, `characterReturn`, `characterSpawn`, `playerRestoreCharacter`, `navigateStart`, `navigateCancel`, `teleport` | Obtain a character, travel, or use administrative relocation. |
| Build and remove | `place`, `placeMany`, `placeTile`, `remove`, `removeMany`, `mine`, `clearArea`, `harvestArea` | Real item-backed construction, mining, and bounded clearing. |
| Blueprints and ghosts | `createBlueprint`, `createBlueprintFromEntities`, `placeBlueprint`, `buildGhosts` | Export/import plans; `buildGhosts` is administrative revival. |
| Deconstruction | `deconstructArea`, `cancelDeconstructArea`, `deconstructEntities`, `cancelDeconstructEntities` | Robot orders or bounded instant mode. |
| Items and machines | `craft`, `cancelCraft`, `fuel`, `transfer`, `equip`, `equipGrid`, `pickup`, `pickupArea`, `beltTake`, `dropItem`, `setRecipe`, `clearRecipe`, `rotate`, `setDirection` | Crafting, inventory movement, recipe changes, line seeding, orientation. |
| Entity settings | `setSplitterSettings`, `setInserterSettings`, `setLampSettings`, `setContainerBar`, `setLogisticRequests`, `setActorLogisticRequests`, `setTrainStop`, `setEntityTags`, `setNameTag` | Configure production and logistics entities. |
| Circuits | `setCircuitBehavior`, `setCombinatorParameters`, `connectWire`, `disconnectWire` | Conditions, combinator logic, read modes, and red/green wiring. |
| Research | `startResearch`, `cancelResearch`, `queueResearch`, `clearResearchQueue`, `unlockAllTechnologies` | Normal research control; unlock-all is administrative. |
| Trains | `setTrainSchedule`, `trainControl` | Schedules, manual mode, dispatch. |
| Fluids | `setFluid` | Administrative direct fluid write. Normal play uses pumps and pipes. |
| Rocket | `setRocketSilo`, `launchRocket` | Auto-launch flag and explicit launch with engine reason on refusal. |
| Server/global | `setGameSpeed`, `announce`, `save`, `shutdown` | Global simulation and server control. |
| Measurement | `beltThroughputStart` | Start persistent lane crossing measurement. |

## Essential command recipes

Always run `describe` before copying an old example into a consequential mutation.

### Observe actor and surroundings

```graphql
{
  status { tick speed paused players { name surface position { x y } actor { character hasCharacter } } }
  characters(input:{surface:"nauvis", detachedOnly:false}) {
    pageInfo { total hasMore }
    characters { unitNumber player surface position { x y } }
  }
}
```

```graphql
{
  nearby(input:{surface:"nauvis", position:{x:0,y:0}, radius:40,
                order:DISTANCE, limit:100}) {
    pageInfo { total hasMore }
    entities { name type unitNumber position { x y } distance }
  }
}
```

### Find resources and legal build tiles

```graphql
{
  worldResources(input:{surface:"nauvis", force:"player",
    area:{leftTopX:-64,leftTopY:-64,rightBottomX:64,rightBottomY:64}, limit:100}) {
    pageInfo { total hasMore }
    worldEntries { kind name amount position { x y } nearbyDrills { unitNumber position { x y } } }
  }
}
```

```graphql
{
  probePlacement(input:{surface:"nauvis", force:"player",
    prototype:"transport-belt", direction:4,
    area:{leftTopX:0,leftTopY:0,rightBottomX:32,rightBottomY:4}}) {
    sampled placeable blocked
    blockedTiles { position { x y } blockers terrain tile }
  }
}
```

`probePlacement` checks engine collision at tile centers. It ignores inventory, research, actor reach, and throughput. Default `BLOCKED_TILES` mode returns blockers; `GRID` returns row-major `1`/`0` maps; `SITES` finds largest non-overlapping rectangles for requested dimensions. Probe each prototype separately because collision masks differ.

### Place, configure, and verify

```graphql
mutation {
  place(input:{actor:{character:"123"}, surface:"nauvis",
    position:{x:40.5,y:60.5}, item:"assembling-machine-1", direction:4}) {
    name unitNumber position { x y }
  }
}
```

```graphql
mutation {
  placeMany(input:{actor:{character:"123"}, surface:"nauvis",
    item:"transport-belt", placements:[
      {position:{x:41.5,y:60.5},direction:4},
      {position:{x:42.5,y:60.5},direction:4}
    ]}) {
    placed failed
    errors { index code message target { position { x y } } }
    entities { name unitNumber position { x y } }
  }
}
```

```graphql
mutation {
  setRecipe(input:{actor:{character:"123"},
    target:{unitNumber:"456"}, recipe:"iron-gear-wheel", drain:ABORT}) {
    name recipe unitNumber
  }
}
```

`setRecipe` supports `drain: ABORT | TO_ACTOR | SPILL`. Prefer `ABORT`; inspect and deliberately clear old materials before changing live production.

```graphql
mutation {
  transfer(input:{actor:{character:"123"}, target:{unitNumber:"456"},
    inventory:INPUT, direction:TO_ENTITY, item:"iron-plate", count:20}) {
    moved item inventory
  }
}
```

`InventoryKind` includes common machine, logistics, roboport, vehicle, and wagon inventories. Use `describe InventoryKind` for the current complete enum.

```graphql
{
  inspect(input:{actor:{character:"123"}, target:{unitNumber:"456"}}) {
    name type statusName recipe craftingProgress unitNumber
    inventories { input { name quality count } output { name quality count } modules { name count } }
    fluids { name amount }
    settings { splitter { inputPriority outputPriority filter { name quality comparator } } }
  }
}
```

### Configure logistics

```graphql
mutation {
  setSplitterSettings(input:{actor:{character:"123"}, target:{unitNumber:"700"},
    filter:{name:"iron-plate"}, outputPriority:"left"}) {
    splitter { filter { name quality comparator } inputPriority outputPriority }
  }
}
```

A splitter filter without output priority defaults to `left`; explicit `none` with a filter is rejected because it sorts nothing.

```graphql
mutation {
  setContainerBar(input:{actor:{character:"123"}, target:{unitNumber:"701"},
    inventory:"chest", bar:4}) {
    container { inventory bar capacity }
  }
}
```

Limit mall output chests. Unlimited mall production can consume all science intermediates.

### Research

```graphql
{
  researchInsights(input:{force:"player", status:AVAILABLE, limit:100}) {
    currentTechnology progress researchQueue
    pageInfo { total hasMore }
    technologies { name available prerequisites scienceIngredients { name amount } effects }
  }
  scienceBudget(force:"player", surface:"nauvis") {
    technology progress labCount missingPackLabCount
    sciencePacks { name nominalPacksLeft availableItems nominalShortfall }
  }
}
```

```graphql
mutation {
  startResearch(input:{force:"player", technology:"automation"}) {
    currentResearch researchQueue
  }
}
```

Use `queueResearch` only after prerequisites are researched. `researchInsights(unlocksRecipe:"...")` finds technology that unlocks a recipe.

### Diagnose flow

```graphql
{
  machines(input:{surface:"nauvis", force:"player", limit:100}) {
    pageInfo { total hasMore }
    summaries { type total working idle noFuel noPower noIngredients outputFull other }
    machines {
      name type unitNumber position { x y } status recipeName
      input { name count } output { name count } fuel { name count }
      diagnostics {
        blocked { category rawStatusName suggestedAction }
        shortfall { name requiredPerCraft available missing }
        throughput { theoreticalCraftsPerSecond }
        power { connected networkId satisfaction }
      }
    }
  }
}
```

```graphql
mutation {
  beltThroughputStart(input:{target:{unitNumber:"900"}, durationTicks:600, wait:true}) {
    id status elapsedTicks itemsCrossed itemsPerMinute
    lanes { lane itemsCrossed itemsPerMinute crossedItems { name quality itemsCrossed } }
  }
}
```

Measurement accepts normal transport-belt tiles, not splitters, underground belts, or loaders. Geometry changes invalidate sample. Zero flow is valid evidence.

## Gameplay task method

Use same closed loop for any build, production, research, or maintenance task:

1. **Establish baseline.** Query state needed to prove current condition: tick, actor, target identities, inventories, research, and relevant network state.
2. **Find limiting dependency.** Recipe, technology, item stock, legal space, power, input flow, or output capacity.
3. **Make smallest complete change.** Build or repair one functional unit, not disconnected fragments.
4. **Observe stabilization.** Factorio advances asynchronously. Wait or re-query until machine, belt, fluid, power, or research state proves effect.
5. **Verify result.** If proof fails, diagnose changed path before adding capacity.

Sections below explain how to perform each gameplay task. They are not a required linear sequence.

## How to survey and choose a build site

**Inputs:** required resource/prototype, approximate capacity, preferred surface, maximum travel distance, required footprint.

**Method:**

- Query `surfaces`, actor position, and existing factory bounds.
- Use `worldResources(groupPatches:true)` for resource searches. Compare patch `totalAmount`, bounds, centroid, water, obstacles, and nearby drills.
- Use `probePlacement(mode:SITES, siteWidth:..., siteHeight:...)` for rectangular blocks. Use `GRID` for route planning and `BLOCKED_TILES` for exact blockers.
- Query `worldLogistics`/`entities` around candidate area to detect existing structures.
- Check power-pole reach, belt/pipe route, and expansion space. Nearest legal site is not always cheapest connected site.
- Record chosen bounds and route endpoints before building.

**Proof:** candidate area contains required resource or footprint, collision probe reports usable site, and no existing structure conflict exists.

## How to obtain materials

**Inputs:** item, count, actor, whether hand crafting is allowed.

**Method:**

- Query `stock(item:...)` and actor `inventory`; existing accessible stock may satisfy requirement.
- Query `recipe` for actor craftability. If product has several recipes, use `itemRecipes` and select an enabled compatible recipe.
- For raw resources, find exact entity positions with `worldResources`, then use `mine`. Use `harvestArea` only for bounded trees/rocks.
- Use hand crafting only for bootstrap, recovery, or rare singletons. If demand repeats, automate the item instead. When immediate bootstrap completion is required, call `craft(wait:true)`; otherwise poll `queue`.
- For factory stock, use `transfer` from known container/output. Avoid emptying buffers needed by running production.
- Re-query actor inventory after mining, crafting, or transfer. Response count alone does not prove final inventory while queue and simulation advance.

**Proof:** actor or designated source contains required item/count at recorded tick.

**Failure handling:** if recipe is disabled, identify unlocking technology. If ingredients are missing, identify exact shortfall with `recipe`/`billOfMaterials`; avoid expanding unrelated production before proving dependency.

## How to build and grow a mall

A mall automates construction items so factory growth does not depend on repeated hand crafting. Start with items consumed often: belts, underground belts, splitters, inserters, electric mining drills, poles, assembling machines, furnaces, pipes, and chests.

**Method:**

- Establish automated plates, gears, and circuits before expanding the mall.
- Build one bounded production cell per product. One assembler is enough until measured demand proves otherwise.
- Feed cells from shared intermediate belts, direct insertion, or another explicit automated route.
- Limit every output with `setContainerBar`, a small buffer, or logistic request limits. Mall production must not consume all science intermediates.
- Add products when repeated hand crafting, manual transfers, or expansion shortages prove demand.
- Preserve lower-tier production when upgraded recipes consume lower-tier items. Add upgrade cells downstream instead of removing required inputs.
- Reserve a clear extension corridor. If the mall becomes cramped, build and verify a larger replacement before retiring old cells.
- Remove a known quantity from each output and verify automatic refill without actor transfers.

**Proof:** construction items refill automatically, buffers remain bounded, upstream materials stay supplied, and science or production does not starve.

See [Automation and mall strategy](references/automation-and-mall.md) for staged product choices, upgrade policy, layout, and verification.

## How to build one production cell

Use for gears, circuits, science packs, modules, rocket components, or any assembler/chemical-machine line.

**Inputs:** product, target rate or minimum working cell, source endpoints, output destination, area.

**Method:**

- Discover enabled producing recipe with `itemRecipes`; query recipe duration, ingredients, products, and compatible machine names.
- Use `billOfMaterials` for required batch. Use `itemFlow` and `stock` to identify existing upstream supply.
- Choose legal machine, inserter, belt/pipe, pole, and output positions with `probePlacement`.
- Obtain all build items before placement. Partial cells create confusing dead structures.
- Place machine; retain returned unit number. Apply `setRecipe`.
- Place inputs and outputs from endpoint toward machine. For inserters, direction is pickup side and drop is opposite; verify `pickupPosition` and `dropPosition`.
- Connect power. For fluids, inspect each box and `fluidboxNeighbours`; do not assume adjacent pipes joined correct port.
- Limit output chest with `setContainerBar` when cell feeds mall or bounded buffer.
- Seed only enough input to prove operation. Sustainable source must replace manual seed.

**Proof:** machine status leaves missing recipe/power state, crafting progress changes, output count rises, and output leaves machine when continuous flow is required.

**Failure handling:** inspect `diagnostics.blocked`, shortfall, inventories, fluids, power, inserter endpoints, and output capacity in that order. Never add second machine until first works.

## How to build or repair mining and smelting

**Inputs:** ore/product, target belt or destination, target rate, area.

**Method:**

- Select patch with `worldResources(groupPatches:true)`. Check `nearbyDrills` to avoid overlap.
- Probe drill placements across patch. Orient output toward belt or adjacent furnace; drill output needs no inserter.
- Build smallest drill row that reaches destination, then power and verify one drill before bulk placement.
- For smelting, confirm furnace recipe behavior by inserting ore into input; furnaces auto-select recipe from contents.
- Build ore input, furnace fuel/power, and plate output as separate observable edges.
- Use `stock` and belt reads to distinguish patch production from accumulated ore.
- Expand only after measuring destination flow or showing sustained machine activity.

Base-game sizing hints: yellow belt carries 15 items/s; electric drill produces about 0.5 common ore/s; about 30 drills, 48 stone furnaces, or 24 steel/electric furnaces can saturate one yellow plate belt. Treat these as starting estimates; save/mod/quality and real downtime win.

**Proof:** drills have mining targets and progress; furnaces craft continuously; plate output rises over time; belt measurement meets specified rate when rate matters.

## How to build or repair power

**Inputs:** required extra power or affected consumers, fuel source, water access, area.

**Method:**

- Query `electricNetworks` and `powerConsumers` around affected factory. Identify disconnected consumers separately from generation shortage.
- For steam, find water edge, probe pump/boiler/engine positions, and preserve correct fluid adjacency.
- Build one complete chain: offshore pump to water pipe/boiler, boiler to steam engine, fuel delivery, then poles.
- Power fuel inserters carefully. Keep burner inserter or direct coal path able to restart cold system.
- Verify boiler fuel, water/steam boxes, engine network, and consumer connection before cloning block.
- For solar/accumulators, evaluate full day/night behavior; immediate daytime production is insufficient proof.
- Re-query network production/consumption after connecting load. Do not infer satisfaction from stored machine energy.

**Proof:** target consumers report connected power and leave no-power states; network production supports observed load; steam block has continuous fuel and fluid.

## How to route belts, inserters, and pipes

**Inputs:** source endpoint, sink endpoint, item/fluid, required rate, allowed corridor.

**Method:**

- Read endpoint positions and directions with `inspect`, `worldLogistics`, or `factoryGraph`.
- Probe corridor for exact prototype. Belts, pipes, underground variants, and poles have different collision rules.
- Place route from known endpoint in short chunks. After each turn, inspect structural connection.
- Verify inserter pickup/drop positions. Rotate or replace wrong inserter; visual intuition is unreliable.
- For splitters, configure filter and priorities with `setSplitterSettings`; placement alone does not set routing policy.
- For pipes, inspect `fluidboxNeighbours` and fluid names. Never connect two different fluids.
- Seed belt with `dropItem(target:BELT)` only for testing or startup, not as supply design.
- Use `beltRun` for structure and `beltThroughputStart` for rate. Belt fullness alone is not throughput.

**Proof:** source and sink belong to intended topology, correct item/fluid reaches sink, and measured rate meets required throughput when specified.

## How to diagnose and repair a stalled factory

**Inputs:** failed product, machine, area, or symptom.

**Method:**

- Capture baseline tick, machine status, recipe, inventories, fluids, settings, and power.
- Check in this order:
  1. missing recipe or disabled machine;
  2. power/fuel;
  3. ingredient shortfall;
  4. inserter endpoint/direction;
  5. belt lane or fluid connectivity;
  6. output full;
  7. upstream producer rate.
- Use `stock` to find where item exists. Use `itemFlow` to find configured producers/consumers. Use `factoryGraph`/`beltRun` to prove connection. Measure only after structure is correct.
- Repair smallest root cause. Removing output blockage is better than adding upstream machines.
- Compare same fields after stabilization.

Interpretation:

- `no_fuel`: fuel delivery or burner restart.
- `no_power`, `low_power`, not connected: poles or generation.
- `no_ingredients`: upstream production, routing, inserter, lane, or fluid.
- `output_full`: downstream removal, storage, or wrong output route.
- Full long belt: stored buffer, not sustainable supply.

**Proof:** original stalled machine works or exact unresolved blocker is demonstrated. Record cause and changed entity IDs.

## How to run research and science

**Inputs:** requested technology, unlocked capability, or science bottleneck.

**Method:**

- Find unlocking technology with `researchInsights(unlocksRecipe:...)` or inspect exact technology.
- Verify prerequisites and `available`. Do not queue unavailable technology.
- Use `scienceBudget` to identify nominal pack shortfall and labs missing packs.
- For each missing pack, discover actual recipe and repair/build one production cell using method above.
- Feed every required pack to labs. Inspect `missingSciencePacks` on lab and inserter endpoints.
- Start or queue research only after checking current research and queue. Avoid canceling useful progress accidentally.
- Sample progress at two ticks. Pack inventory without increasing progress is not proof.

**Proof:** requested technology is active/completed, progress increases, or required recipe becomes enabled.

## How to build oil and fluid production

**Inputs:** requested fluid/product, source patch, destination, target rate, area.

**Method:**

- Discover enabled refinery/chemical recipe and all fluid inputs/outputs.
- Survey crude patch and water route. Build and verify one pumpjack.
- Place refinery/chemical plant with room for every fluid connection. Inspect fluidbox neighbours after each pipe connection.
- Keep each connected segment single-fluid. A wrong connection can contaminate or block whole segment.
- Provide storage/consumption for every refinery output; one full output can stop entire recipe.
- Reserve heavy oil for lubricant and light oil for solid/rocket fuel before cracking surplus.
- Use `fluidStock` plus machine fluid boxes to locate fluid. Structural pipe connection alone does not prove supply.
- Use pumps/circuit control when flow priority matters. Avoid `setFluid`; it is administrative, not production.

**Proof:** source amount decreases or produced fluid rises, target machine receives correct fluid, and final product increases over time.

## How to use blueprints and construction robots

**Inputs:** source layout or explicit entity plan, target origin, area, construction mode.

**Method:**

- Export known-good layout with `createBlueprint` or construct explicit plan with `createBlueprintFromEntities`.
- Probe target footprint before placement.
- `placeBlueprint` creates ghosts only. Query `ghosts` for materials and placement alternatives.
- For normal play, verify roboport coverage, construction robot availability, and logistic material stock with `logisticNetworks`.
- Poll `ghosts` and `constructionOrders` until required ghosts disappear and real entities inspect correctly.
- Use `buildGhosts` only for explicitly chosen sandbox play. It bypasses materials and robots.

**Proof:** real entities, not ghosts, occupy target; recipe/settings and connections match intended layout.

## How to configure entity settings, circuits, and trains

**Inputs:** target unit number, desired behavior, connected endpoints, expected signal/train outcome.

**Method:**

- Read `inspect.settings` before writing. Preserve unrelated settings.
- Use focused setter: splitter, inserter, lamp, container bar, logistic requests, train stop, circuit behavior, combinator parameters, or wires.
- Read settings back in same entity and verify exact values.
- For circuits, inspect `circuitNetwork` after wiring and signal-producing configuration.
- For trains, query current schedule/cargo/manual state. Replace schedule deliberately; then use `trainControl` and observe movement/state.
- Never guess connector names or control fields; use `describe` and engine-supported values.

**Proof:** readback matches requested settings and downstream behavior changes as intended.

## How to complete rocket work

Rocket work divides into ingredient production, silo construction, launch diagnosis, and launch.

**Ingredient production:**

- Discover live recipes for `rocket-part`, `rocket-silo`, `low-density-structure`, `processing-unit`, `rocket-fuel`, and any save-specific substitutes.
- Use `billOfMaterials` for requested batch and existing stock.
- Build or repair chosen ingredient line with production-cell method.
- Prove sustained output and measure rate/stock.

**Silo construction:**

- Verify technology, silo item availability, legal powered footprint, and ingredient routes.
- Place silo, retain unit number, connect power and actual recipe ingredients.
- Do not transfer `rocket-part`; silo creates internal parts.
- Inspect silo settings and status after stabilization.

**Launch:**

- Read `rocketParts`, `rocketSiloStatus`, `sendToOrbitAutomatically`, input/output inventories, crafting progress, and save mode.
- Treat engine silo status as readiness authority. Space Age can report ready below remembered part count.
- Use `setRocketSilo` for auto-launch policy or `launchRocket` for explicit launch:

```graphql
mutation {
  launchRocket(input:{actor:{character:"123"}, target:{unitNumber:"999"}}) {
    rocketSilo { rocketParts rocketSiloStatus sendToOrbitAutomatically }
  }
}
```

- On refusal, stop. Capture exact reason and current silo state. Never retry until state changes or first attempt is known.
- Verify launch through post-launch silo state/progress and relevant production/world evidence. Mutation success alone is insufficient.

Base game and Space Age launch semantics differ. Never hard-code landing-pad, satellite, cargo, or platform prerequisites without live evidence.

## Factory capability map

Choose work from current bottleneck rather than following a fixed progression list.

| Missing capability | Useful gameplay tasks |
|---|---|
| No controlled character | Restore/spawn character; transfer required inventory. |
| No raw supply | Survey patch; build one mining export; measure output. |
| Plate shortage | Repair ore route; add powered smelting block; clear output blockage. |
| Power faults | Diagnose network; build restart-safe generation block; reconnect consumers. |
| Missing intermediate | Build one production cell and prove sustained output. |
| Repeated hand crafting | Build or grow a bounded mall; automate common construction items; verify refill. |
| Research stalled | Identify missing pack; repair pack line/lab feed; start available technology. |
| Fluid shortage | Trace fluid stock/connectivity; repair one source/refining/cracking path. |
| Construction backlog | Supply materials/robots; extend roboport coverage; verify ghost completion. |
| Rocket blocked | Produce missing ingredient; repair silo feed/power; diagnose readiness; launch. |

Victory condition remains verified rocket launch.

## Layout, main bus, and factory evolution

- Build in modules: mining, smelting, intermediates, science, mall, oil, and power.
- Choose an explicit material-flow plan. A main bus works well for common recurring materials, but it is a strategy, not a requirement.
- Put broadly consumed materials on a bus; keep low-volume products and stable adjacent producer-consumer pairs local.
- Select one bus extension direction and keep it unobstructed. Reserve lanes, branch space, belt and pipe crossings, power corridors, roboports, and future train approaches.
- Leave expansion room beside each block. Mid-game assemblers, fluids, upgraded logistics, modules, and beacons need more space than bootstrap layouts.
- Four belt lanes with two empty tiles between groups is a useful bus convention, not a requirement.
- Use splitter priority only after configuring it with `setSplitterSettings` and verifying output.
- Do not add balancers by habit. Measure lane starvation first.
- Chests are useful bounded outputs and train buffers. Chests manually feeding core production hide unsustainable supply.
- Use both belt lanes intentionally. Verify lane contents.
- Keep power capacity ahead of expansion. Power faults often imitate logistics faults.
- Build one working production slice before cloning a larger block.
- Evolve with parallel cutovers: build and verify replacement beside live production, transfer consumers in bounded steps, then remove obsolete structures.
- Upgrade logistics only when throughput, reach, or operating cost justifies it. Faster belts do not fix insufficient production or blocked machines.
- Prefer several small verified builds over one huge unverified blueprint.
- Blueprints place ghosts only. Normal completion needs materials, construction robots, coverage, and available robot capacity.
- Set chest bars and logistic requests to prevent unbounded stockpiles.
- Use trains only when distance or throughput warrants their operational cost. Verify schedule, station names, mode, fuel, cargo, signaling, and unloading capacity.

See [Layout, main bus, and factory evolution](references/layout-and-factory-evolution.md) for stage-specific layout strategy, modular interfaces, transport choices, and safe migration steps.

## Common traps

- Old skill examples may use flat `player/x/y/entity` inputs. Current API uses `actor`, `target`, and `position` shared inputs.
- Old examples may use string directions. Current placement uses integer engine directions.
- Old examples may use lowercase transfer enums. Current GraphQL enums are uppercase.
- Old `announce` Boolean is gone. Successful actions announce; `message` overrides text.
- Reach is permissive by default, not enforced by default.
- `nearby` is deterministic and paginated, not unsorted and capped at 100 total.
- `setRecipe` supports drain modes and recipe quality. Furnaces still choose recipe from inserted input; use actual transfer and verify.
- `placeBlueprint` creates ghosts and consumes no items.
- `buildGhosts` revives ghosts without consuming materials or robots; it is a sandbox shortcut.
- `clearArea` destroys trees/rocks without drops. `harvestArea` mines with drops.
- `deconstruct*` creates robot orders unless area mutation uses explicit instant mode.
- `setFluid` writes fluid directly and is not normal logistics.
- `worldPlacement`/`probePlacement` prove collision legality only.
- Empty or full belts are buffers, not measured rates.
- `stock`, `scienceBudget`, and `ghosts.buildable` have documented scope limits.
- `launchRocket` exists. Never claim silo can launch only through auto-launch.
- Rocket readiness comes from engine silo status and launch result, not remembered part count.

