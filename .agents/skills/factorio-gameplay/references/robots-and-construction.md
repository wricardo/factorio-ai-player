# Construction: blueprints, ghosts, robots, mall, teardown, repair

Condensed from the project's robot-mall, roboport-expansion, safe-teardown and damage-rebuild work. Schemas and live reads win. Consult `automation-and-mall.md` for mall strategy; this file is the executable detail.

## 1. Blueprints and ghosts

- `createBlueprint(area)` exports blueprintable entities (`includeModules/includeFuel` default on; tiles/trains/station names off). `createBlueprintFromEntities` builds a string from explicit blueprint-relative entities (name, position, direction, quality, recipe, mirror). Strings limited to 2,000,000 chars.
- `placeBlueprint` places **entity ghosts only**; it consumes no items. Position is the top-left corner of the blueprint; rotate with `direction`, which must be 0, 4, 8 or 12 (a blueprint only turns in 90° steps, anything else is rejected before RCON). The result reports `ghosts`, the blueprint's `entityCount`, and `skipped` when the engine refused entities.
- `placeGhosts(entities:[{name,position,direction?,quality?,recipe?,recipeQuality?}], tiles:[{name,x,y}])` plans the same ghosts directly at absolute map positions with no blueprint string: up to 10000 entities and 20000 tiles per call, no items consumed, no reach or collision check. It is the primitive for laying out a build plan; `ghosts` then reads the backlog (omit `area` for the whole surface).
- Audit a plan before building: `inspect` on an `entity-ghost` target `{entity:"entity-ghost", unitNumber:"6867"}` reports `ghostName` (prototype it would build), `ghostQuality`, `ghostRecipe` (crafting-machine ghosts) and `direction`. All three ghost fields are null for a real entity, so `ghostName ?? name` compares ghosts and built entities in one list. `inspectMany` returns the same fields per row for a whole plan.
- Query `ghosts` for backlog; omit `area` to count the whole surface, or supply a box of at most 1024 tiles per side. Entries carry `placementAlternatives { name count available missing }` and `buildable` (observed stock only; it reserves nothing and ignores robot coverage and reach).
- Real construction needs: roboport coverage, construction robots, materials in the logistic network, power. Poll `ghosts` and `constructionOrders` until backlog is gone and entities inspect correctly.
- `buildGhosts` revives ghosts without construction robots (modes `REVIVE` default and `SILENT`), so the build lands in one tick, but it charges the build: the ghost cost comes from the actor main inventory first, then an optional `source` entity inventory (`sourceInventory` defaults to `CHEST`), at the ghost quality and all or nothing per ghost. `missing` is the stock that was absent, `MATERIALS` marks those ghosts, a blocked tile reports `COLLISION` and refunds, and `consumed`/`collidedItems` report what was spent and what ground items were displaced. An actor or source is required.
- Destroyed entities leave **both** a remnant corpse (name contains `remnant`) and an entity ghost; the ghost list is the exact rebuild list.
- `upgradeArea(area, mappings:[{from,to,quality?}])` marks tier upgrades through `order_upgrade` (up to 1000 mappings; the 2.0 planner item cannot carry mappings), reporting `MARKED`/`ALREADY_MARKED`/`LOOKUP`/`REFUSED` rows; `cancelUpgradeArea` reverses them. `constructionOrders(state:UPGRADE)` lists what is queued.

### Blueprint catalog books

Named designs live in a **script-owned catalog**, not a player inventory: it works with no character, survives save/load, and is read/written only through `blueprintBooks` / `blueprintBook` and the book mutations (full operation list and semantics in `api-map.md` section 3). Use it instead of a player book when the save has no connected character or when a design must persist for later workers.

- Create with `createBlueprintBook({name<=64, label?, description?})` (max 200 books), or store a whole external book with `importBlueprintBook({name, blueprint, overwrite})`; a single blueprint string is rejected there and belongs in `addBlueprintToBook`.
- `addBlueprintToBook` writes one **single** blueprint, `addAreaToBook` exports a live area (<=1024 tiles per side) straight into an entry; `updateBlueprintInBook` rewrites in place; `moveBlueprintInBook` swaps two slots; `removeBlueprintFromBook` clears a slot without shifting later indices.
- `placeBlueprintFromBook({name, index?, position, direction})` places ghosts of one entry (default `activeIndex`) and consumes no items, exactly like `placeBlueprint`; `giveBlueprintBook({name, player})` copies the whole book into a connected player's main inventory when a human needs to use it.
- Verify by re-reading `blueprintBook(name:...){ entryCount slotCount entries { index name label entityCount tileCount } }`: `BlueprintBookEntry` carries counts, not entities, and `blueprint` only appears with `includeBlueprint:true`. The engine migrates older strings to the running version on import, so per-entry `entityCount` plus entity-name diff is the only real proof.

## 2. Roboport network expansion

Target: contiguous, powered roboport network with robots and materials, proved by a live ghost build.

1. **Survey:** `logisticNetworks(surface, force)` for cells, `constructionRobots{total available}`, `logisticRobots`, per-item stock/requester/shortfall. Existing roboports via `entities(type:"roboport")` (`worldLogistics` does not support roboport). `electricNetworks` + `powerConsumers` for power.
2. **Grid plan:** roboport supply 50x50 (radius 25); adjacent roboport centres must be < 50 apart on **both** axes to merge networks (47-48 ideal). Construction radius 55. Probe candidate footprints (`probePlacement prototype:"roboport" mode:GRID`) per cell; accept small offsets if the whole graph stays connected (|dx|<50 AND |dy|<50 for each pair).
3. **Materials:** roboport = 45 steel + 45 gears + 45 advanced circuits (5 s); construction robot = 1 flying-robot-frame + 2 circuits. Advanced circuits are the bottleneck: cable 0.5 s (2 per copper), AC 6 s each; spread across many `characterSpawn` workers (`transfer` full ingredient sets per worker, then `craft(wait:false)`); `begin_crafting` consumes all queued ingredients up front.
4. **Place + power:** up to 9-18 per `placeMany`, check `errors`. One **big-electric-pole** per roboport within 4 tiles (its 4x4 supply then covers the 4x4 roboport) plus a midpoint big pole per ~48-tile link (big<->big 32; big<->medium only 9). Verify `powerConsumers` `connectedToProducingNetwork:true`; `low_power` right after placement is transient while buffers charge.
5. **Robots + stock:** `transfer` `construction-robot` / `logistic-robot` with `inventory:ROBO_PORT_ROBOT` (~2 per port). Craft `passive-provider-chest` (steel-chest + 3 circuits + 1 advanced circuit), place inside coverage, bulk-load build items. Note requester chests in a merged network drain new provider stock; use buffers or limits.
6. **Prove:** `createBlueprintFromEntities` with 3-4 belts, probe free tiles in coverage, `placeBlueprint`, poll `ghosts` until 0 and entities exist (real bots pulled belts within ~10 s in the live test).

## 3. Robot-fed mall cell

Cell geometry (assembler `(ax,ay)`, 3x3; 7x3 footprint, rows 3 apart):

```
requester chest (ax-3,ay) -> input inserter (ax-2,ay) dir 12 -> assembler (ax,ay) -> output inserter (ax+2,ay) dir 12 -> passive provider chest (ax+3,ay)
```

Direction rule: inserter direction points to pickup (`12` = pickup west). Use fast inserters on inputs.

- **Script-set requester slots fill to `min`, not `max`.** Always set `min = max = N`:
  `setLogisticRequests(input:{actor, target:{entity:"requester-chest", unitNumber:"N"}, sections:[{slots:[{index:1,name:"iron-plate",min:50,max:50}]}]})`.
- If the player controller blocks `player:` actors (e.g. "actor has no main inventory"), use `actor:{character:"<unit>"}`.
- Upstream feeders (gears, cables, circuits, sticks) need their own belt-fed cells; otherwise mall products drain storage in minutes. Bound every provider chest (`setContainerBar` bar 4; bar 3 can stall inserters at ~100 items for some stacks).
- A normal inserter feeds ~1 item/s: mall throughput is inserter- and robot-limited.
- Verify: recipes set, requester/provider counts rise, remove a known quantity and watch refill, robots counts, power `connectedToProducingNetwork:true`, a ghost test completes.
- `logisticNetworks.contents` can be negative while demand is unmet; do not read it as stock.
- Crafting may be free in sandbox saves; do not infer ingredient use from craft success. Read `cheatMode` on `player` to tell.

## 4. Safe teardown of a block

Never box-delete by name over a broad area.

1. **Map:** `machines(type)` gives positions; `entities(area,name)` gives unit numbers; `factoryGraph(area<=1024)` gives machines+belts+inserters+edges in one snapshot.
2. **Prove ownership of each belt line:** `beltRun` per seed; dedicated when `boundaryEdges` is empty and sources/sinks are only inserters touching removed machines. Closed loops fed by chests are infrastructure, not removable bus.
3. **Remove in layers:** machines (`removeMany` with `{entity, unitNumber}`; non-minable script-placed ones: `deconstructArea(instant:true, names:[...])`, which spills contents and never voids items); feeds (`deconstructArea` over a box verified by a `belts(area)` count first); inserters/chests; second pass for dead inserters (`dropPosition` hits nothing). `deconstructArea` scans the force plus neutral, so trees, rocks, cliffs and ground items are selectable there too (characters and ghosts never are).
4. **Verify:** same census returns only poles/ground items; `production(window)` for removed items is 0; `stock` shows recovered items.
5. **Concurrency:** re-query before each boxed removal; unexpected new unit numbers are someone else's work: never delete them.

## 5. Repair and rebuild after damage

1. `safety(damagedLimit)` for damaged entities; `ghosts` for destroyed ones, bounded by an area when the damage is local (neutral rocks are collateral).
2. Rebuild with real items: batch same-item `placeMany`; placing the entity fulfils the ghost and clears the remnant.
3. Repair packs are actor-inventory only. `craft repair-pack` (2 gears + 2 circuits). `repair` needs `target:{unitNumber, entity}` (+ `count`); a normal pack restores 600 HP (durability 300 × repair speed 2).
4. Verify: `ghosts` -> 0, `safety damaged` -> 0 for player entities, machines leave `no_power` (a destroyed pole is the common cause), pumpjacks leave `waiting_for_space_in_destination`.
5. If spawners survive and defences are absent, rebuilding is temporary: report the gap (see `combat-and-defense.md`).
