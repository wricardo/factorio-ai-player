# Factorio AI Player — Agent Handbook

You are an AI assistant playing Factorio through the GraphQL interface exposed by the
`factorio-ai-player` repository. Your mission is to **launch a rocket**. This file is your
persistent memory: read it at the start of every session, and **update it as you play**.

This file lives in `play/`. The repository root is the parent directory.

---

## 1. Mission and success condition

- **Primary goal:** build and launch a rocket (rocket silo built, its required rocket parts produced,
  rocket launched).
- The save is Factorio 2.0 with Space Age enabled. A rocket launch is the requested milestone;
  do not assume it ends the game. Verify actual recipe/tech requirements in-game instead of
  relying on memory.
- There is **no explicit launch mutation**. A rocket silo launches automatically once it has
  received the required rocket parts. Build the silo, power it, and feed its input inventory
  with `transfer`; the silo crafts `rocket-part` itself from processing units, low density
  structures and rocket fuel (one input slot each), and launches when its part counter is full
  and the engine status is `waiting_to_launch_rocket`; watch it with `inspect.settings.rocketSilo`.
- Work incrementally. Each session must leave the factory measurably closer to the goal.

## 2. Ground rules

1. **Never guess game state.** Query it. Production, inventories, positions, research, and
   machine status are all observable.
2. **Never retry a failed mutation blindly.** Mutations are not transactional; an error does not
   prove the action did not run. Inspect the world first.
3. **The server always simulates**, even with no client connected. Fuel burns, biters attack,
   and crafting progresses. Do not pause work assuming the world is frozen.
4. **Most entity interactions need reach.** The player `wricardo` exists in the save. Selecting
   `{player: ...}` does not bypass distance checks: `place`, `placeMany`, `placeTile`, `fuel`,
   `transfer`, `mine`, `remove`, `removeMany`, `rotate`, `setRecipe`, `beltTake`, `pickup`,
   `pickupArea`, and `harvestArea` resolve the acting character (the player's current character
   for a player actor) and enforce reach; `placeMany` and `placeTile` check every requested
   position. `craft`, `equip`, blueprint creation, and `beltThroughputStart` need no build reach.
   To act remotely you must first move the character with `navigateStart` (character unit **25** in
   this save). Read-only queries need no reach.
5. **Keep the save safe.** Do not delete unrelated structures, do not empty shared chests, and do
   not stop the server unless a save/restart is actually needed. Back up before risky changes.
6. **Measure before and after.** For every change, capture a query proving the new state
   (inventory count, machine status, production rate, research progress).
7. **Record only verified learnings** in this file, with the command/result that proved them.
   Mark uncertain conclusions as `[INFERENCE]`.
8. **Stop and raise anything that looks wrong.** If a GraphQL query or mutation, a Lua error, an
   RCON failure, or a gameplay outcome contradicts the schema/docs — unexpected error, null or
   default where data is promised, mutation reports success but state is unchanged, machine
   status that cannot be true — halt that workstream and report to the user: the exact command
   or GraphQL document, variables, tick, observed output, and expected result. Do not silently
   work around it, guess a fix, or blind-retry mutations.
9. **Report valuable GraphQL gaps.** While playing, when the API makes a real task awkward —
   missing read or relationship, ambiguous semantics/defaults, per-entity RCON fan-out, needing
   a detached character or reach for unrelated work — record a concise proposal for the user:
   the need, the workaround used, the suggested field/input/behavior, and rough cost. Do not
   implement API changes mid-play unless asked.

## 3. Environment and commands

### Server lifecycle (run from repository root)

```sh
just start              # start the managed RCON server
just observe            # launch a client to watch
just save               # request an asynchronous save
just stop               # save and shut down cleanly
just logs               # inspect the server log
```

The managed save is `~/Library/Application Support/factorio/saves/ai-player.zip` and the RCON
password is in `.factorio/password`. Default RCON address: `127.0.0.1:27015`.

### GraphQL invocation

Run commands from the repository root (the parent of `play/`). Every invocation needs the RCON
password:

```sh
cd ..
export FACTORIO_RCON_PASSWORD="$(cat .factorio/password)"
go run . query '{ status { tick players { name position { x y } } } }'
go run . mutation 'mutation { save { ... } }'
```

Useful discovery commands:

```sh
go run . queries --args --desc
go run . mutations --args --desc
go run . types --filter Input
go run . describe CraftInput --desc
go run . describe Query.machines --args
```

Execution options:

- `-q/--query` or positional document; `mutation` for writes.
- `-v/--variables '{"x":1}'` or `--var-file`.
- `--format json` for machine-readable output; default `toon` is token-efficient.
  (`-f` means `--file`, not `--format`.)
- There is no `--jq` flag. Pipe JSON output to external `jq`, or filter inside a script.
- This project runs GraphQL in-process over RCON. Use `go run .` for one-off calls and
  `go run . script` for GraphQL-only loops, branching, and dependent operations; shell/Python
  remain appropriate for non-GraphQL work or processes outside the game.
- If `go run .` reports a build error, the checkout is mid-edit. Run `go build ./...` from the
  repository root and report it rather than guessing; do not "fix" unrelated files silently.

### Scripts

Use `go run . script` (`js`) to run JavaScript against the same in-process schema. Each GraphQL
call still goes over RCON, but the script can keep intermediate results and branch on them
without shell re-encoding.

```sh
# Inline: return status data after one query
go run . script --source 'async function run(gql) {
  const res = await gql.query("{ status { tick players { name } } }")
  return res.data.status
}'

# File with an input object
go run . script --file scripts/check_machines.js --arg '{"limit":20}'

# Saved script under .gqlcli.json "scripts"
go run . script --op inspect-stock
```

Script signature: `function run(gql, input)` or `async function run(gql, input)`. Helpers:

- `gql.query(document, variables?, operationName?)`
- `gql.mutation(document, variables?, operationName?)`
- `gql.request({ type, query|mutation, variables, operationName })`
- `gql.each(items, worker, { concurrency?, stopOnError?, onError? })`

`gql.query` and `gql.mutation` return the full GraphQL envelope, so read `res.data...`. The
script's return value is printed as indented JSON; `console.log`/`console.error` also write to
command output. Flags: `--file/-f`, `--source`, `--op`, `--function`, `--arg`, `--arg-file`,
`--output/-o`.

Scripts can execute mutations. They do **not** make a workflow transactional: inspect before
retrying after an error or lost response. Keep loops bounded and prefer small batches so a
failure is easy to localize.

### Actor selection

Almost every operation takes `ActorInput`:

```graphql
input ActorInput { character: ID  player: String }
```

Select exactly one. Use `{ player: "wricardo" }` while the player is connected/available.
Use `{ character: "<unit-number>" }` for a detached character created by `characterCreate`.
Character IDs are strings; they are saved with the map but are not player names.

`setRecipe`, `rotate`, `pickup`, `pickupArea`, and `navigateStart` require a
character actor. `beltTake` accepts either a player or a character, like the placement
mutations. `characterCreate` only works while the player is disconnected; it moves the
player's main inventory into a detached character. Use `characterReturn` to restore that main
inventory and destroy the character. A connected player's character id is not exposed directly;
find it with `nearby` at the player position (`type: "character"`).

### Common reads

```graphql
# Tick and connected players
{ status { tick players { name surface position { x y } } } }

# Actor + position + inventory
{
  player(input: {player: "wricardo"}) {
    connected hasCharacter character position { x y } health force
  }
  inventory(input: {player: "wricardo"}) { items { name quality count } }
}

# Nearby entities (radius 1-100; capped at 100, unsorted)
{
  nearby(input: {x: 48.5, y: 57, surface: "nauvis", radius: 30}) {
    truncated
    entities { name type position { x y } }
  }
}

# Machines with status and inventories
{
  machines(input: {force: "player", surface: "nauvis", limit: 100}) {
    total
    machines {
      name type position { x y } status recipeName
      input { name count } output { name count } fuel { name count }
      craftingProgress energy
    }
  }
}

# Production/consumption over a window
{
  factoryOverview(input: {force: "player", surface: "nauvis", window: ONE_MINUTE}) {
    productionTick machineTick
    production { item produced consumed producedPerMinute consumedPerMinute }
    machines { type total working idle noFuel noPower noIngredients outputFull other }
  }
}

# Research
{
  researchInsights(input: {force: "player"}) {
    researchEnabled progress currentTechnology researchQueue
    technologies { name researched enabled available level scienceIngredients { name amount } }
  }
}

# Recipe and craftability
{ recipe(input: {player: "wricardo", recipe: "iron-gear-wheel"}) { enabled craftableCount ingredients { name count } products { name count } } }
{ itemRecipes(input: {force: "player", item: "rocket-part", limit: 10}) { total recipes { name category ingredients { name count } craftingMachines } } }

# Stock (belts, chests, machines, ground); totals have their own pagination
{ stock(input: {force: "player", surface: "nauvis", item: "iron-plate", limit: 100,
                totalsLimit: 100, totalsOffset: 0}) {
    tick total totalsTotal
    totals { item quality category count }
    locations { category entityName entityType position { x y } inventoryKind stack { name quality count } }
} }

# Simple area scans: every entity, or transport belts with both lanes
{ entities(input: {area: {leftTopX: 28, leftTopY: 50, rightBottomX: 60, rightBottomY: 75},
                   name: "iron-ore", limit: 20}) {
    total entities { name type position { x y } unitNumber }
} }
{ belts(input: {area: {leftTopX: 28, leftTopY: 50, rightBottomX: 60, rightBottomY: 75}, limit: 20}) {
    total belts { position { x y } direction line1 { items { name count } } line2 { items { name count } } }
} }

# Placement routing: engine legality for one prototype across every tile of an area
# (<=16384 tiles, one round trip); returns only blocked tiles.
{ probePlacement(input: {prototype: "transport-belt",
                         area: {leftTopX: 28, leftTopY: 48, rightBottomX: 60, rightBottomY: 52}}) {
    sampled placeable blocked
    blockedTiles { position { x y } blockers terrain tile }
} }

# Topology: directed belt component from a seed belt; structural, not throughput
{ beltRun(surface: "nauvis", x: 44.5, y: 50.5, maxEntities: 100) {
    truncated nodes { name position { x y } } edges { kind boundary }
    sources { name position { x y } } sinks { name position { x y } }
    inserters { inserter { name } pickup { name } drop { name } }
} }
{ factoryGraph(area: {leftTopX: 44, leftTopY: 48, rightBottomX: 50, rightBottomY: 53}) {
    machines { name position { x y } } belts { name } chests { name }
    inserters { inserter { name } pickup { name } drop { name } }
    boundaryEdges { kind }
} }

# Planning and science
{ itemFlow(input: {item: "iron-ore", limit: 20}) {
    producerTotal consumerTotal truncated
    producers { name recipe status theoreticalRatePerSecond }
    consumers { name recipe status }
} }
{ billOfMaterials(input: {item: "rocket-part", amount: 100, stock: {source: FACTORY}}) {
    complete materials { item required availableStock missingStock }
    steps { recipe crafts } rawRequirements { item amount reason }
    unresolved { item amount reason } warnings
} }
{ scienceBudget { technology progress labCount missingPackLabCount
                  sciencePacks { name nominalPacksLeft availableItems nominalShortfall } } }
{ ghosts(input: {area: {leftTopX: 28, leftTopY: 50, rightBottomX: 60, rightBottomY: 75}, limit: 20}) {
    total ghostEntries { kind name position { x y } buildable
                         requestedMaterials { name count available missing } }
} }

# Per-machine diagnosis and inspection extensions (separate snapshots)
{ machines(input: {force: "player", surface: "nauvis", limit: 20}) {
    machines { name type status diagnostics {
      shortfall { name requiredPerCraft available missing }
      throughput { theoreticalCraftsPerSecond actualCraftsPerSecond }
      blocked { category rawStatusName suggestedAction }
      power { connected networkId satisfaction }
    } }
} }
{ inspect(input: {player: "wricardo", entity: "lab", x: 46.5, y: 53.5}) {
    missingSciencePacks { name required available missing supported }
    logistics { inserters { inserter { name } pickup { name } drop { name } } }
    fluidNetwork(maxBoxes: 100) { visitedBoxes truncated totalAmount totalCapacity }
} }
```

Check exact field names with `go run . describe <Type> --desc`; the schema is the authority.

### Common writes

```graphql
# Queue crafting; wait drains the whole queue (including older work)
mutation {
  craft(input: {player: "wricardo", recipe: "iron-gear-wheel", count: 20,
                wait: true, waitTimeoutSeconds: 120}) {
    started queueSize craftingComplete waitTimedOut waitedSeconds
  }
}

# Mine one resource/tree/rock (reach required; instantaneous)
mutation { mine(input: {player: "wricardo", entity: "iron-ore", x: 32.5, y: 33.5}) { ... } }

# Place one normal-quality item from inventory (reach required; navigate first)
mutation { place(input: {player: "wricardo", item: "stone-furnace",
                         x: 40, y: 64, direction: "north", surface: "nauvis"}) { ... } }

# Move items between actor and machine/chest
mutation { transfer(input: {player: "wricardo", item: "iron-ore", count: 50,
                            entity: "stone-furnace", x: 40, y: 60,
                            inventory: "input", direction: "to-entity"}) { moved } }

# Fuel a furnace/drill/boiler
mutation { fuel(input: {player: "wricardo", entity: "stone-furnace", x: 40, y: 60,
                        item: "coal", count: 10}) { ... } }

# Configure an idle, empty assembling machine (character actor only; see Actor selection)
mutation { setRecipe(input: {character: "123", entity: "assembling-machine-1",
                             x: 46.5, y: 60.5, recipe: "iron-gear-wheel"}) { ... } }

# Research (must be enabled with prerequisites met)
mutation { startResearch(input: {force: "player", technology: "automation-science-pack"}) { ... } }

# Batch placement; the acting character (player's current character) must be in build reach
mutation { placeMany(input: {player: "wricardo", item: "transport-belt",
                             placements: [{x: 40.5, y: 70.5, direction: "east"},
                                          {x: 41.5, y: 70.5, direction: "east"}]}) {
    placed failed errors
} }
mutation { placeTile(input: {player: "wricardo", item: "landfill", x: 40, y: 64}) {
    tile position { x y }
} }

# Character equipment: inventory guns/ammo/armor/trash; direction to-equipment/to-main
mutation { equip(input: {player: "wricardo", item: "iron-plate", count: 1,
                         inventory: "trash", direction: "to-equipment"}) { ... } }

# Bulk removal: explicit reachable targets, at most 200
mutation { removeMany(input: {player: "wricardo", removals: [
                            {entity: "tree", x: 40, y: 70},
                            {entity: "tree", x: 41, y: 70}]}) {
    removed failed errors
} }

# Blueprints: creation does not touch the world; placement creates ghosts only.
mutation { createBlueprint(input: {area: {leftTopX: 44, leftTopY: 48,
                                          rightBottomX: 50, rightBottomY: 53},
                                  includeTiles: false}) { entityCount blueprint } }
mutation { createBlueprintFromEntities(input: {entities: [
                                  {name: "transport-belt", x: 0, y: 0, direction: "east"}]}) {
    entityCount tileCount blueprint
} }
mutation { placeBlueprint(input: {blueprint: "<string from createBlueprint>",
                                  x: 40, y: 70, direction: "north"}) { ghosts } }

# Real belt throughput: mod protocol 1, Factorio >= 2.0.76, no message option
mutation { beltThroughputStart(input: {x: 44.5, y: 50.5, durationTicks: 600, wait: true}) {
    id status itemsCrossed itemsPerMinute
    lanes { lane itemsCrossed itemsPerMinute crossedItems { name quality itemsCrossed } }
} }
# Later: { beltThroughput(id: "<id>") { ... } }
```

`clearArea`, `harvestArea`, `pickupArea`, `entities`, `belts`, `ghosts`, `factoryGraph`,
`createBlueprint`, and BOM stock areas accept at most 256 tiles per side; `beltRun` and
`beltRuns` cap traversal nodes at 1–1000. `inspect` also supports `logistics`,
`missingSciencePacks`, and `fluidNetwork`; `machines` supports `diagnostics`.

### New capabilities (2026-10-30, verified live on an isolated 2.0.77 copy)

```graphql
# Agent efficiency (sandbox server knobs)
mutation { setGameSpeed(input:{speed:5}) { tick speed paused } }      # 0.1..1000; paused optional
mutation { teleport(input:{character:"267",x:40,y:60}) { position { x y } } }

# Entities 8-way direction and rails: directionCode wins over direction
mutation { place(input:{character:"267",x:200,y:210,item:"rail",directionCode:2}) { direction } }
mutation { setDirection(input:{character:"267",unitNumber:"42",entity:"straight-rail",direction:2}) { direction } }
# setDirection verifies readback and refuses when the engine ignores it (belts/rails) - place with directionCode instead.

# Blueprints that actually build (script revival bypasses bots and items - sandbox shortcut)
mutation { buildGhosts(input:{character:"267",surface:"nauvis",area:{...},mode:REVIVE}) { revived entities { unitNumber name } missing { name count } } }

# Bulk teardown with filters; instant mines reachable targets and reports recovered items
mutation { deconstructArea(input:{character:"267",area:{...},names:["transport-belt"],instant:true}) { removed items { name count } } }

# Fluids: SET clamps to capacity, ADD reports accepted amount, CLEAR empties
mutation { setFluid(input:{character:"267",entity:"pipe",unitNumber:"42",mode:SET,fluid:"water",amount:100}) { before after capacity } }
query { fluidStock(input:{area:{...}}) { totals { fluid amount } locations { entityName fluid amount } } }

# Item writes, quality, fast replace, typed inventories
mutation { dropItem(input:{character:"267",item:"iron-plate",count:5,x:40,y:60,target:GROUND}) { moved } }
mutation { place(input:{character:"267",x:40.5,y:60.5,item:"transport-belt",quality:"uncommon",directionCode:4,fastReplace:true}) { unitNumber } }
mutation { transfer(input:{character:"267",entity:"wooden-chest",unitNumber:"42",inventoryKind:CHEST,direction:"to-entity",item:"iron-plate",quality:"uncommon",count:5}) { moved } }
query { inspect(input:{character:"267",unitNumber:"42",entity:"assembling-machine-1"}) { unitNumber parametersJSON nameTag beltNeighbours { unitNumber } } }
mutation { setNameTag(input:{character:"267",unitNumber:"42",entity:"wooden-chest",nameTag:"mall-feed"}) { nameTag } }
# setEntityTags fails on normal entities in 2.0.77 (tags are blueprint/ghost metadata) - do not rely on it.

# Recipes: drain input/output before changing; clearRecipe drains to the actor
mutation { setRecipe(input:{character:"267",unitNumber:"42",entity:"assembling-machine-1",recipe:"iron-stick",drain:TO_ACTOR}) { recipe returned { name count } } }
mutation { clearRecipe(input:{character:"267",unitNumber:"42",entity:"assembling-machine-1"}) { recipe returned { name count } } }

# Trains (rails still need directionCode for diagonals/junctions)
query { trains(input:{surface:"nauvis"}) { total trains { id statusName manualMode schedule { station waitSeconds } cargo { name count } } } }
mutation { setTrainSchedule(input:{character:"267",unitNumber:"1499",entity:"locomotive",records:[{station:"mine",waitSeconds:10},{station:"base",waitSeconds:0}]}) { id schedule { station } } }
mutation { trainControl(input:{character:"267",unitNumber:"1499",entity:"locomotive",manualMode:false}) { statusName } }

# Character gear, actor logistics, research
mutation { equip(input:{character:"267",item:"power-armor",inventory:"armor",direction:"to-equipment"}) { moved } }
mutation { equipGrid(input:{character:"267",item:"exoskeleton-equipment",count:2,direction:TO_GRID}) { moved items { name count } } }
mutation { setActorLogisticRequests(input:{character:"267",sections:[{slots:[{index:1,name:"transport-belt",min:20,max:200}]}]}) { sections { slots { name min max } } } }
mutation { queueResearch(input:{force:"player",technology:"logistics"}) { currentResearch researchQueue } }
mutation { unlockAllTechnologies(input:{force:"player",mode:RESEARCH}) { technologies } }   # sandbox cheat

# World/read additions
query { surfaces(force:"player") { name index planet totalPollution evolutionFactor } }
query { circuitNetwork(input:{unitNumber:"42",entity:"inserter",color:"red"}) { connected signals { name value } } }
query { safety { evolutionFactor totalPollution killCounts { name count } } }
query { nearby(input:{x:0,y:0,radius:30,order:DISTANCE,limit:20,offset:0}) { total entities { name distance unitNumber } } }
query { logisticNetworks { networks { id cells constructionRobots { total available } contents { name count } } } }
```

Player-only actions: `useCapsule` and `shoot` need a connected player controlling the character;
`shoot` also needs the updated mod loaded (save reload). `repair` is a scripted step that consumes
repair packs (300 HP each in 2.0.77).

## 4. Playing well

### Bootstrap priorities (early game)

1. **Power and fuel:** ensure boilers/steam engines are fueled and connected; check
   `electricNetworks` and `powerConsumers` for unsatisfied demand.
2. **Iron and copper plates:** feed every furnace; fix `no_fuel` and `no_ingredients` first.
3. **Automation science:** automate red science (iron gear + copper plate into
   automation-science-pack), feed labs, run research continuously.
4. **Belts and inserters:** automate transport belts and inserter production before scaling.
5. **Defense:** monitor `safety`; repair/replace damaged entities and keep ammunition/fuel
   supplied. Biters evolve while you build.

### Mid game

- Research logistics and get construction robots if available; otherwise place by hand.
- Build green science (transport belt + inserter), then military/chemical science.
- Establish oil processing, plastic, advanced circuits, and modules.
- Keep power ahead of demand; brownouts masquerade as logic bugs.

### Rocket path

- Discover the actual requirements from the schema instead of assuming:
  `itemRecipes` for `rocket-part`, `rocket-silo`, `processing-unit`, `low-density-structure`,
  `rocket-fuel`, `electric-engine`, and the `rocket-silo` technology.
- Produce rocket part ingredients at scale, craft rocket parts, and `transfer` them into the
  silo's input inventory. The silo launches automatically when full.
- Verify silo state with `inspect`. If the silo is unpowered or missing ingredients, production
  queries and `machines` will show why.

### Factory practices (required)

- **Never buffer coal in chests as a feed.** A chest that hands coal to inserters is a dead-end
  buffer: it hides the real supply rate, empties silently, and needs manual `transfer` top-ups.
  Coal for boilers and smelters MUST come from a **belt fed by coal mining** (electric drill →
  belt → consumer inserter), with chests used only as end-of-line storage where nothing depends
  on them. Fix an existing chest-fed coal line by replacing it with the belt, not by refilling.
- Coal is allowed **only** for boilers and smelters (stone furnaces). Every other machine —
  drills, inserters, assemblers, labs — must be electric. Convert burner drills and inserters to
  electric, and replace any chest-fed coal line with electric drill → belt → consumer inserter.
- Chest buffers in general should not be load-bearing: any chest that a production line consumes
  from needs a verified upstream belt and a consumer that outpaces it.

### Play loop

1. **Observe:** status, factory overview, research, inventory, safety.
2. **Choose the bottleneck:** usually missing ingredient, missing power, or missing research.
3. **Act:** one or a few mutations, in dependency order.
4. **Verify:** re-query the affected inventories/machines/production.
5. **Escalate:** any anomaly (ground rule 8) or API gap worth fixing (ground rule 9) stops the
   loop — report it to the user with the reproduction before continuing.
6. **Record:** update `Current state`, `Next actions`, and `Learnings` below.
7. **Repeat.** Do not idle; the factory can always be improved.

## 5. Updating this file (required)

After each meaningful action or session:

- Refresh **Current state** with the game tick and the facts you actually observed.
  Delete stale details rather than letting them rot.
- Keep **Next actions** as an ordered, concrete checklist; mark items done and move them.
- Append new **Learnings** as dated bullets. Each must state the fact, the evidence
  (query/mutation and observed result), and whether it is verified or `[INFERENCE]`.
  Remove a learning once disproven; correct it in place.
- Add open questions or risk notes under **Open questions**.
- Log unresolved anomalies and GraphQL feature requests under **Open questions** with the exact
  reproduction (command, variables, tick, observed vs expected) and raise them to the user in
  chat; do not bury a suspected bug in a learning bullet as if it were understood.
- Never delete the Mission, Ground rules, or command reference.
- Keep this file concise. This is operational memory, not a diary.

---

## 6. Current state

**Fresh sandbox map (2026-10-30 13:41) — previous world quit and archived. Mini mall built
2026-10-30, tick ~505k.**

- **Live re-check 2026-10-30 17:0x, tick ~736k (verified by `factoryOverview`/`machines`):** the
  world moved past the snapshot below. Player force has 8 assembling machines (the mall, every
  one `item_ingredient_shortage`), 1 stone furnace at `(33,51)` `no_fuel`, and **72
  electric-mining-drills mostly at `x=82.5, y≈15..35`** (`waiting_for_space_in_destination` /
  `no_minable_resources`) that this file does not describe — re-survey before planning.
  `MachineSummary.noPower` exists now (always 0 here); the only unpowered entity is a stray
  `long-handed-inserter` at `(-3.5,16.5)`, unit 485, outside any producing network.

- Server hosts `saves/ai-player-sandbox.zip`, created from the bundled `sandbox` scenario
  (throwaway instance on RCON 27016 → `save` → `shutdown` → file copied in → `just start`).
  Previous electrified base saved at `saves/ai-player-sandbox-20261030-electrified.zip`
  (16 stone furnaces, all-electric drills/inserters, iron-plate ~130/min, copper-plate ~130/min).
- Player `wricardo` = character **unit 267** (`Actor.character` reports `"267"`,
  `hasCharacter: true`). `researchEnabled: true`, no technology. RCON `127.0.0.1:27015`,
  password in `.factorio/password`.
- **Mini mall (chest-fed, verified): site `x -6..16, y 18..81`** — cleared, all 1344 tiles
  placeable (`probePlacement`). 7 slices, 8 rows tall, at `s = 20,29,38,47,56,65,74`.
  Template for machine `m` (tiles `x=1+5m..3+5m`): row `s` belt#1, row `s+1` belt#2,
  row `s+2` normal inserter `x=1+5m` (picks belt#2) plus long-handed `x=2+5m` and `x=3+5m`
  (pick belt#1 — plate is the limiting feed), machine 3x3 at `(2.5+5m, s+4.5)`, row `s+4`
  output inserter `x=4+5m` → chest `x=5+5m`, row `s+6` south inserter `x=2+5m` (picks belt#3),
  row `s+7` belt#3. Feed chests at `x=-2` (rows `s`, `s+1`, `s+7`) with feed inserters at `x=-1`
  dir `west`. Every belt row **ends exactly at its last tap tile** (`ing1 3+5m`, `ing2 1+5m`,
  `ing3 2+5m`) so items jam at the inserter.
- Slices/products: `20` transport-belt (plate, gear); `29` underground-belt (plate, belt);
  `38` splitter (plate, EC, belt); `47` inserter + electric-mining-drill (plate, gear, EC);
  `56` assembling-machine-1 (plate, gear, EC); `65` small-electric-pole (wood, cable);
  `74` medium-electric-pole (steel, stick, cable). 8 product machines, 45 inserters,
  11 long-handed, 26 chests (18 feed + 8 output).
- Power: `electric-energy-interface` 2x2 at `(-4..-3, 21..22)` (centre `(-3,22)`) + **30 medium
  electric poles** on rows `s+2`/`s+6` at tiles `x = 0,5,...,5N`. One network (id 2),
  production = consumption (~72 kW at partial load), 0 `no_power`.
- Mall map (one slice, `ch` source chest, `i` inserter, `LH` long-handed, `M` machine,
  `[]` belt; belts end at their tap tile):

```
tile x:  -2  -1   0   1   2   3   4   5
row s+0       ch   i  [====== belt#1 ======]
row s+1       ch   i  [====== belt#2 ==]              (ends at x=1)
row s+2                  i1     LH2 LH3
row s+3                [M    M    M]
row s+4                [M    M    M]   i4 --> [out chest]
row s+5                [M    M    M]
row s+6                   i5
row s+7       ch   i  [== belt#3 ==]                  (only for 3-input products)
```
- Verified rates (ONE_MINUTE window, chests at 600/800, tick ~590k): transport-belt **120/min**,
  inserter **60/min**, medium-pole **42/min**, UG-belt **34/min**, splitter **30/min**,
  AM1 **18/min**, drill **11/min**, small-pole 0 (no wood left). Product stock earlier:
  belt 768, UG 78, splitter 40, inserter 76, drill 12, AM1 22, small-pole 38, medium-pole 96.
- Source chests are filled to **600 each (belts 800, sticks 1200)**; at full inserter feed a
  600-plate chest still lasts only ~4 min (two long-handed inserters pull ~2.4 items/s), so the
  mall is **manual-feed for now** — next step is feeding these chests from production.
- Wood is the scarce input: `harvestArea` reach is tiny (6 trees → 6-19 wood), so the
  small-pole line only runs while wood lasts.
- **Iron field mining (built 2026-10-30, tick ~600k): patch `x 47..82, y 14..38`** (~740 ore
  tiles, no water, 1 rock cleared), just east of the mall. 72 `electric-mining-drill`s in
  6 rows × 12 columns (drill centres `x = 49.5+3k`, rows `y = 15.5/19.5/23.5/27.5/31.5/35.5`);
  rows `15.5/23.5/31.5` face `south` and rows `19.5/27.5/35.5` face `north`, i.e. every drill
  drops onto one of the three mining belts at rows **17, 25, 33** (24 drills per belt, 12 ore/s
  theoretical vs 15/s yellow capacity). 10 of the 72 sit on patch edge and report
  `no_minable_resources` (86% usable).
- 3 belts run **west** along their row to export lanes at `x = 18/19/20`, then **south** to
  `y = 83`, ending in `inserter` (dir north) → wooden chest at `(18.5/19.5/20.5, 85.5)`.
  366 belts total. Power: 2x2 `electric-energy-interface` at `(45,21)` + 20 medium poles on
  rows `13/21/29/37` at `x = 48/56/64/72/80` (network 3); the chest bank has its own 2x2 EEI at
  `(23,84)` + pole `(21,84)` (network 4) because the field network does not reach it.
- Belt fill measured (sum of ore ÷ 8 per tile): row 17 **93.5%**, row 25 **91.1%**, row 33
  **77.8%** (still equalising), with 48 drills blocked on `waiting_for_space_in_destination` —
  the expected "belts are full" signal. Transient ore rate ~495/min; steady state is limited to
  ~216/min by the bank's 3 inserters until smelting arrays are attached. 11 of 72 drills sit on
  patch edge and report `no_minable_resources`.
- **Ore sorter rig (built 2026-10-30, `y 86..92`, `x 14..25`):** because the API cannot set
  splitter or inserter filters, item-type routing is done with the *type-aware* `transfer`
  mutation. Geometry: intake belt `y=86` (x 14..21, east) → inserters `(18/19/20, 87)` dir north →
  buffer chests `(18.5/19.5/20.5, 88.5)`; sorting station stand `(21.5, 89.5)`; per-type chests
  `(15.5,90.5)`, `(19.5,90.5)`, `(23.5,90.5)`; inserters `(15/19/23, 91)` dir north → **per-type
  reroute belt stubs** `y=92` at `x 15..16`, `19..20`, `23..24` (separated so they never merge).
  Poles `(17,90)`, `(21,90)` sit on network 4 (bank EEI).
- Sorter verified end-to-end with a 100-item mixed load (iron-ore 60 + iron-gear-wheel 50 +
  electronic-circuit 40 spread over the three buffers): buffers ended empty, output chests held
  **only** their own type (52 / 42 / 32) plus the same single type on each belt stub
  (3–4 per tile), i.e. 3 clean segregated streams. Sorting a buffer needs one `transfer`
  `to-player` + one `to-entity` per item type; a character standing within reach of all chests.
- Sandbox semantics: the scenario strips the connecting player's character (god controller,
  `player.character == null`); `playerRestoreCharacter` or raw
  `/silent-command game.players["wricardo"].create_character()` fixes it. Player `craft` is
  **free** (still true at tick ~505k); a detached character's craft consumes ingredients.
  `place`/`placeMany`/`transfer` enforce the character's build reach (~10 tiles).
- Everything below this point describes the **previous** save; its coordinates are void, its
  learnings still hold.

### Previous save (archived)

Snapshot: 2026-10-30 12:24, tick ~1.13M. The running server hosts
`~/Library/Application Support/factorio/saves/ai-player-sandbox.zip` (see its command line),
**not** `saves/ai-player.zip`; RCON `127.0.0.1:27015`.

**Base is now all-electric (mining + furnace inserters) with one EEI power network.**

- Power: `electric-energy-interface` `(87,-60)` feeds **one** network (23 poles/substations,
  ~1.09 MW, production = consumption). Backbone: substations `(85,-60)`, `(89,-61)`, `(36,-78)`,
  `(22,-76)`, `(10,-74)`, `(10,-56)`, `(10,-42)`, `(10,-26)`, `(40,-92)` (iron patch),
  `(24,-62)`, `(30,-62)` (furnace area); big poles `(70,-62)`, `(55,-61)`, `(40,-68)`, `(24,-56)`.
  Electric drills add ~450 kW of the load; the EEI supplies on demand.
- Mining (all electric): 5 drills on the eastern iron patch (`y≈-97`), 6 on the copper strip
  `y=-34.5`, 4 on the coal patch `(x 22..35, y -78..-85)`. Coal 156/min peak, iron-ore
  ~130/min, copper-ore ~146/min — all demand-limited (25 drills sit `output_full`).
- Smelting: **16 stone furnaces** — iron row `y=-68` at `x=3,6,9,12,18,21,24,27,30`, copper row
  `y=-61` at `x=3,6,9,12,15,18,21`. All furnace inserters are **electric** (no `no_fuel`).
  Fuel design: iron row burns coal from lane `y=-71.5` via **long-handed** inserters in the
  `y=-69.5` row; copper row burns coal from lane `y=-64.5` (eastbound, fed from the `x=0.5`
  column) via long-handed inserters in the `y=-62.5` row. Ore lanes stay single-item:
  iron `y=-70.5` (west), copper `y=-63.5` (east).
- Rates (12:24, ONE_MINUTE window): iron-plate **131/min**, copper-plate **140/min**,
  iron-ore 130, copper-ore 146, coal 156. Baseline at 11:30 was 0 (everything stalled).
- Bus-end sinks: copper `(92.5,-57.5)`/`(92.5,-58.5)` are **iron chests** (3200 each); iron
  `(92.5,-64.5)`/`(92.5,-65.5)` are wooden chests holding plate **plus leftover coal**
  (605 + 260 and 872 + 199). All four served by electric inserters at `(91.5,*)`.
- Mixed lanes: 0. Iron bus lane `y=-64.5` must stay clean — do **not** place belts carrying
  other items there east of `x≈30` (see learnings).
- Dead/known-broken: 9 burner copper drills at `y=-17..-22` have no output belt (`no_fuel`,
  idle); 2 burner coal drills remain at `(31,-80)`, `(32,-76)` (still fueled, electric drills
  don't cover their 3x3); 2 iron furnaces report `no_ingredients` because upstream furnaces eat
  the ore first.
- Player inventory is nearly full (~60/80 slots, 2984 copper + 600 iron plates) — `pickupArea`
  with the player silently returns `itemsPickedUp: []`; use a detached character instead.

## 7. Next actions

- [x] Confirm actor setup on the fresh map: player `wricardo` has character unit 267; `player:`
      place/transfer/seed all verified working (2026-10-30).
- [x] Build the chest-fed mini mall (7 slices, 8 products) and verify every product flows into
      its output chest (2026-10-30, tick ~505k).
- [ ] **Feed the mall from production instead of by hand** (the standing bottleneck): a
      component stage above the mall (gear, iron-stick, copper-cable, EC assemblers) whose output
      inserters fill the mall's source chests, plus a plate/copper belt from smelting. Then only
      iron-plate, copper-plate, steel and wood stay hand-fed.
- [ ] Bootstrap real production: electric drill → stone furnace smelting on the iron patch inside
      the surveyed area (225 iron-ore entities in `-40,-5..60,75`), coal belt, then plates into
      the mall. Never hand-feed burners (previous save died twice that way).
- [ ] Add a wood supply (tree farm or a standing harvest run) so the small-pole slice is not the
      first line to starve.
- [ ] Extend the mall with more products using the same slice template (underground pipe, pipe,
      stone furnace, lamp, splitter/UG belts for other tiers) once feeds are automatic.
- [ ] Re-derive rocket requirements in-game with `itemRecipes`/`billOfMaterials` before scaling.

## 8. Learnings (verified)

<!-- Append dated bullets. Example:
- 2026-10-29 — `machines` reports electric drills with `waiting_for_space_in_destination`
  when their output backs up; adding a chest or belt lane fixes it. (verified: query before/after)
-->

- 2026-10-30 — **Mini mall slice template (verified live).** A 3x3 assembler can take two
  ingredients from belts above and a third from a belt below: `row s` belt#1, `row s+1` belt#2,
  `row s+2` normal inserter at the machine's left column (picks belt#2 one tile up) **and**
  long-handed inserter(s) in the same row (pick belt#1 two tiles up, drop two tiles down into the
  machine's middle row), machine at `rows s+3..s+5`, `row s+6` south inserter (picks belt#3 on
  `row s+7`), `row s+4` output inserter dir `west` → chest one tile east. Machines repeat at
  `x = 1+5m`. Verified: 8 product machines in 7 slices, all recipes set with `setRecipe` on
  character 267, all 8 outputs appeared in their chests.
- 2026-10-30 — **Long-handed inserter geometry, measured:** an inserter at tile centre
  `(-3.5,16.5)` dir `north` reported `pickupPosition (-3.5,14.5)` and
  `dropPosition (-3.5,18.7)` — exactly 2 tiles each way (it can also drop into a machine whose
  bounding box covers the 2-tiles-away tile). This is what makes the 3-input slice possible.
- 2026-10-30 — **A belt row must END at its last tap tile.** If the row continues past the
  inserter, items only stop at the far dead end, and once the machine's input buffer is satisfied
  the passing items are carried beyond the pickup tile — the machine later starves while the dead
  end holds stock (observed: wood sitting on tiles x=4,5 while the LH inserter tapped x=3, chest
  and machine empty). Trim each row to `ing1 3+5m`, `ing2 1+5m`, `ing3 2+5m`; removed tiles drop
  items, so `pickupArea` afterwards. (verified: 81 → 69 belt tiles, all 18 rows end at their tap)
- 2026-10-30 — **Medium pole coverage is tighter than assumed.** Poles on `x=0` left every
  slice's output inserter at tile `x=4` (`no_power`), which showed up as machines on
  `full_output` with empty output chests. Adding a pole column at `x=5` (and `x=10` for the wide
  slice) cleared all 7 `no_power` inserters. Check `powerConsumers.status`, not the chest.
- 2026-10-30 — **`placeMany` spends real inventory.** Seeding source chests (240 belts) inside the
  same script starved later slices' belt placement — every remaining placement failed with
  `actor lacks item`. Place everything first, seed afterwards, and re-check inventory between
  batches.
- 2026-10-30 — **`direction` on a mining drill = the side the ore is DROPPED to** (this
  supersedes the earlier "north drops south" note, which was wrong). Placing 72 drills with
  dir `north` on the row *north* of a belt dropped ore onto the ground behind them
  (rows 13/21/29/37) while the belts at 17/25/33 stayed empty; `rotate(input:{...turns:2})` on
  all 72 moved every drop onto its belt in one pass (belt ore 0 → 515/500/346 per row).
  Rule: a drill at row `beltY-1..beltY-3` needs dir `south`; one at `beltY+1..beltY+3` needs
  dir `north`. Verified with `machines.status`: blocked drills report
  `waiting_for_space_in_destination` only when their drop tile is on the belt.
- 2026-10-30 — **3x3 placement positions are snapped to the tile grid**: passing an
  already half-integer centre (`x=48.5`) lands the entity one tile further (`49.5`), and a
  half-integer `y` landed drills on the belt row so every north row failed with
  "cannot place entity at position". Pass the tile-centre coordinate directly (`48.5` for a
  drill spanning tiles 48..50 at rows 18..20 → centre `(49.5,19.5)`), never add another 0.5.
- 2026-10-30 — `placeMany` reports per-index failures (`placement N: <reason>`), so a batch can
  be retried selectively: parse the index, keep only the failed placements, and re-submit them
  from the next standing spot. That is how the 465-placement field was finished with 12
  navigations at ~10-tile build reach.
- 2026-10-30 — Belt fill ratio = (sum of `line1+line2` item counts over the row) / (8 × tiles);
  a full yellow belt tile holds 8 items. Row 17 measured 515/520 = 99% while 50 drills sat
  `waiting_for_space_in_destination`. Edge drills on a patch report `no_minable_resources`
  (10 of 72 here), so budget ~86% drill utilisation on a hand-mapped patch.
- 2026-10-30 — A pole with no generator is a dead network: the chest-bank inserter rows reported
  `no_power` even though a medium pole stood right next to them, because that pole had no wire
  path to the field's EEI (~54 tiles away). Adding a second `electric-energy-interface` beside
  the bank (inside that pole's supply area) made network 4 self-sufficient and all 3 inserters
  went `working`. Check `electricNetworks.productionWatts` per network, not just pole presence.
- 2026-10-30 — **Splitters cannot sort by item type through this API.** There is no splitter
  type, field, or input anywhere in the schema (`grep -i splitter *.graphqls` only matches prose
  in the topology/belt-flow docs), so neither a filter nor an output priority is readable or
  settable; the entity can only be placed and rotated. Empirical check: a splitter fed 20
  `iron-gear-wheel` + 20 `electronic-circuit` delivered exactly **10+10 of both types to each of
  its two outputs** — pure count splitting. Inserter filters do exist in-game
  (`inserterSettings { useFilters filterMode filterSlotCount filterSlots { index name quality comparator } }`,
  read-only via `inspect.logistics`) but no mutation sets them, and `filter-inserter` has no
  recipe in this save, so a filter-based sorter is not reachable today.
- 2026-10-30 — **Furnace type-lock facts** (verified, then rejected as a rig design): `transfer`
  of the "wrong" ore into a primed furnace returns `moved: 0` while the right ore moves; a
  stone furnace's crafting input caps at 45; and inserters refuse to pick an item whose target
  machine cannot accept it (status `waiting_for_source_items` with the item still in the chest),
  so a machine-per-type bank only works on mostly-correct belts. Unfuelled machines also block
  inserter pickup (`no_fuel`). Kept as reference, not used in the mall or mining design.
- 2026-10-30 — **Working sorter pattern without filters:** `transfer` is item-type-scoped, so
  `to-player` with `item:X` pulls only X out of a mixed chest and `to-entity` pushes it into X's
  chest. 100 items of three types across three buffers sorted into three single-type chests and
  three single-type belt stubs with zero cross-contamination, driven by one `go run . script`
  pass. Cost: one transfer pair per buffer per type; needs a character within build reach.
- 2026-10-30 — **API gap (open): no filter configuration.** To make sorting autonomous in-game
  we need mutations to set, at minimum, `splitter_filter` + `splitter_input/output_priority` and
  `inserter_filter_mode` + `inserter_filters` (engine fields to be confirmed against the 2.0 Lua
  API). Rough shape: 2 schema inputs + 1 domain `*.go` Lua builder + gqlgen regen + validation
  tests; blast radius = new mutations only, no change to existing resolvers. Until then, use the
  transfer-based sorter or keep mixed lines separated at the source.
- 2026-10-30 — Chest-fed mall arithmetic: with 200 items per source chest, a product line runs
  out in ~10-30 min (splitter needs 5 plate + 5 EC per craft → 40 splitters from one seed).
  Production statistics close exactly (40 splitters = 200 plate + 200 EC consumed), so a drained
  chest is supply, not a bug — use `factoryOverview.production.consumed` to audit.
- 2026-10-30 — Schema drift vs this handbook's command cheatsheet: `PlaceInput` has **no**
  `announce` field (only `message`); `HarvestAreaResult` exposes `treesMined`/`rocksMined`;
  `FactoryProduction` has no `production` field (use `factoryOverview { production { ... } }`);
  `pickupArea` returns `itemsPickedUp`/`itemsRemaining` as `[ItemStack!]!`, so they need subfield
  selections. `go run . describe <Input>` settles each in one call.
- 2026-10-30 — `harvestArea` reach is short: a 24x24 request harvested 1-2 trees per standing
  spot (~4-tile reach), so wood is expensive (6 trees → 6-19 wood). Tree type filter is
  `type: "tree"`; `name: "tree"` is an unknown prototype. Plan a wood farm instead of
  harvesting for production.
- 2026-10-30 — **Electrification ramp (mining + smelting).** Replaced every furnace-side
  burner inserter (24) with electric `inserter`s and all patch drills with
  `electric-mining-drill`s, then watched `factoryOverview`: iron-plate 37 → 131/min,
  copper-plate 25 → 140/min, ore 0 → ~276/min total, 16 furnaces with 0 `no_fuel` /
  0 `output_full`, one EEI network at ~1.09 MW production = consumption. Key sequencing: power
  reach first, then drills, then inserters — each stage is visible in `machines.status`.
  (verified: repeated ONE_MINUTE samples 11:30–12:24)
- 2026-10-30 — **`no_power` inserters look like production bugs.** Two iron furnaces sat
  `full_output` with empty plate belts: their output inserters at `(23.5,-66.5)` and
  `(26.5,-66.5)` were `no_power` (outside every supply area). Placing substation `(24,-62)`
  fixed both instantly. When a furnace is `full_output` but the target lane is empty, inspect
  the inserter's `statusName`, not the belt. (verified: inspect before/after, furnace → working)
- 2026-10-30 — **Single-item furnace feed without mixing:** each furnace row has only two free
  sides, so ore and fuel cannot both use short inserters. Layout that works: ore lane
  `y=-70.5` → short inserters `y=-69.5` → furnaces `y=-68/-67` → short inserters `y=-66.5` →
  plate lane `y=-65.5`, with a **long-handed** inserter in the same `y=-69.5` row picking coal
  from `y=-71.5` (2-tile pickup, drops 2 south into the furnace). Copper row equivalent: coal
  lane `y=-64.5`, long-handed inserter row `y=-62.5`. Long-handed inserters are electric and
  need pole coverage. (verified: 8 iron + 5 copper long-handed inserters, 0 `no_fuel`)
- 2026-10-30 — Coal lane plumbing detail: the coal patch lane `y=-71.5` turned south at
  `(0.5,-71.5)` but the column was missing its first tile `(0.5,-70.5)`, so coal piled at the
  corner and never reached the copper row. A dead-end chest at `(13.5,-71.5)` (800 coal) with a
  burner inserter at `(14.5,-71.5)` was also eating the lane; removing both and bridging the two
  tiles sent coal down the `x=0.5` column to the new `y=-64.5` copper fuel lane.
  (verified: lane contents before/after, copper furnaces `no_fuel` → working)
- 2026-10-30 — **Never put a second item type on `y=-64.5` east of `x≈30`** — that row is the
  iron-plate bus lane. A copper coal-lane extension placed there put coal onto the bus
  (coal+iron-plate tiles at `x 33.5..44.5`); removed the extension belts and `beltTake`d the
  coal. (verified: bus rows re-surveyed clean)
- 2026-10-30 — `place` of two `underground-belt`s in one `placeMany` call did **not** connect
  (no flow across the gap, both ended up as entrances — `[INFERENCE]`); moving the blocking
  big pole (to `(24,-56)`, keeping `electricNetworks total == 1`) was simpler and worked.
- 2026-10-30 — Removing a chest while its inserter keeps running drops plates on the ground,
  and ground items block `place` ("cannot place entity at position"). Order matters: remove the
  inserter, clear ground items (`pickupArea`), then remove/place the chest, then re-place the
  inserter. When the **player inventory is full**, `pickupArea` returns `itemsPickedUp: []` with
  `itemsRemaining` — use a detached character for the pickup instead. (verified live)
- 2026-10-30 — Crafting differs by actor on this save: `craft(input:{player:"wricardo"})`
  produced hundreds of belts/drills/substations without consuming ingredients (sandbox editor
  crafting), but detached characters' `craft` consumes real ingredients — subagents had to move
  stock from the player inventory through a temporary chest. (verified by three subagents)
- 2026-10-30 — Mining drill output side is set by placement direction: `direction:"north"`
  drops south (onto the ground behind the drill); `rotate turns:2` flips it onto the belt in
  front. Always confirm `inspect { miningTarget { name } }` and that ore lands on a lane.
  (verified: 5 electric drills, ground piles before rotation)
- 2026-10-30 — **Mixed lanes: found the one real contamination path.** The iron feed lane
  `y=-70.5` (westbound) carried leftover iron ore past the last iron furnace into belt
  `(0.5,-70.5)`, down the `x=0.5` column and east into the **copper** furnace lane at
  `(0.5,-63.5)`. Copper furnaces then smelted iron ore, so iron plates appeared on the copper bus
  (86 iron plates found in the copper chest `(92.5,-58.5)`). Removing the belt at `(0.5,-64.5)`
  cut that path; the copper lane then carried copper ore only, and 20 residual iron ore were
  `beltTake`n from tiles `(15.5..19.5,-63.5)`. `(verified: belts survey before/after, chest
  inventory)`
- 2026-10-30 — **Cleaning a mixed lane:** `beltTake` removes one item type from one belt tile
  (`count` is a cap; it moved 1–5 items per tile), reach is ~4.5–6 tiles, so you must walk stands
  along the lane. Fastest convergence is to empty *every* item from each contaminated tile
  (`beltTake` per item name): the tile then refills from the single-item feed upstream instead of
  keeping the leftover. 73 mixed tiles were cleared this way in ~2 min of walking. `(verified:
  mixed-tile count 73 → 0, still 0 after 75 s with iron-plate production running)`
- 2026-10-30 — Detecting mixed belts: query `belts` over the whole base (limit 1000, one page)
  and flag every tile whose `line1+line2` item names contain more than one distinct name. Note
  contents are per tile (max 4 items per lane), so a "mixed" reading is real, not a segment
  artifact. Coal riding an ore lane is *not* inherently a bug in this base — each furnace has a
  single supply inserter, so the lane carries both ore (→ input) and coal (→ fuel slot); it only
  becomes a bug when it reaches the wrong furnace row. `(verified: 73 mixed tiles, all
  coal+iron-ore or iron+copper-ore)`
- 2026-10-30 — The sandbox `craft` mutation is **free** and instant: crafting 14
  long-handed-inserters, 4 substations, 5 big-electric-poles and 40 belts changed no ingredients.
  `(verified: inventory before/after)`
- 2026-10-30 — **Burner fuel is not sustainable by hand:** each `coal` keeps a burner mining
  drill running ~30 s (150 kW), so refuelling 4 coal drills (`fuel` with 4–5 coal mined from
  nearby coal tiles) only restarted coal for ~2 min before they went `no_fuel` again. `mine` on
  a `coal` resource tile yields exactly 1 coal; whole-base fuelling needs electric drills or a
  coal belt with inserters. (verified: drills `working` → `no_fuel`, `factoryOverview` coal
  0 → 49–70/min and back)
- 2026-10-30 — Distinguishing a frozen chunk from a stalled factory: belt `transportLines` and
  `machines.status` keep their last values either way. Compare a `stock` total (e.g. `coal`) at
  two points ~15 s apart; identical counts plus uniform `no_fuel`/`no_ingredients` means a real
  stall. Production stats (`factoryOverview`) only move when chunks are simulated.
  (verified: coal total 73→73 over 15 s during the stall, then coal production resumed after
  fuelling)
- 2026-10-30 — **Bus-end chest drain fixed with per-lane electric inserters.** The copper lane
  `y=-57.5` and iron lane `y=-64.5` dead-ended at `x=90.5` with no inserter while only
  `y=-58.5`/`y=-65.5` fed chests. Rotating the dead-end belts to `south` (plan: sideload into
  the served lane) **does not work**: the corner belt held its items (`transportLines` 4
  copper/iron plates, unchanged for minutes) and the next tile stayed empty, even after `remove`
  + re-`place` of the corner. Reverted to east and instead placed one inserter + chest per lane:
  `(91.5,-57.5)`/`(92.5,-57.5)`, `(91.5,-58.5)`/`(92.5,-58.5)`, `(91.5,-64.5)`/`(92.5,-64.5)`,
  `(91.5,-65.5)`/`(92.5,-65.5)`. Growth measured: +162 copper and +160 iron plates in 62 s on
  the new chests. (verified: belt/inventory `inspect` before and after each change)
- 2026-10-30 — Inserters can be powered by the sandbox cheat pair
  `electric-energy-interface` + `substation`: EEI is 2x2 (place with integer centre, `(87,-60)`),
  substations are 2x2 (`(89,-61)`, `(85,-60)`), the two poles auto-connect into one network and
  the EEI supplies on demand — `electricNetworks` reported production = consumption = 3798 W
  with four `inserter`s working. `place` for inserters uses the **pickup** side: `direction:
  "west"` picks from the tile to the west and drops east, the opposite of an east-facing machine
  layout assumption. (verified live)
- 2026-10-30 — This sandbox map has **free crafting**: `craft` returned `started` counts while
  the ingredients stayed untouched (iron plates 8→8 after 2 gears + 2 circuits + 2 inserters;
  wood 17→17 after 2 wooden chests). Do not infer ingredient stock from crafted output here.
  (verified: inventory before/after)
- 2026-10-30 — `mine` reach is a character's `can_reach_entity`, roughly 4–4.5 tiles: a tree at
  `(96.3,-62.25)` was out of reach from `(92.64,-60.8)` but minable after `navigateStart` to
  `(95.06,-61.43)`. Each tree yields 4 wood. (verified live)
- 2026-10-30 — Plateau: `electricNetworks` reports only networks with poles in the query area;
  a lone unconnected source (EEI placed away from poles) shows `total: 1, productionWatts: 0`
  until a pole is added next to it. (verified: network totals before/after placing `(85,-60)`)
- 2026-10-30 — **CORRECTED:** a character actor *can* both give and receive items through
  `transfer`. `LuaEntity.get_main_inventory()` exists for characters, so `transfer` with
  `character` reads/writes that character's own main inventory: `direction:"to-player"` moved 1
  iron plate from a chest into character `218` (its `inventory` then showed it), and
  `direction:"to-entity"` moved it back. The earlier "player has no main inventory" failure is
  stale. Inventories are still keyed by entity type (container/furnace/crafter/lab), so a chest
  is addressed with `inventory:"chest"` regardless of actor.
  (verified live, tick ~120k: chest A ↔ character 218 both directions)
- 2026-10-30 — `Actor.character` now reports the player's character unit number (player
  `wricardo` = **267**); before the fix it was null with `hasCharacter: true` and needed a
  `nearby` scan. `place` also returns `ActionResult.unitNumber`, and `inspect`/`transfer`/`fuel`/
  `remove` accept `unitNumber`. The engine's `game.get_entity_by_unit_number` only resolves
  prototypes flagged `get-by-unit-number` (characters, vehicles, some hubs) — chests, belts, and
  inserters return nil — so the backend scans the surface for the given prototype name and
  compares unit numbers; pass `entity` with `unitNumber` for those.
  (verified live: `inspect(unitNumber:"302", entity:"wooden-chest")`, transfer, fuel with and
  without the name, and `remove(unitNumber:"440", entity:"transport-belt")`)
- 2026-10-30 — Unit numbers now address every entity operation that can use one: `rotate`
  (belt 305, 2 turns each way), `beltTake` (moved 2 plates off belt 305 by unit number),
  `setRecipe` (`assembling-machine-1` unit 463 → `iron-gear-wheel`), `removeMany` entry
  (`{unitNumber:"463", entity:"assembling-machine-1"}` removed the machine), `beltRun`
  (`unitNumber:"303"` seeded at the right belt), and `beltThroughputStart` (`unitNumber:"303"`
  → COMPLETE; a chest unit number is rejected with "entity is not a transport belt"). Naming a
  different prototype with a unit number is rejected by the surface scan. `mine` and `pickup`
  cannot: resources, trees, rocks, and dropped stacks report `unitNumber: null`.
  (verified live; coordinate paths re-tested for rotate and removeMany)
- 2026-10-30 — `beltThroughputStart` reads **0 crossings on jammed/saturated lanes and at
  dead-ends**, even while the same lanes are visibly feeding chests (~120–160 plates/min
  measured by chest deltas at `x=86.5` and `x=90.5`). Do not use the sampler to prove flow on a
  backed-up line; use inventory deltas of the sink, or sample a lane you know is moving.
  (verified: three separate 300–600 tick samples returned 0 while chest counts rose)
- 2026-10-30 — Furnace output inserters holding a plate with
  `waiting_for_space_in_destination` mean the receiving **belt lane is jammed**, not a recipe
  problem. Rotating/opening the lane end cleared it (5 iron furnaces left `full_output`).
  (verified: `inspect` before/after on `(5.5,-66.5)` and `(8.5,-66.5)`)
- 2026-10-30 — Bus lanes report `iron-plate:4` / `copper-plate:4` on every tile of a segment,
  so a per-tile `transportLines` read is not a per-tile quantity; read chest/machine inventories
  instead of belt counts when judging fullness. (verified: repeated reads on `(90.5,-64.5)`)
- 2026-10-30 — `characterSpawn(input:{x,y,surface,force})` creates a detached character at the
  nearest non-colliding spot within 10 tiles and returns `unitNumber` (`1870` for `(94,-61)`);
  it works while the player stays connected, unlike `characterCreate`. (verified live)
- 2026-10-30 — `craft` used to announce **twice** (generic `command` bridge printed the
  generated summary, then `craftMutation` printed again, and a custom `message` still left the
  generated line in chat). Fixed by routing craft through `commandQuiet` so it announces once
  after the queue settles; `TestCraftAnnouncesOnce` pins one print for queue-only, custom
  message, and wait-drain paths. Other custom announcers (`placeMany`, `removeMany`,
  `placeTile`, blueprints, navigation) bypass `command` and were already single. (verified:
  fake-RCON probe counted 2 prints before, 1 after; `go test ./...` passes)
- 2026-10-29 — `nearby` results are capped at 100 and are **not sorted** (README + observed
  exactly 100 entries for radius 30 and 40); narrow the radius or filter by `name`/`type`
  rather than paginating. (partially verified: cap observed, ordering inference from README)
- 2026-10-29 — Always pass explicit `x`, `y`, and `radius` to `nearby`. The schema defaults
  `x`/`y` to `0,0` and `radius` to `10`, while observed results around a radius-30 query
  looked player-centered. Do not rely on omitted-coordinate behavior; pass the position.
  (verified: schema defaults from `schema.graphqls`; runtime behavior ambiguous)
- 2026-10-29 — `setRecipe` accepts **only** a character unit number, not `player`. To change a
  machine recipe you need a detached character, which requires the player to be disconnected.
  Plan machine reconfiguration around that constraint. (verified: `SetRecipeInput` schema)
- 2026-10-30 — Research is active: `advanced-material-processing`, 75 research units total,
  69 remaining. Both labs report `missing_science_packs`; logistic science is 0/69 while
  automation science has 1246. (verified: `scienceBudget`, `machines`)
- 2026-10-29 — GraphQL errors for several `factoryOverview` fields: `machineTick` and
  `productionTick` are scalars, `production[].item` is a String, not an object. Use the schema.
  (verified: failed query then `describe FactoryOverview`)
- 2026-10-29 — `remove` mutation mines one reachable entity back into the actor's inventory
  (`RemoveResult.items`); it rejects characters and out-of-reach entities. The `player` actor is
  **not** administrative here: it enforces reach like a character. (verified: remove of
  `burner-mining-drill` at (35,63) from character 4 returned the item; distant attempt failed
  "entity is out of reach")
- 2026-10-29 — `removeLua` shipped with a scoping bug: `local before=inventory_item_counts(inv)`
  ran before the shared `inventoryItemHelpersLua` chunk was concatenated, so every call failed
  with `attempt to call global 'inventory_item_counts'`. Fixed by moving the snapshot line after
  the helper chunk. (verified: build + successful remove)
- 2026-10-29 — The connected player's character id is not exposed (`player.character` is null
  and `status.players[].actor.character` is null). Recover it with a `nearby` query at the
  player's position and read `entities[].unitNumber` where `type == "character"` (unit 4 in
  this save). (verified)
- 2026-10-30 — `walk` is **removed** from the GraphQL API and CLI; movement is only
  `navigateStart` (coordinates, `targetEntity`, or `targetCharacter`, plus `tolerance`,
  `timeoutTicks`, `wait`, `waitTimeoutSeconds`) and polling `status` (`ARRIVED`). Navigation
  needs the mod and handles obstacles that plain walking bricked on. Use `targetEntity` with a
  chest/assembling-machine/inserter unit number to stop beside it instead of guessing tiles; the
  goal is the entity's bounding box inflated by `tolerance`, so pick tolerance 1 for a chest and
  up to 2 for a 3x3 assembler. (verified: navigateStart to (52.5,-29.5) returned ARRIVED;
  targetEntity on a chest, inserter, and assembling-machine-1 each returned ARRIVED, and a
  destroyed target returned `target entity not found`)
- 2026-10-29 — `setRecipe` only accepts entities whose type is `assembling-machine`; passing a
  stone furnace fails "assembling machine not found". A previous test found a null-recipe
  furnace stayed `recipe: null` after a copper-ore `transfer`; the current schema comment claims
  furnaces auto-select on transfer. Re-test before relying on either behavior.
  (partially verified: furnace (-39,96) stayed null in the earlier test)
- 2026-10-29 — Electric and burner drills have different footprints (2.7 vs 1.4 collision):
  a burner drill's 2x2 tile cannot host an electric drill in place. Place the replacement on a
  clear 3x3 (e.g. south of the old site) and add a pole within supply range. (verified:
  "cannot place entity at position" in place, success at 31.5/37.5,39.5)
- 2026-10-29 — `transfer` directions are only `to-player` and `to-entity`; `from-entity` is
  rejected. Chest inventories are addressed with `inventory:"chest"`, machine inputs with
  `"input"`, outputs with `"output"`. (verified: error text + successful pulls)
- 2026-10-29 — Dead-end chest buffers are this base's dominant failure: 431 iron plates in
  `(38.5,31.5)`, 641 iron ore in `(39.5,64.5)`, 255 coal in `(36.5,62.5)` while their consumers
  starved. Always check `stock` per item before concluding a line is short. (verified: `stock`
  totals vs furnace `no_fuel`/`no_ingredients`)
- 2026-10-29 — Reach is enforced for actor-driven entity operations: `place`/`fuel`
  (`actions.go`) and `transfer` (`production.go`) resolve `c = p` for a character or
  `c = p.character` for a player and require `can_reach_entity`/`build_distance`.
  `mine`, `remove`, `removeMany`, `rotate`, `setRecipe`, `beltTake`, `placeTile`, `pickup`,
  `pickupArea`, and `harvestArea` also enforce reach. `placeMany` now enforces reach for both
  player and character actors. (verified: `go build ./...`, `go test ./...`, and live mutations)
- 2026-10-29 — **Iron ore lane built:** drills `(31.5,39.5)`, `(34.5,39.5)`, `(37.5,39.5)`,
  `(41.5,39.5)` all powered (pole `35.5,41.5`) and drop onto belt `y=37.5` (dir east). Drills
  report `waiting_for_space_in_destination` because the lane backs up. (verified: belt lane shows
  iron-ore; drills powered)
- 2026-10-29 — Copper furnace `(-45,96)` input was observed cycling 2→1→2 copper ore with
  electric inserter `(-43.5,96.5)` operating. Iron furnace `(40,60)` smelted iron and reached
  100 output plates, blocking further production. `worldLogistics` showed its supposed
  chest-fed test inserter `(37.5,60.5)` drops on belt `(38.5,60.5)`, not directly in furnace.
- 2026-10-29 — Inserters at iron furnace `(38,34)` were observed holding ore while reporting
  `waiting_for_space_in_destination`, including when a transfer-seeded recipe was active.
  Cause remains unknown; no evidence establishes recipe provenance as the cause. Inspect actual
  pickup/drop, belt lanes and furnace inventories before changing the layout.
- 2026-10-30 — The old `inspect` mining-drill decode error is fixed: live `inspect` returned
  `modules[0]` and normal mining fields with no error. Keep using `machines` for bulk status.
  (verified: live inspect at 31.5,39.5)
- 2026-10-29 — `setRecipe` is engine-limited to assembling machines (`LuaEntity.set_recipe`
  rejects furnaces: "Entity is not assembling-machine"); reverting a furnace whitelist was
  required. Recipe changes for furnaces must come from item insertion. (verified)
- 2026-10-30 — `beltTake` accepts `player` or `character` and enforces belt reach for the
  acting character; it moves items off a belt into its inventory, useful when the player must
  hand-feed a machine. (verified live: player near belt `moved: 0`, far belt → "belt is out of
  reach", both actors → validation error)
- 2026-10-29 — Reach applies to `place` with a character too ("position is out of build reach"),
  and placement under the player fails ("cannot place entity at position"); step off first.
  (verified)
- 2026-10-29 — `worldResources` needs `area` ≤256 tiles per side and returns resources,
  obstacles and water in `worldEntries[].kind`; use it to prove a belt corridor is clear of water
  and cliffs. (verified: 256-tile rejection; empty water/cliff list for x 28..60, y -40..75)
- 2026-10-29 — Factory diagnosis surface now answers "why is this line stuck" directly:
  `beltRun(surface,x,y,maxEntities)` returns a directed belt component (edges, `sources`,
  `sinks`, `boundaryEdges`); `factoryGraph(area)` returns machines, belts, chests and inserters
  of an area in **one** snapshot, including inserters outside the area whose actual target is
  inside; `inspect { logistics { beltRuns } }` walks belts reached from a machine's real
  inserter targets. `sources`/`sinks` are structural terminals, **not** stall or dead-end
  diagnoses. (verified live: cable assembler yielded 2 components; maxEntities=5 on an 81-belt
  line returned 5 nodes, 6 edges, 2 boundary edges, truncated=1)
- 2026-10-29 — `machines { machines { diagnostics } }` gives per-machine `shortfall`,
  theoretical `throughput`, `blocked` category plus raw engine status, and `power` state in one
  extra snapshot. `throughput.actualCraftsPerSecond` and `power.satisfaction` are always null
  (a single snapshot cannot measure them; 2.0 exposes no per-machine demand ratio) — do not read
  null as zero. (verified: 29 nauvis machines, no unmapped status; backed-up drills reported
  OUTPUT_BLOCKED with `waiting_for_space_in_destination`)
- 2026-10-29 — `billOfMaterials(item, amount, stock)` plans against one stock snapshot and spends
  each stored unit once: 20 transport belts with 10 in stock planned 10 crafts, 15 iron plates
  (15 allocated of 30) and 5 gear crafts worth 10 more plates. `complete:false` means bounded or
  unresolved (`RECIPE_CYCLE`, `EXPANSION_LIMIT`, `NO_ENABLED_RECIPE`), **not** "cannot build".
  `itemFlow` lists who produces/consumes one item with role-local buffered counts. (verified)
- 2026-10-29 — `scienceBudget` reports nominal packs left versus retrievable storage and counts
  labs actually missing a pack; `inspect { missingSciencePacks }` reports one lab's gaps;
  `ghosts` lists entity/tile ghosts and item-request proxies with real placement materials
  (`buildable` is material-only); `inspect { fluidNetwork }` deduplicates shared engine segments
  (3 pipes = 1 segment, 100 water, 300 capacity). (verified)
- 2026-10-29 — `beltThroughputStart` measures **real** per-lane crossings via the navigation
  mod (`factorio_ai_belt_flow` protocol 1, Factorio ≥ 2.0.76). Verified 9 copper plates crossing
  a moving nauvis belt in 600 ticks (54/min) and 0 on a jammed belt. The mod must be loaded from
  a save before these fields exist. (verified)
- 2026-10-29 — Belts only move where the game simulates the chunks: a fresh surface with no
  player had static belt items and `no_power` inserters, while nauvis crafting progress advanced
  normally. Do not conclude "the belt sampler is broken" from a zero rate; check that a player or
  an active factory keeps those chunks alive. (verified on an isolated save copy)
- 2026-10-30 — Root surface after the gap-audit implementation: 39 queries and 66 mutations (`ancestor` counts change as schemas evolve; verify with `go run . queries|mutations`). Earlier snapshots of this line said 31/32. Newer reads include
  `entities`, `belts`, `beltRun`, `factoryGraph`, `itemFlow`, `billOfMaterials`,
  `scienceBudget`, `ghosts`, and `beltThroughput`; newer writes include `equip`, `removeMany`,
  `createBlueprint`, `createBlueprintFromEntities`, `placeBlueprint`, `beltThroughputStart`,
  and `announce`. `beltThroughputStart` has **no** message field. Every mutation input otherwise
  announces its generated summary by default; `message` overrides the text. `announce(message:
  String!)` prints free-form chat, for narrating plans. (verified: `go run . queries --args
  --desc`, `go run . mutations --args --desc`)
- 2026-10-30 — Live smoke checks passed for `entities` (16 iron-ore), `belts` (146 belts),
  `beltRun` (truncated at maxEntities=5), `factoryGraph` (20 nodes, 2 machines, 16 belts),
  `itemFlow` (8 producers / 33 consumers, truncated), `billOfMaterials` (`complete:true`,
  10 transport-belt crafts), `scienceBudget` (advanced-material-processing), `ghosts` (0),
  `beltThroughputStart`/`beltThroughput` (id 2, `COMPLETE`), and both blueprint-create
  mutations. (verified live; `placeBlueprint`/`removeMany`/`equip` were not executed because
  they mutate the save)
- 2026-10-30 — `placeMany` and `placeTile` now resolve the player's acting character and
  enforce surface and build reach for **both** actor kinds; `placeMany` checks every position.
  Live results: player and character far placements → `out of build reach` / `position is out of
  build reach`; near placements → `actor lacks item` or tile-condition error, proving reach passed
  first. `beltTake` now takes `player` or `character` and enforces belt reach. (verified live with
  character unit 4 and player `wricardo`; no world state changed)
- 2026-10-30 — `probePlacement(input: {prototype, area, direction})` checks engine placement on
  every tile center of an area (≤16384 tiles; 256 per side) server-side in **one** RCON round
  trip and returns only the blocked tiles, each with `blockers` (colliding entity prototype
  names), `terrain` (the tile itself blocks that prototype) and `tile` (ground tile name).
  Verified: a 10x8 area near water matched 80 separate `worldPlacement` calls with 0
  mismatches; corridor `x -100..140, y -60..-38` sampled 5280 tiles, 271 terrain-blocked tiles
  exactly matching `worldResources` `waterTotal` 271, with only `big-sand-rock`/`offshore-pump`
  as entity blockers; belts are placeable over ore (56/56 over a coal patch). Validation
  rejects direction outside 0..15, non-finite or inverted areas, >16384 samples, and unknown
  prototypes before dialing.
- 2026-10-30 — There is **no** pathfinding query. Navigation exposes only `navigateStart`,
  `navigateCancel` and `navigation(id)` with statuses `PATHFINDING/MOVING/ARRIVED/NO_PATH/
  STUCK/...`; the mod keeps `surface.request_path` waypoints in `job.path` and never returns
  them. Character paths use the character collision box, so they do not answer belt or pipe
  routing — use `probePlacement` per prototype instead. (verified: `navigation.graphqls`,
  `mods/factorio_ai_navigation/control.lua` request_path/snapshot)

- 2026-10-30 — `just graphql` (`go run . serve --listen 127.0.0.1:8080`) exposes the same schema over HTTP at
  `/query`, which is how the TypeScript agents (`play/harness`) reach the game. The endpoint accepts **one**
  GraphQL operation per request — `gql_batch` and subscriptions are rejected ("transport not supported"), so
  batch several root fields into a single document instead. GET is query-only (422 on a mutation).
  (verified: `serve` + curl, then four durable agent runs that measured ticks through it)

- 2026-10-30 — **God controller → character is one raw RCON command.** There is no GraphQL
  mutation that gives a connected player a character (`characterCreate` demands a disconnected
  player, `characterSpawn` makes an empty detached character), and no Lua passthrough command.
  Send the console command directly over RCON (same framing as `main.go`: 12-byte header then
  body) — `/silent-command game.players["wricardo"].create_character()` — which returns a
  **boolean**, not the entity (`attempt to index local 'c' (a boolean value)` if you print it).
  Afterwards `player.character` is still null in GraphQL but `player.hasCharacter` is true and
  `player.position` becomes the character's position, so `player:` placement/transfer pass reach
  and read the player's main inventory. (verified: `hasCharacter: true`, chest/inserter/belt
  placement and `transfer` all succeeded afterwards)
- 2026-10-30 — `craft.count` is a number of **crafts**, not items: `transport-belt` ×5 yielded 10
  belts (recipe output 2), `iron-plate` ×200 yielded exactly 200. In this sandbox both consume no
  ingredients, but a detached character's craft still fails `could not queue recipe; check
  ingredients` with an empty inventory. (verified: inventory before/after each craft)
- 2026-10-30 — `place` snaps 1x1 entities to a half-tile center: a `wooden-chest` requested at
  `(-13,15)` lands at `(-12.5,15.5)`, and `transfer`/`inspect` then need `x:-12.5, y:15.5`
  (integer coordinates fail `entity not found at center on actor's force`). Inserter
  `direction:"west"` picks the tile to the west and drops one tile east, which is the whole
  chest→inserter→belt→inserter→chest chain used here. (verified: full 200-plate chest-to-chest
  run: chest A 200→0 in ~40 s, chest B exactly 200, both inserters idle)

- 2026-10-30 - Script ghost revival does NOT consume items: `LuaEntity.revive{...}` returns `(collided items, built entity, proxy)`; the first value is nil on normal success, so success must be judged from the second value. `buildGhosts` therefore reports collisions in `missing`, not shortages, and both modes are sandbox shortcuts. (verified live on an isolated 2.0.77 copy: 2 ghosts revived, `revived: 2`, no items consumed)
- 2026-10-30 - A roboport created by raw Lua without `raise_built=true` never joins a logistic network (`r.logistic_network` is nil even with energy and robots); API `place` passes `raise_built=true`, so bot networks built through GraphQL work. After building with the event, `logisticNetworks` reported cells 1, robots total/available 10, and `transfer inventoryKind: ROBO_PORT_ROBOT` added 5 robots (15 total). (verified live)
- 2026-10-30 - 2.0.77 fluid writes: `LuaEntity.insert_fluid{name,amount,temperature}` returns the accepted amount, `clear_fluid_inside()` empties, and capacity comes from `entity.fluidbox:get_capacity(i)`. `add_fluid`/`get_fluid_capacity` are 2.1-only. `setFluid` SET/ADD/CLEAR verified against a pipe (100 water, ADD clamped to capacity, CLEAR to 0) and `fluidStock` reported it. (verified live)
- 2026-10-30 - `set_recipe` keeps items that are valid ingredients of the new recipe, so draining cannot rely on its return value. `drain: TO_ACTOR` now explicitly empties input/output into the actor first (verified: 20 iron plates returned, machine empty), and `clearRecipe` returns 5 loaded plates while clearing the recipe. (verified live)
- 2026-10-30 - Scripted direction writes are silently ignored for belts and rails in 2.0.77 (`e.direction=N` reads back unchanged; `rotate` only does quarter turns). Place 8-way prototypes with `directionCode` (a rail at direction 2 verified), and `setDirection` now fails with an actionable message instead of a false success. (verified live)
- 2026-10-30 - Entity tags are not scriptable on normal entities in 2.0.77: `e.tags={...}` is accepted then reads back nil. `setNameTag` works; `setEntityTags` deliberately fails with a message pointing at blueprint/ghost metadata. Combinator `parametersJSON` is readable through `get_control_behavior().parameters`. (verified live)
- 2026-10-30 - `LuaEquipmentGrid.put` inserts one equipment per call (no count); the grid read is `get_contents()` and removal is `find(name)` + `take{equipment=...}`. Equipping power armor then `equipGrid` 2 exoskeletons worked and `Actor.equipmentGrid` reported them. (verified live)
- 2026-10-30 - Ground `dropItem` spills with `spill_item_stack`, and belt drops use `get_item_insert_specification` + `can_insert_at` + `insert_at`; both verified (5 plates on the ground, 5 inserted into belt line 2). Transfer with `inventoryKind: CHEST` and quality `uncommon` round-tripped a chest. (verified live)
- 2026-10-30 - `trains` reads `game.train_manager.get_trains{surface,force}`; `train.schedule` is a plain table (`{current,records}`), assigning or nil-clearing works, and `trainControl` sets `manual_mode` (status `no_path` without stations). Locomotive/wagon placement must be aligned to a rail; `place` direction semantics for rolling stock are engine-snapped. (verified live on a script-built rail line)
- 2026-10-30 - This managed save runs with the builtin `space-age`, `quality` and `elevated-rails` features enabled (mod-list.json + server log FeatureFlag quality=true), so non-normal quality items exist and all write paths now take a `quality` field. (verified from server state)
- 2026-10-30 - Remaining unverified live paths: `useCapsule`, `shoot`, and every `player:` actor path that needs a connected player (headless server has no client), plus Space Age surfaces. They are covered by unit/fake-RCON tests and should be smoke-tested when a client connects. (limitation)

- 2026-10-30 - Rocket silo feed (verified on an isolated copy): the silo does NOT accept `rocket-part` items in its input inventory (`moved: 0`); it runs the `rocket-building` recipe itself, so feed 1 processing-unit + 1 low-density-structure + 1 rocket-fuel per part into `inventory:"input"`. `rocketParts` then rises with crafting, `rocket_parts_required` is 50 on this Space Age save (not 100), and once the engine reports `waiting_to_launch_rocket`, `launchRocket` launches (`rocket_silo_status` 8). Power is mandatory: an unpowered silo keeps `rocket_parts` at 0 and never crafts.
## 9. Open questions

- Which technologies are actually required for the rocket silo and rocket parts in this save?
- Where is the nearest large iron/copper patch, coal, oil, and water? (use `worldResources`)
- Is the player expected to stay connected, or should detached-character mode be used?
- Are biters a near-term threat? Check `safety` and evolution/pollution before expanding far.

### GraphQL gaps raised 2026-10-30 — all closed the same day

1. **Connected player without a character** → `playerRestoreCharacter(input:{player})` creates and
   attaches one (`p.create_character()`), returns its unit number/surface/position, leaves the
   player's main inventory untouched, and rejects a player that already has a character.
2. **`Actor.character` was always null** → now reports the player's character unit number
   (verified live: `"267"`); character-only mutations can target it directly.
3. **Entity addressing needed exact snapped centers** → `inspect`, `transfer`, `fuel`, `remove`,
   `rotate`, `setRecipe`, `beltTake`, and each `removeMany` removal accept `unitNumber`, and
   `beltRun`/`beltThroughputStart` accept it instead of belt x/y. Only prototypes flagged
   `get-by-unit-number` (characters) resolve by number alone, so pass the prototype `entity` name
   too; the backend scans the surface by that name (`beltTake` implies `transport-belt`). `mine` and
   `pickup` stay coordinate-only: resources, trees, rocks, and item stacks have no unit number.
4. **`craft.count` semantics** → documented in the schema and README as crafts, not items.
5. **Power outages were invisible in summaries** → `MachineSummary.noPower` now counts
   `no_power`, `low_power`, and `not_connected_to_electric_network` machines (schema + Lua
   bucket in `factory_machines.go`). Verified live: a lone unpowered `assembling-machine-1`
   placed at `(26.5,96.5)` raised `factoryOverview.machines[assembling-machine].noPower` 0 → 1
   (total 8 → 9) and `machines(status:"no_power")` returned exactly it; removing the machine
   restored 8 / 0. Inserter power faults still require `powerConsumers`, because machine scans
   cover only assembling machines, furnaces, drills, labs, and silos.

No Lua/console passthrough was added; the fix stayed inside the typed surface.
