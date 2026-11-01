# Engine and API traps (verified facts)

Each item says what is true, how it fails, and the workaround. Provenance: **[live]** = reproduced against the managed server in this repo (Factorio 2.0.77, Space Age, quality enabled); **[ticket]** = recorded in closed Beads tickets; **[README]** = repository contract; **[memory]** = earlier sessions, re-verify before relying; **[INFERENCE]** = reasoned, not observed. Repository schemas and live state outrank this file.

## 1. Addressing and identity

- **`unitNumber` alone is not enough.** `inspect`, `transfer`, `repair`, `fuel`, `waitUntil` targets and most entity-addressed operations reject `{unitNumber}` with `prototype is not resolvable by unit number alone; pass entity too` (or `entity not found by unit number; pass entity too`). Send `{entity:"lab", unitNumber:"60"}`. [live]
- Resource tiles, trees, rocks and ground items have no unit number: address by exact position (`{entity:"iron-ore", position:{x:-114.5,y:-38.5}}`). [live]
- Exact engine names only. `wall` does not exist (`stone-wall` does). Technology names are not recipe names. Ask `prototype`, `itemRecipes`, `researchInsights`. [live]
- `recipe(input:{actor:{...}, recipe:"name"})` wraps the actor; `inventory(input:{player:"x"})` does not. [live]
- `characters(detachedOnly:true)` is the default and **hides connected players' characters**; use `detachedOnly:false` or `status.players[].actor.character`. [README]
- `UnitNumber` is a string in GraphQL; keep it quoted.

## 2. Placement and building

- `placeMany` creates unpaired underground belts unless each placement sets `undergroundType: INPUT` (entrance) or `OUTPUT` (exit). Both halves become entrances otherwise and items dead-end. Prove pairing with `beltStretch` (tunnel collapses to one hop) or measured flow; `undergroundNeighbour` is always null on 2.0.77. [README][memory]
- `setDirection`/`rotate` are not reliable for belts and rails (the engine ignores scripted direction writes). Place with the correct direction; to fix, remove and re-place. Inserters, machines and pipes-to-ground respond to `setDirection`. [README][ticket]
- A placement fails while the acting character stands on the target tile (`cannot place entity at position`). Navigate ~3 tiles off, then place; use the response's snapped position afterwards. [memory]
- `probePlacement` is engine collision only and is wrong or opaque for some prototypes: rocket-silo (9x9) returned a 3x3-shaped grid; pumpjack tiles are "blocked" with an **empty** `blockers` list when the crude resource is missing. Empty blockers + blocked > 0 means a prototype requirement failed. [memory]
- `placeBlueprint` places **ghosts**, consumes nothing; `buildGhosts` revives without items or robots (admin shortcut). [README]
- `place` can commit the entity and then fail response decoding: `decode place response: json: cannot unmarshal string into Go struct field ActionResult.direction of type int`. `placeMany` is unaffected. The working tree now normalizes `DirectionString` names to engine ints (unit-tested `TestPlaceDecodesDirectionString`); an older `serve` binary still has the defect. Read back the entity by `unitNumber`/position before retrying. [live][source fix]
- `clearArea` deletes trees/rocks with no drops; `harvestArea` mines with drops. [README]
- `dropItem target:GROUND` refuses a tile with a belt/splitter/loader (the stack would be swallowed); use `BELT`. [README]
- `mine` is **instant** (not timed), but needs reach in practice: a character reports targets ~5 tiles away as `outOfReach`; navigate to within ~1-2 tiles with `tolerance 1`. `harvestArea` inserts yields directly into the main inventory, so a following `pickupArea` finds nothing. [memory]
- Burner drills can spill one item on their output tile and stall (`waiting_for_space_in_destination`); `pickupArea` over the patch box clears it. [memory]
- Chests: `setContainerBar` `bar` is the first UNUSABLE slot, so usable slots = bar - 1. `bar:1` = zero usable slots and `transfer TO_ENTITY` reports `moved:0` (looks like a bug, is semantics). `bar:2` = one slot. Raising bar by 1 adds exactly one stack. Bar 3 stalled inserters at ~100 items for some stacks. [live][memory]
- Stack size and fuel value are now available: `prototype(name){ item{ stackSize fuelValue } }`. Verified: iron/copper/steel/pipe/processing-unit 100, LDS 50, rocket-fuel 20, coal/ore/stone 50, circuit/science pack 200, wood 100. Fuel values: coal 4 MJ, wood 2 MJ, solid-fuel 12 MJ, rocket-fuel 100 MJ. [live]

## 3. Actors, crafting, transfer

- Sandbox scenarios strip the player's character and leave a god controller (`controller_type 2`, no character); `player:`-addressed operations then fail ("player has no character"). `playerRestoreCharacter` fixes it, or use `characterSpawn`. [README][memory]
- A player whose controller is an unusual type can show `hasCharacter:true` but fail `player:` actors with "actor has no main inventory"; use `actor:{character:"<unit>"}`. [ticket-skill]
- A disconnected player's crafting queue does not advance; use a detached character. `craft.requested` counts crafts, not items; `wait:true` returns when the **entire** queue drains. [README]
- `craft(wait:true)` is for bootstrap only; for anything repeated build an assembler.
- `transfer` moves between an actor and **entity** inventories only. Character -> player direct is impossible; stage through a wooden chest (2 wood): everyone deposits `TO_ENTITY`, the receiver withdraws `TO_ACTOR`. [memory]
- `transfer` and `fuel` need both `entity` and `unitNumber` on the target. `fuelMany`/`transferMany` reject duplicate targets and fill exactly `countPerTarget` or fail that target (no partial fill). [README]
- Quality: items default to `normal`; mutations take `quality` explicitly.
- Furnaces choose their recipe from inserted input; `setRecipe` applies to assemblers/chemical plants and needs the machine idle and empty (`drain: ABORT|TO_ACTOR|SPILL`). [README]

## 4. Logistics, circuits, power

- Script-set requester slots fill to **`min`**, not `max`. `min:0,max:50` delivers nothing; set `min=max=N`. [memory][ticket-skill]
- `logisticNetworks.contents` can be negative while demand is unmet. Not stock. [ticket-skill]
- `stock` excludes wagons, vehicles, robots, roboports, turrets, linked chests, equipment/trash and fluids. [live schema]
- `electricNetworks.satisfaction` can be null. `powerConsumers.connectedToProducingNetwork:false` is a predicate on producing networks, not proof of no network. [live schema]
- Power poles join networks when wire distance <= min(reach_a, reach_b). Big (32) to medium (9) is only 9. [live]
- A circuit condition only inhibits when the inserter is physically wired. `enabled:false` is rejected for ordinary inserters. Counters mixing a chest's item signal with hand-HOLD signals drift. [ticket-skill]
- Older `serve` builds fail `circuitNetwork` for both colors: `decode factory response: red is not a valid WireColor`; Lua returns lowercase `defines.wire_color` names while the GraphQL enum is `RED|GREEN`. The working tree normalizes lowercase colors in `normalizeGraphQL` (unit-tested); rebuild `serve` to use it. Until then use the probe-inserter bisection in `circuits-and-trains.md`. [live][source fix]

## 5. Reads that mislead

- `production` has no fluids. `itemFlow` rates are **theoretical** and unreliable for chemical plants/refineries (they reported 10-15). Compute from live recipe `energy` and machine `craftingSpeed`. [ticket-skill]
- Full belt != throughput; measured 0/min can be a downstream sink that is full. [ticket]
- `beltStretch` snapshot mixture on a jammed belt is static stock, not contamination; confirm crossings with `beltStretchMeasure`. [ticket-skill]
- `belts` sees transport-belt only (no underground/splitter/loader lines); `worldLogistics` needs one type per call and rejects roboports.
- Older `serve` builds fail `prototype(name:"inserter")` (also `burner-inserter`, `long-handed-inserter`): `cannot unmarshal array into Go struct field EntityPrototype.entity.inserterDrop of type main.Position`. Factorio `Vector` is a two-element array; the working tree normalizes `inserterPickup`/`inserterDrop` to `Position` (unit-tested and real-engine integration). Until `serve` is rebuilt, use the geometry formulas in `spatial-and-geometry.md`. [live][source fix]
- Research: `researchInsights.prerequisites` is `[{name researched}]`, not strings. `researchTriggerJSON` marks trigger techs. [live]
- `mapArea` legend letters change per call. [live]
- A `waiting_for_space_in_destination` pumpjack usually has an isolated stub output pipe. [memory]
- `factoryBriefing` sections are read at different ticks; ghosts need `area`, stockouts need `watchItems`. [live]
- **Inserter endpoints go null while the inserter is idle.** Factorio only populates `pickup_target`/`drop_target` once the inserter has run, so an unpowered or never-fed inserter reads `pickup:null, drop:null` in `factoryGraph`, `inspect.logistics` and `adjacency` even with a chest on its drop tile (verified live 2026-11-01: inserters `940/943/946` reported null drops while iron-chests sat on those tiles; the drop appeared after the assemblers were fed). The working tree falls back to the entity under `pickup_position`/`drop_position`, and `factoryGraph` also scans inserters only one tile beyond the area instead of the whole surface. Until `serve` is rebuilt, decide dead vs idle from geometry (`inspect.dropPosition`/`pickupPosition` plus `nearby` on that tile), never from a null target: deleting inserters on a null drop is wrong. [live][source fix]

## 6. Server, navigation, notifications

- **`shutdown` does not save.** Always `save` first; do not stop the shared server at all in normal play. [README]
- First RCON Lua command on a fresh save can trigger the "Lua console commands will disable achievements" prompt; the retry proceeds and permanently disables achievements for that save. [README][memory]
- `scripts/server.sh start` defaults to `ai-player.zip` when `FACTORIO_SAVE` is unset; `just restart` can silently switch saves. Do not restart the shared server; see `multi-agent-and-operations.md`. [memory]
- Navigation: one job per character; paused server cannot advance it; manual client input competes with scripted walking for a connected player; `targetCharacter` can fail `NO_PATH` when the target stands among belts/inserters, so navigate to a position ~2 tiles away instead; walking speed ~9 tiles/s; long unarmed routes through unmapped territory can be fatal where enemies exist. `TARGET_MISSING` when the target dies or changes surface. [README][memory]
- The managed server is headless: `screenshot` needs a connected graphical client (`just observe`) and a matching `script-output` directory. [README]
- `characterSpawn` workers persist in the save, are never cleaned up and cannot be removed by `characterReturn`; drain their inventories to a chest and leave them idle, or reuse them. [README][ticket-skill]

## 7. Rocket facts (Space Age 2.0.77)

- `rocket-part`: 1 processing-unit + 1 low-density-structure + 1 rocket-fuel, 3 s, category `rocket-building` [live recipe]. A launch needs **50 rocket parts** (`rocketParts 50`, status `waiting_to_launch_rocket`); not 100/1000. [memory; confirm with `inspect.settings.rocketSilo` when a silo exists]
- `rocket-silo` item recipe: 1000 steel + 1000 concrete + 200 processing-unit + 200 electric-engine-unit + 100 pipe, 30 s [live recipe].
- A cargo pod resting on a loaded silo is the **pre-launch** pod, not evidence of a launch. Sample position twice and check surfaces. [memory]
- `launchRocket` exists; stop on refusal and capture the engine reason. Mutation success alone does not prove a launch.

## 8. Tool-chain gotchas

- gqlcli needs CWD = repo root (`.gqlcli.json`) or `--url`; otherwise `404 page not found`. [live]
- `gqlcli script` default strict mode throws on any GraphQL error and loses the envelope; use `--strict=false` for mutation scripts. [live]
- gqlcli `script` has no `--jq` or `--format`; `-f` on `script` means `--file` (not format). `query`/`mutation` have `--jq`/`-f` output formats, but `--input` exists only on `mutation`; for a query use `--variables`. [live]
- Piping gqlcli's default toon output to external `jq` fails with `failed to parse: value expected`; pass `-f json` or use gqlcli's built-in `--jq`. Do not redirect stderr into the JSON output file. [live]
- Older `serve` builds reject `remove`/`mine`/`beltTake` through `EntityRefInput` with `flag provided but not defined: -surface` because the default `surface` is flattened into a backend flag. The working tree defines the flag and `TestCommandFlagsAcceptEntityRef` covers it; until `serve` is rebuilt, use `removeMany`/`placeMany` or the position form. [live][source fix]
- A rebuilt serve binary needs `gqlcli schema refresh`; the cache lives 10 minutes. [skill]
