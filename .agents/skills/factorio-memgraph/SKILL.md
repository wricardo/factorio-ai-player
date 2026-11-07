---
name: factorio-memgraph
description: "Factory graph in Memgraph from factorio-ai-player: refresh the snapshot over GraphQL (exportMapSnapshotToMemgraph) or sync inside the read (memgraph.query sync:true), introspect the loaded schema (memgraph.schema), run read-only Cypher (memgraph.query) with parameters and limits, and use a validated cookbook of topology, belt, inserter, chest, furnace, fluid, power, circuit and content queries."
---

# Factory graph in Memgraph (GraphQL + gqlcli)

The graph is a **snapshot**, not live state: every entity of one force plus engine-verified connections, exported from the live save into `.factorio/graph/snap-*`, then MERGEd into Memgraph. Read it back through the same GraphQL endpoint with one read-only Cypher statement at a time. Design and full schema: [`docs/memgraph-schema.html`](../../../docs/memgraph-schema.html). Source: `memgraph.graphqls`, `memgraph_api.go`, `graph_export*.go`, `graph_load.go`.

Authority: live schema (`gqlcli queries --args --desc`) > this skill > docs. A resolved query returns the graph **as of its snapshot**; always record the snapshot id and tick.

## When to use this

- Topology questions that are expensive entity-by-entity over RCON: what feeds a machine, where a belt run goes, which belts head nothing, splitter port routing, fluid/wire/rail adjacency.
- Whole-factory structural audits: unpowered machines, unfuelled burners, ghost inventory, network membership, beacon coverage.
- Cross-entity path questions: drill → belt run → inserter → machine chains.
- **Not** for live values (current inventory, rate, power satisfaction, status stuck for one tick), tick-critical reads, or anything that must include entities created after the export. Those come from the regular queries (`status`, `inspectMany`, `factoryBriefing`, `production`, `beltStretch`).

## Preconditions

```sh
cd /Users/wricardo/go/src/github.com/wricardo/factorio-ai-player   # gqlcli needs .gqlcli.json in CWD
just graph-up          # Memgraph bolt://127.0.0.1:7687 + Lab http://127.0.0.1:3000
gqlcli query '{ status { tick } }'          # serve must be up at 127.0.0.1:8080
gqlcli query '{ memgraph { schema { snapshot { id status nodes edges loadedAt } } } }'
```

- Bolt target is **server configuration**, never a GraphQL input: `MEMGRAPH_BOLT` (default `bolt://127.0.0.1:7687`), `MEMGRAPH_USER`, `MEMGRAPH_PASSWORD` are read by the `serve` process. The same env drives `just graph-load`.
- `just graph-sync`, `just graph-export`, `just graph-load --in DIR`, `just graph-shell` (mgconsole), `just graph-schema`, `just graph-down`, `just graph-wipe` (deletes the volume).
- Memgraph stack down → `memgraph.query` fails with "is the stack up? `just graph-up`".

## Operation 1 — refresh the snapshot (GraphQL mutation)

`exportMapSnapshotToMemgraph` runs export **and** load in one call: reads the live save, writes a snapshot directory, MERGEs it into Memgraph, then prunes every node/network/relationship in the exported scopes that the new snapshot does not contain. One export at a time — a concurrent call fails with `another Memgraph export is already running`.

**Shared-save effect:** exports briefly pause the game by default for all game reads, without changing entities. The prior pause state is restored on success, error or cancellation before snapshot disk writes and Memgraph loading; an already paused game stays paused. Opt out with mutation input `pause:false`, `just graph-sync --pause=false`, or `just graph-export --pause=false` to leave pause state untouched. Without pausing, reads can span game ticks and drift. Pausing does not exclude concurrent external edits from other clients.

```graphql
mutation Refresh($in: ExportMapSnapshotToMemgraphInput!) {
  exportMapSnapshotToMemgraph(input: $in) {
    snapshot directory status force nodes externalNodes edges
    prunedNodes prunedEdges prunedNetworks exportMs loadMs
    surfaces { surface status explanation tickBefore tickAfter staticExported mobileExported leaves }
    labelCounts { name count }
    edgeTypes { name count }
  }
}
```

```sh
gqlcli mutation --file refresh.graphql --variables '{"in":{"force":"player","surfaces":["nauvis"]}}' --format json
# or inline
gqlcli mutation '{ exportMapSnapshotToMemgraph { snapshot status nodes edges prunedNodes } }'
# leave the game's pause state untouched
gqlcli mutation '{ exportMapSnapshotToMemgraph(input: {pause:false}) { snapshot status nodes edges } }'
```

Input (all optional; defaults shown):

| Field | Type | Default | Meaning |
|---|---|---|---|
| `force` | String | `"player"` | force whose entities are exported |
| `surfaces` | [String] | null = every surface the force owns entities on | surfaces to export |
| `leafMax` | Int | `2000` (1..20000) | estimated entities per leaf read; each leaf is one RCON call that blocks the game thread for its duration |
| `concurrency` | Int | `4` (1..6) | parallel leaf reads (still capped by the shared `rconConcurrency` 6) |
| `pause` | Boolean | `true` | pause during all game reads; restore prior state before disk writes and load, also on error or cancellation |
| `includeLineLinks` | Boolean | `false` | also emit transport-line links as `BELT_OUT` edges with `kind:"line"` |
| `allowIncomplete` | Boolean | `false` | load a snapshot whose reconciliation found a hole |

Result semantics (verified live):

- `status` is the **worst** surface status: `complete` (before = after = exported), `drift` (world changed mid-scan; each node is correct at its own `tick`), `incomplete` (a counted entity was not exported).
- `incomplete` is **written but not loaded** unless `allowIncomplete`; the call fails and the previous Memgraph data stays. Rerun rather than loading a hole.
- `snapshot` id looks like `snap-20261105T035638.342Z`; `directory` is on the GraphQL host (`.factorio/graph/<snapshot>`); the newest 5 directories are kept and `LATEST` names the newest.
- `nodes` = Entity + Network rows written (networks are included); `edges` = relationships written; `pruned*` = stale rows removed from the exported scopes.
- `memgraph { schema { snapshot { tick ageSeconds } } }` sizes graph staleness before any structural claim: `tick` is the export's final read tick, so compare it with `status { tick }`. `null` means the snapshot was written before the field existed. `ageSeconds` is wall clock since `loadedAt`, which says nothing about game progress if the save was paused.
- Scopes are `surface|force`. Only the exported surfaces are replaced; other surfaces/forces are untouched (stale scopes can remain if you export a subset).
- The one-export lock is **per process**: two `serve` instances (or CLI plus serve) can export concurrently and prune each other's rows. Run exports from one process.
- Export cost grows with entity count and with leaf count; measured 4.3 s export + 69 ms load for 313 nodes/358 edges, docs: 6.1 s / 41.6 k entities. The shared save is paused during reads by default; without pausing, avoid active construction and rerun on drift.

Snapshots on disk (`nodes.jsonl`, `edges.jsonl`, `manifest.json`, `LATEST`) are reloadable without re-reading the game: `just graph-load --in .factorio/graph/<snap>`.

## Operation 2 — read the graph (read-only Cypher)

One statement per `memgraph.query` field; pass `sync:true` to export and load a fresh snapshot first, so the same call answers from current data. Batch several by aliasing fields in one GraphQL document — each alias is its **own read transaction**, so aliases are not one atomic view.

```graphql
query Run($cypher: String!, $params: Any) {
  memgraph {
    query(input: {cypher: $cypher, params: $params, limit: 200, timeoutMs: 10000}) {
      columns
      rows
      truncated
      durationMs
    }
  }
}
```

```sh
gqlcli query --file run.graphql \
  --variables '{"cypher":"MATCH (b:Belt) WHERE b.unit = $u RETURN b.id, b.x, b.y","params":{"u":19269}}' \
  --format json
# inline form
gqlcli query '{ memgraph { query(input:{cypher:"MATCH (m:Machine) RETURN m.unit, m.recipe, m.status", limit:5}) { columns rows truncated } } }'
# fresh data in one call: export+load first, then read
gqlcli query '{ memgraph { query(input:{sync:true, cypher:"MATCH (c:Chest) RETURN c.unit, c.items"}) { columns rows } } }'
```

`sync:true` runs after the statement and params are validated (an invalid statement never costs an export) and before `timeoutMs` starts. It uses the default paused export: the shared save is briefly paused for game reads, then its prior pause state is restored before disk writes and load, including on error or cancellation. It is slower (seconds), fails if another export is already running, and does not change the result shape — read `memgraph.schema.snapshot` if you also need the new snapshot id. To avoid pausing, call `exportMapSnapshotToMemgraph(input:{pause:false})` separately, then query without `sync:true`.

Input:

| Field | Type | Default | Notes |
|---|---|---|---|
| `cypher` | String! | — | one read-only statement |
| `params` | Any | null | JSON **object** (`{u:19269}`), referenced as `$u`; integral JSON numbers become int64, so `b.unit = $u` matches |
| `limit` | Int | `1000` | 1..10000; rows beyond it are dropped and `truncated` is true. Applied after the query starts — add `LIMIT`/`SKIP` in Cypher to bound server work |
| `timeoutMs` | Int | `10000` | 100..60000; deadline for the Bolt read transaction only — never the sync export |
| `sync` | Boolean | `false` | export a fresh snapshot from the live save and load it **before** running the statement, using the mutation's defaults (player, all owned surfaces, leafMax 2000, concurrency 4, pause true) |

Output: `columns` are the Cypher expression texts, `rows` are arrays aligned with columns. Values:

- `int`/`float`/`bool`/`string`/`null` pass through.
- A node becomes `{id, labels, properties}` and a relationship `{id, type, startId, endId, properties}` — those `id`s are **Memgraph element ids**, not the factory id. Address entities by `n.properties.id` (or return `n.id` in Cypher).
- A path becomes `{nodes, relationships}`. Temporal/spatial values are stringified.

Read-only guard (lexical, not a permission boundary): statement must start with `MATCH`, `OPTIONAL MATCH`, `WITH`, `UNWIND`, `RETURN`, `EXPLAIN` or `PROFILE`; must not contain `CREATE`, `MERGE`, `DELETE`, `DETACH`, `SET`, `REMOVE`, `DROP`, `FOREACH`, `CALL` or `LOAD` outside string literals/comments/quoted identifiers. Consequences: no MAGE procedures, no writes; use `just graph-shell` (mgconsole) or Lab for those, and use a read-only Memgraph user when the endpoint is shared.

## Operation 3 — introspect what is actually loaded

```graphql
query Schema {
  memgraph {
    schema {
      labels { name nodes properties { name count } }
      relationships { name edges properties { name count } }
      snapshot { id createdAt loadedAt status force surfaces nodes edges tick }
    }
  }
}
```

- Derived from loaded data, so a never-loaded label/type/property is absent; declared constraints/indexes are not reported (they live in `memgraph/schema.cypherl`; `SHOW INDEX INFO`/`SHOW CONSTRAINT INFO` via mgconsole).
- One read transaction; empty lists and null snapshot before the first completed load; fails when the stack is down.
- `snapshot` = latest **loaded** snapshot (`:Snapshot {loaded:true}` ordered by id).
- Labels count per label, so `Entity` (all entities) plus category labels overlap; `nodes` in a label is not a partition.

## Operation 4 — supply-chain reads (no Cypher)

```graphql
query Chain {
  memgraph {
    supplyChain(input: {unitNumber: "370"}) { summary { pathCount nodeCount kindCounts { name count } attention { reason node { id unitNumber status } } } outline truncated snapshot { ageSeconds } }
    ingredientCoverage(input: {unitNumber: "370"}) { gaps }
    downstreamChain(input: {unitNumber: "143"}) { summary { pathCount nodeCount attention { reason node { id unitNumber status } } } outline sinks { id } truncated }
  }
}
```

- `supplyChain`: upstream of any Machine, Chest or Belt by `unitNumber` (+ `surface`, default nauvis); kind is detected, other kinds are rejected. Read `summary` then `outline`; `nodes`/`paths` are the normalized detail (paths hold node ids). `resolution: STRETCH` is compact but coarse; BELT is exact. `graphCypher` returns the chain as path objects to paste into Lab Graph Results.
- `ingredientCoverage`: per recipe ingredient, the inserters whose source can provide it; `gaps` lists ingredients with no feed path. Topology, not stock.
- `downstreamChain`: the mirror of `supplyChain`: what a Machine, Chest or Belt feeds, through inserters and the belt line after the drop point, down to `sinks`. Outline uses `->` ("feeds"); `attention` flags `sink:belt` (a line nothing picks from). BELT resolution only. `supplyChain` is upstream only.
- `rootCause(input:{unitNumber})`: deepest upstream causes of a stall (`ORIGIN_BELT`/`NO_FEEDING_ARM`/`MACHINE_TROUBLE`); start here for "why is X stalled". `nominalRateBalance(input:{item,limit})`: nominal per-minute produced vs consumed per item, topology only (live stock is the top-level `itemBalance`). `removalImpact(input:{unitNumber})`: machines that lose an ingredient if the node goes; use before teardown. `chainCapacity(input:{unitNumber})`: nominal output and limiting stages of a machine's chain (throughput of belts/inserters not modelled). `routeBetween(input:{from,to})`: downstream routes A to B, `connected:false` when none. `itemSources(input:{item,nearX,nearY,limit})`: where an item is available. `beltStretches(input:{item,mixedOnly,endKind,nearX,nearY,limit})`: all belt stretches and the items per lane (sample, not rate); findings MIXED_LANE / NO_CONSUMER; no filters returns everything. `beltStretchMap(input:{area,filter,mode})` (area required, one char per tile, max 65536 tiles): ASCII rows + legend of the same stretches (join `rows` with newlines). `areaGraph(input:{area:{leftTopX,leftTopY,rightBottomX,rightBottomY}})`: entities in an XY box, the edges among them, connected `components` and `boundary` edges crossing the box (power edges off by default). Chest contents are `CONTAINS` edges (not `HOLDS`, which is machine/drill slots).
- `factoryHealth`, `powerAudit`, `fuelAudit`, `inserterAudit`, `sinkAudit` (each `input: {surface, limit}`) return one ranked finding list (`MemgraphAudit`: summary, per-code counts, findings with severity, message, related nodes, impact, truncated, snapshot). `factoryHealth` sweeps the whole graph (NO_FEEDING_ARM for item ingredients, ORIGIN_BELT, MACHINE_TROUBLE, NO_CONSUMER) sorted by downstream impact. `powerAudit`: UNPOWERED, ISOLATED_POLE, SINGLE_POLE_NETWORK, NO_GENERATOR (topology only, no overload). `fuelAudit`: NO_FUEL_ARM, FUEL_NOT_ARRIVING (an empty fuel slot is a sample; fuel list is a fixed heuristic). `inserterAudit`: VOID_PICKUP (infinity chest with no filters and `removeUnfilteredItems`, exported on Chest nodes), SELF_LOOP, BELT_TO_BELT, DEAD_SOURCE, FILTER_MISMATCH. `sinkAudit`: VOID_SINK, FULL_OUTPUT_NO_CONSUMER, BUFFER_FULL. `fluidChain(input: {unitNumber, maxNodes})` walks FLUID_LINK (nodes, links, sources, sinks, DEAD_END_PIPE, ISOLATED_FLUID_ENTITY, NO_SOURCE; no fluid levels). `cellSummary(input: {limit})` groups machines into production cells (a belt line feeding 3+ machines is a main belt and never joins cells) with recipes, counts and a status histogram.
- `factoryReport(input: {surface, limit})` is the one-call factory report and needs no argument. It merges factoryHealth, powerAudit, fuelAudit, inserterAudit and sinkAudit with structural checks (STRETCH_NO_INPUT, MIXED_LANE, INSERTER_NO_SOURCE, INSERTER_NO_DESTINATION, MACHINE_NO_RECIPE, GHOSTS_PENDING), the lab feed check (LAB_PACK_UNREACHABLE, a pack made in the factory that no feeding path delivers to a lab; every lab is assumed to want every made pack) and NOMINAL_DEFICIT (nominal consumption above nominal production, excluding items with infinity sources). Findings are de-duplicated by code and node, ordered by severity then impact, and carry the `audit` that raised them. `sections` lists every audit with its finding count or the error that stopped it, so a failed audit is visible; the report only errors when every audit fails. Topology and exported status at the snapshot, not a rate.
- `areaGraph(input:{area:{leftTopX,leftTopY,rightBottomX,rightBottomY}, includePower, includeBoundary, maxNodes})`: entities whose position is inside the box, the edges among them, connected `components`, and `boundary` edges crossing the box (`IN`/`OUT`). Live ±10 box: 121 nodes, 123 edges. Sides up to 1024 tiles.
- `beltStretches(input:{item,mixedOnly,endKind,nearX,nearY,limit})`: every belt stretch with items per lane; read `summary` then `outline`; `attention` lists `MIXED_LANE` and `NO_CONSUMER` (carries items, no `FLOWS_INTO` out, no picking inserter). Contents are the export-time lane sample and can be stale; check `snapshot`. `beltStretchMap(input:{area, filter, mode:ITEM|STRETCH})`: ASCII rows (north first, one char per tile, area required, ≤1024/side, ≤65536 tiles) plus `legend`.
- **Named gqlcli ops** (`.gqlcli.json`, run `gqlcli query --op <name> --variables '{...}'`): `memgraph-schema`, `memgraph-query` (`cypher`, `params`, `limit`, `timeoutMs`), `supply-chain`, `downstream-chain`, `ingredient-coverage`, `root-cause`, `nominal-rate-balance`, `removal-impact`, `chain-capacity`, `route-between` (`from`,`to`), `item-sources` (`item`, `nearX`, `nearY`), `area-graph` (`area`), `belt-stretches` (`input`), `belt-stretch-map` (`input` with `area`). Unit numbers are strings: `{"unitNumber":"370"}`.
- Response-shape rules for any new read: summary + outline first, entities once with id references, pre-classified findings, `truncated` and snapshot age on every result.

## Model

**Identity.** `id = surface:unit` when the entity has a unit number, else `surface:type:name:x:y` (resources have no unit). Unit numbers are global; the surface prefix keeps ids readable and cross-surface linked belts distinct.

**Node labels.** Every entity is `:Entity` plus category labels from engine type.

| Label | Engine types |
|---|---|
| `:Machine` | assembling-machine, furnace, lab, rocket-silo, agricultural-tower, asteroid-collector |
| `:MiningDrill` | mining-drill |
| `:Belt` | transport-belt, underground-belt, splitter/`:Splitter`, loader, loader-1x1, linked-belt, lane-splitter |
| `:BeltStretch` | derived node: one sequential belt run (not an engine entity) |
| `:Item` | derived node: one item or fluid per surface, `surface:item:name` (one node per name, quality lives on the edge) |
| `:Recipe` | derived node: one recipe per surface, `surface:recipe:name`, with `energy` and `category` |
| `:Technology` | derived node: one per technology per surface holding a lab, `surface:technology:name`, with `count`/`energy` per research unit; only technologies whose every pack some lab accepts are exported, and current research/queue/progress is not in the graph |
| `:Inserter` | inserter |
| `:Chest` | container, logistic-container, linked-container, infinity-container |
| `:FluidEntity` | pipe, pipe-to-ground, pump, offshore-pump, storage-tank, infinity-pipe, heat-exchanger, boiler, generator, fusion-generator |
| `:PowerSource` | boilers/generators/fusion/reactor/solar/accumulator/burner-generator/EEI/lightning-attractor |
| `:Pole` | electric-pole, power-switch |
| `:Beacon` · `:Roboport` | beacon · roboport |
| `:Rail` · `:RailSignal` · `:TrainStop` | all rail pieces · rail/chain-signal · train-stop |
| `:Combinator` | arithmetic/decider/constant/selector, power-switch, programmable-speaker, display-panel, lamp |
| `:Defense` | turrets, artillery, wall, gate, land-mine, radar |
| `:Vehicle` · `:RollingStock` · `:Character` | car, spider-vehicle, locomotives/wagons (also RollingStock) · character |
| `:Ghost` | entity-ghost, tile-ghost (`ghostName`, `ghostType`) |
| `:ResourceTile` | resource (only as a drill's mining target) |
| `:External` | edge endpoint no leaf owns: neutral resources, another force's entities |

Unlisted types are still `:Entity` with their `type`. Networks are `(:Network {id})` with `:ElectricNetwork` (`surface:electric:N`), `:LogisticNetwork` (`surface:force:logistic:N`), `:CircuitNetwork` (`surface:circuit:N`, `color`). Snapshots are `(:Snapshot {id})`.

**Entity properties:** `id`, `unit`, `name`, `type`, `surface`, `force`, `x`, `y`, `direction`, `quality`, `status` (engine status name, e.g. `working`, `no_fuel`, `no_ingredients`, `item_ingredient_shortage`), `recipe`, `recipeQuality`, `recipeSource` (`previous` for an idle furnace), `modules` (`name:quality:count`), `electricNetwork`, `logisticNetwork`, `train`, `mobile`, `tick` (tick of the read that owned it), `snapshot`, `scope`.

**Point-in-time contents sample** (taken at the leaf tick, not live): `fuel`, `input`, `output` (furnaces, assemblers, burner drills/boilers/inserters), `items` (chests), `left`/`right` (engine transport lines 1 and 2 of every belt-family entity), each a sorted list of `name:count` (`name@quality:count` when not normal). Matching `…Count` is present whenever the entity has that inventory — `fuelCount:0` = empty burner, absent `fuelCount` = no fuel inventory.

**Belt lanes:** `lanes` holds one `index|lineName|side|item:count+…` string per non-empty engine transport line (undergrounds expose surface and tunnel lines, splitters four), with `lineName`/`side` matching `beltTiles`/`beltStretch` (`left_line`, `right_underground_line`, …). `leftCount`/`rightCount` sum every line of that side (splitters use 5–8, undergrounds 3–4), while `left`/`right` stay lines 1–2; `mixedLeft`/`mixedRight` are true when a side holds more than one item or quality anywhere on that entity — use them instead of recomputing from `left`/`right`.

**Crafting counters:** `productsFinished` (cumulative), `craftingProgress` (0–1), and on furnaces `previousRecipe`; `previousRecipe <> recipe` means the furnace switched recipe.

**Drills:** `areaResources`/`areaAmounts` cover the whole `mining_area` as `name:tiles` / `name:amount`, with `areaTiles` and the `miningLeft/Top/Right/Bottom` corners. `MINES` is only the current `mining_target`, so a drill with two `areaResources` entries is the one mixing ores.

**Inserters:** `pickupX`/`pickupY`, `dropX`/`dropY`, `pickupLeftLane`/`pickupRightLane`, `useFilters`, `filterMode`, `filters`, `stackSizeOverride`, `held`, and `dropResolved:false` when nothing is under the drop.

**Belt stretches:** `(:BeltStretch {id})` is one sequential run — follows undergrounds and linked belts, continues through curves, and is always cut at splitters (each splitter output starts a new run); a side-load ends the loading run and joins it to the main one. Properties: `belts`, `undergrounds`, `headUnit`/`headX`/`headY`, `tailUnit`, `endKind` (`splitter`/`sideload`/`deadend`/`loader`/`loop`), per-item lists `itemsLeft`/`itemsRight` (`name:count` strings), `mixedLeft`/`mixedRight`.

**Production graph:** `RUNS` ties a machine to its `:Recipe`; `PRODUCES`/`CONSUMES` exist twice — `(:Recipe)-[:PRODUCES]->(:Item)` is per craft (`amount`, `probability`, `kind`), `(:Entity)-[:PRODUCES]->(:Item)` is per minute (`via` `recipe`/`mining`/`infinity`, `perMinute`). Rates are nameplates: `60 × craftingSpeed / recipe.energy × amount`, ignoring modules/beacons/productivity; drills divide by `miningTime` and multiply by `1 + force mining productivity`; infinity containers carry `count`/`mode` instead of a rate. `BURNS` = fuel items, `CONTAINS` = chest stock (`count`, `quality`), `HOLDS` = machine/drill slot contents (`slot` `input`/`output`/`fuel`, `count`, `quality`). Contents edges are leaf-tick samples, not live stock; linked-container contents are not exported.

**Splitter:** `:Belt`+`:Splitter` with `inputPriority`/`outputPriority` (`none`/`left`/`right`) and optional `filters` (`{index,name,quality}`). Its two outputs are separate `BELT_OUT` edges with `fromPort` `left`/`right`; the entering edge carries `toPort`. Left is counter-clockwise of travel.

**Relationships.** Every edge carries `key`, `snapshot`, `scope`.

| Type | From → to | Properties |
|---|---|---|
| `BELT_OUT` | belt → belt (or loader ↔ chest) | `kind` belt/underground/linked/loader/line; `fromPort`/`toPort`, `fromPortIndex`/`toPortIndex` |
| `PICKS_FROM` | inserter → source | `side` when source is a belt (side the inserter stands on) |
| `DROPS_TO` | inserter → destination | `side` when destination is a belt (lane the item lands on) |
| `DROPS_ON` | drill / machine → target | — |
| `MINES` | drill → `:ResourceTile` | — |
| `AFFECTS` | beacon → machine | — |
| `FLUID_LINK` | canonical sorted pair | `dirA`, `dirB`, `ports`; raw per-port declarations, never infer flow |
| `POWER_WIRE` / `CIRCUIT_WIRE` | canonical pair | `connectorA/B`; wire adds `color` |
| `RAIL_LINK` | canonical pair | — |
| `POWERED_BY` | entity → `:ElectricNetwork` | — |
| `IN_LOGISTIC_NETWORK` | entity → `:LogisticNetwork` | — |
| `ON_CIRCUIT` | entity → `:CircuitNetwork` | `connector`, `color` |
| `RUNS` | machine → `:Recipe` | `recipeSource` (`previous` for an idle furnace) |
| `PRODUCES` | `:Recipe` → `:Item` (per craft) · entity → `:Item` (per minute) | `amount`/`probability`/`kind` · `via`/`perMinute`/`kind`, infinity adds `count`/`mode` |
| `CONSUMES` | `:Recipe` → `:Item` · entity → `:Item` | `amount` · `perMinute`, `via:"recipe"` |
| `ACCEPTS` | `:Machine` lab → `:Item` | — (the lab prototype's `lab_inputs`) |
| `REQUIRES_PACK` | `:Technology` → `:Item` | `amount` (packs per research unit) |
| `PREREQUISITE` | `:Technology` → `:Technology` | —; edge omitted when the prerequisite was not exported |
| `UNLOCKS` | `:Technology` → `:Recipe` | —; only recipes the snapshot already has a `:Recipe` node for |
| `BURNS` | entity → `:Item` | — (fuel sample) |
| `CONTAINS` | `:Chest` → `:Item` | `count`, `quality` |
| `HOLDS` | machine / drill → `:Item` | `slot` (`input`/`output`/`fuel`), `count`, `quality` |
| `IN_STRETCH` | belt → `:BeltStretch` | `index` (0 = head) |
| `FEEDS_STRETCH` | inserter / drill / splitter port / side-loading tail belt → `:BeltStretch` | `atUnit`, `index`, `side`, `via`, `fromPort` |
| `TAKES_FROM_STRETCH` | inserter / splitter input port → `:BeltStretch` | `atUnit`, `index`, `side`, `toPort` |
| `FLOWS_INTO` | `:BeltStretch` → `:BeltStretch` | `via` (`splitter`/`sideload`), `viaUnit`, `atUnit`, `side`, `fromPort` |

Only entities that physically move items link to a stretch: machines and chests never do — reach them through their own inserter (`Inserter-[:PICKS_FROM]->machine`, `Inserter-[:DROPS_TO]->machine`).

Fluid, wire and rail edges are undirected in meaning: match them without an arrow. Belt, inserter, drill and beacon edges are directional.

**Coverage limits.** Captured: static entities of the force on exported surfaces (incl. ghosts, poles, rails, combinators, defenses), vehicles/characters/rolling stock (one-tick mobile pass), belt/inserter/drill/beacon/fluid/wire/rail links and network membership, recipe/modules/status/quality/direction, and the production graph (item/recipe nodes, per-minute nameplate rates, chest/machine contents). Not captured: robots and transient effects (counted as skipped), live stock and measured throughput (contents are leaf-tick samples only), linked-container contents, splitter lane routing, heat pipes, train schedules/paths, tiles, trees, enemies.

## Verification and staleness

```graphql
# freshness + coverage
{ memgraph { schema { snapshot { id status force surfaces nodes edges loadedAt createdAt tick } } } }
```

```graphql
# count cross-check against the latest snapshot; pass --variables '{"snap":"<snap-id>"}'
query Check($snap: String!) {
  memgraph {
    nodes: query(input:{cypher:"MATCH (n) WHERE (n:Entity OR n:Network) AND n.snapshot = $s RETURN count(n)", params:{s:$snap}}) { rows }
    edges: query(input:{cypher:"MATCH ()-[r]->() WHERE r.snapshot = $s RETURN count(r)", params:{s:$snap}}) { rows }
  }
  live: status { tick }
}
```

```graphql
# fresh in one call: export+load, then read (status.tick is a separate operation)
{ memgraph { query(input:{sync:true, cypher:"MATCH (e:Entity) RETURN min(e.tick) AS minTick, max(e.tick) AS maxTick"}) { rows } } }
```

- After a sync, expect `max(e.tick)` within seconds of `status.tick`; a large gap means the export was not the one you think (shared graph, failed sync) — recheck the snapshot id.
- `MATCH (s:Snapshot {loaded:true})` returns exactly one row (loading unsets `loaded` on earlier snapshots) while `:Snapshot` nodes without the filter accumulate one per load; still prefer `memgraph.schema.snapshot` or `ORDER BY s.id DESC LIMIT 1`.
- Compare `status.tick` with the snapshot's per-surface `tickAfter`/node `tick`; a large gap means re-sync before drawing conclusions, and a newer snapshot id means another actor refreshed the shared graph.
- Cross-check counts: `nodes` from the export = entities + networks; edges = relationship count. A mismatch means mixed scopes/snapshots — filter by `n.snapshot`/`r.snapshot`.
- Verify a surprising zero-row answer against the game (`entityCount`, `entities`, `beltStretch`) before calling it a graph defect: the export may be stale, the entity may be neutral/another force (only edges make it `:External`), or the type label may differ.

## Query cookbook (validated against the live graph)

Every statement below executed against a live loaded snapshot. Row counts are snapshot-dependent: at validation time there were no `FLUID_LINK`, `AFFECTS`, `CIRCUIT_WIRE`, `ON_CIRCUIT`, `IN_LOGISTIC_NETWORK` or `RAIL_LINK` edges and no `:FluidEntity`, `:Beacon` or `:Ghost` nodes, so the fluid, beacon, circuit, logistic-network and ghost statements returned zero rows; the status-filter, fuel-starved, dead-head, belt-bridge and unpowered statements also returned zero because no entity matched then. Those are syntax- and model-checked only. Belt, inserter, drill, machine-list, chest, power-network and path statements returned rows. Zero rows are always snapshot-dependent — check occupancy first:

Later live run with real fluids (`snap-20261105T050731.623Z`, tick 3.39 M, 980 nodes / 1104 edges, status `complete`): `FLUID_LINK` 222, `BELT_OUT` 577, `POWER_WIRE` 81, `DROPS_TO` 44, `PICKS_FROM` 45, `MINES` 4, `DROPS_ON` 4, `POWERED_BY` 149 — the fluid and belt statements below then returned rows. Note a `count(r)` over an undirected `-[r:FLUID_LINK]-` pattern reports **twice** the stored edge count (each pair is visited from both ends); `memgraphSchema { relationships { name edges } }` is the true number.

```cypher
MATCH ()-[r]->() RETURN type(r) AS type, count(*) AS n ORDER BY n DESC
```

Wrap any statement as shown in Operation 2. Return `n.id`/`m.unit`, never the map `id`.

**Latest snapshot and coverage**

```cypher
MATCH (s:Snapshot {loaded: true}) RETURN s.id, s.status, s.force, s.surfaces, s.nodes, s.edges, s.loadedAt
ORDER BY s.id DESC LIMIT 1
```

```cypher
MATCH (n:Entity) RETURN n.type AS type, count(*) AS n ORDER BY n DESC
```

**Address one entity / list machines**

```cypher
MATCH (e:Entity {unit: $u}) RETURN e.id, e.name, e.type, e.status, e.recipe, e.x, e.y
```

```cypher
MATCH (m:Machine) RETURN m.unit, m.name, m.recipe, m.status
ORDER BY m.status, m.unit
```

```cypher
MATCH (m:Machine) WHERE coalesce(m.status, '') <> 'working'
RETURN m.unit, m.name, m.recipe, m.status, m.fuelCount, m.inputCount, m.outputCount
```

A missing `status` property compares as null, so `m.status <> 'working'` alone silently drops machines that carry no status; `coalesce` keeps them.

**Machine content samples**

```cypher
MATCH (m:Machine) WHERE m.fuelCount = 0 RETURN m.unit, m.name, m.recipe, m.inputCount
```

```cypher
MATCH (c:Chest) WHERE c.itemsCount > 0 RETURN c.unit, c.name, c.items, c.itemsCount
ORDER BY c.itemsCount DESC LIMIT 50
```

**Chest role: belt→chest sink or chest→belt source**

```cypher
// sink: an inserter drops into the chest; src is its pickup
MATCH (c:Chest)<-[:DROPS_TO]-(i:Inserter)-[:PICKS_FROM]->(src)
RETURN c.unit, c.name, i.unit, i.status, src.name, src.type, src.unit ORDER BY c.unit, i.unit
```

```cypher
// source: an inserter takes from the chest and drops elsewhere
MATCH (c:Chest)<-[:PICKS_FROM]-(i:Inserter)-[:DROPS_TO]->(dst)
RETURN c.unit, c.name, i.unit, i.status, dst.name, dst.type, dst.unit ORDER BY c.unit, i.unit
```

```cypher
// loader links in both directions
MATCH (b:Belt)-[r:BELT_OUT]->(c:Chest) RETURN 'into chest' AS dir, b.unit AS belt, c.unit AS chest, r.kind AS kind
UNION
MATCH (c:Chest)-[r:BELT_OUT]->(b:Belt) RETURN 'out of chest' AS dir, b.unit AS belt, c.unit AS chest, r.kind AS kind
```

**Furnaces and their inserters**

```cypher
MATCH (f:Machine {type:"furnace"})
RETURN f.unit, f.name, f.recipe, f.status, f.x, f.y, f.fuelCount, f.inputCount, f.outputCount
ORDER BY f.unit
```

```cypher
// inserters feeding the furnace, with their pickup source
MATCH (f:Machine {type:"furnace"})<-[:DROPS_TO]-(i:Inserter)-[:PICKS_FROM]->(src)
RETURN f.unit, i.unit, i.status, src.name, src.unit ORDER BY f.unit, i.unit
```

```cypher
// inserters taking from the furnace, with their destination
MATCH (f:Machine {type:"furnace"})<-[:PICKS_FROM]-(i:Inserter)-[:DROPS_TO]->(dst)
RETURN f.unit, i.unit, i.status, dst.name, dst.unit ORDER BY f.unit, i.unit
```

An inserter's status describes **its own** pickup and drop. For a feed inserter (drop target = furnace), `waiting_for_space_in_destination` means the furnace buffer is full; `waiting_for_source_items` means its source had nothing at that tick. For an output inserter, `waiting_for_source_items` means the furnace offered no product — never read that as output-belt backpressure. Plain inserters carry no item filter, so ore-vs-fuel attribution needs the source belt's sampled contents.

**Everything feeding one machine**

```cypher
MATCH (m:Machine {unit: $u})<-[:DROPS_TO]-(i:Inserter)-[:PICKS_FROM]->(src)
OPTIONAL MATCH (up:Belt)-[:BELT_OUT*1..300]->(src)
OPTIONAL MATCH (up)<-[:DROPS_ON|DROPS_TO]-(feeder)
RETURN i.status, src.name, collect(DISTINCT feeder.name) AS feeders
```

**Drill → belt run → inserter → machine (path)**

```cypher
MATCH p = (d:MiningDrill)-[:DROPS_ON]->(:Belt)-[:BELT_OUT*0..300]->(b:Belt)
          <-[:PICKS_FROM]-(i:Inserter)-[:DROPS_TO]->(m:Machine)
MATCH (d)-[:MINES]->(ore:ResourceTile)
RETURN ore.name, d.id, i.id, m.name, m.recipe, length(p) AS hops
```

**Drills and their ore targets**

```cypher
MATCH (d:MiningDrill)-[:MINES]->(o:ResourceTile)
RETURN d.unit, d.status, o.name, o.x, o.y
```

**Belt heads nothing feeds**

```cypher
MATCH (b:Belt)
WHERE NOT ()-[:BELT_OUT]->(b) AND NOT ()-[:DROPS_ON]->(b) AND NOT ()-[:DROPS_TO]->(b)
RETURN b.id, b.x, b.y
```

**Belt stretches: what feeds a run, and where a machine's output goes**

```cypher
// every transferring entity that puts items into the stretch holding belt 815,
// through any upstream stretch, with the machine behind each inserter
MATCH (:Belt {unit: 815})-[:IN_STRETCH]->(s:BeltStretch)
MATCH (up:BeltStretch)-[:FLOWS_INTO*0..100]->(s)
MATCH (x)-[f:FEEDS_STRETCH]->(up) WHERE NOT x:Splitter AND f.via IS NULL
OPTIONAL MATCH (x:Inserter)-[:PICKS_FROM]->(src)
RETURN DISTINCT labels(x), x.name, x.unit, src.name, src.unit, src.recipe, up.id
```

```cypher
// where a machine's output ends up
MATCH (m:Machine {unit: $u})<-[:PICKS_FROM]-(i:Inserter)-[:FEEDS_STRETCH]->(s)
MATCH (s)-[:FLOWS_INTO*0..100]->(d)<-[:TAKES_FROM_STRETCH]-(t:Inserter)-[:DROPS_TO]->(c)
RETURN DISTINCT c.name, c.unit, c.recipe
```

**Drills over two ores / mixed lanes**

```cypher
MATCH (d:MiningDrill) WHERE size(d.areaResources) > 1
MATCH (d)-[:DROPS_ON]->(t) RETURN d.unit, d.x, d.y, d.areaResources, t.unit, t.name, t.recipe
```

```cypher
MATCH (b:Belt) WHERE b.mixedLeft OR b.mixedRight RETURN b.unit, b.type, b.x, b.y, b.lanes ORDER BY b.unit
```

**Belt tails and fan-out**

```cypher
MATCH (b:Belt) WHERE NOT (b)-[:BELT_OUT]->() RETURN b.id, b.x, b.y, b.unit
```

```cypher
MATCH (b:Belt)-[:BELT_OUT]->(next) RETURN b.unit, count(next) AS outs ORDER BY outs DESC LIMIT 20
```

**Splitter outputs with dead sides**

```cypher
MATCH (s:Splitter)-[r:BELT_OUT]->(b:Belt)
WITH s, r.fromPort AS port, b, size([(b)-[:BELT_OUT]->() | 1]) AS downstream
RETURN s.id, s.x, s.y, s.outputPriority, port, b.id,
       CASE WHEN downstream = 0 THEN 'dead side' ELSE 'flows' END
```

**Inserters bridging belt to belt (candidate defects)**

```cypher
MATCH (a:Belt)<-[:PICKS_FROM]-(i:Inserter)-[:DROPS_TO]->(b:Belt)
RETURN i.id, i.x, i.y
```

**Machines on no electric network**

```cypher
MATCH (m) WHERE (m:Machine OR m:Inserter OR m:MiningDrill)
  AND m.electricNetwork IS NULL AND NOT m.name STARTS WITH 'burner' AND NOT m.name STARTS WITH 'stone'
RETURN m.name, m.unit, m.x, m.y, m.status
```

**Power network membership**

```cypher
MATCH (e:Entity)-[:POWERED_BY]->(net:Network)
RETURN net.id, count(e) AS entities ORDER BY entities DESC
```

**Beacon coverage per machine**

```cypher
MATCH (m:Machine) OPTIONAL MATCH (b:Beacon)-[:AFFECTS]->(m)
RETURN m.recipe, m.id, count(b) AS beacons ORDER BY beacons
```

**Fluid neighbourhood (undirected)**

```cypher
MATCH (n:Entity {unit: $u})-[r:FLUID_LINK]-(other)
RETURN other.name, other.unit, r.dirA, r.dirB, r.ports
```

```cypher
MATCH (a:FluidEntity)-[r:FLUID_LINK]-(b:FluidEntity)
RETURN a.name, b.name, r.ports, r.dirA, r.dirB LIMIT 50
```

`FLUID_LINK` is stored once on the sorted endpoint pair; match it undirected (`-[r:FLUID_LINK]-`), never `->`, or you see only the canonical-direction half. Same for `POWER_WIRE`, `CIRCUIT_WIRE` and `RAIL_LINK`.

**Fluid component (the belt-components equivalent), anchored anywhere**

```cypher
MATCH (s:Entity {unit: $u})-[:FLUID_LINK*0..400]-(n)
WITH DISTINCT n RETURN n.type AS type, n.name AS name, count(*) AS c ORDER BY c DESC
```

Traverses the whole pipe run through machines, not just one hop: a `boiler` anchor returned 214 pipes + 2 pipe-to-ground + 2 steam engines + the boiler on a live water/steam plant. Variable-length `FLUID_LINK` is undirected only; an anchored `->` pattern silently yields one row.

```cypher
-- dangling fluid entities: unconnected probes, or a source whose port has no owner
MATCH (f:FluidEntity) WHERE NOT (f)-[:FLUID_LINK]-() RETURN f.name, f.x, f.y
```

Two hard limits, both verified:

- **A component crosses fluids.** The boiler links its water box and its steam box into the same component, so a component is geometry, not "one fluid network". Contents, temperature and `MIXED`/`INPUT_EMPTY`/`OUTPUT_FULL` come from the live `fluidSegments` query, never from the graph (graph nodes carry no fluid amounts at all).
- **Offshore pumps export no edges.** `fluidbox.get_pipe_connections` returns one connection with `target.valid = nil` and `target.owner = nil` (the water side), which `graph_fluids` (`graph_export_lua.go`) correctly skips. A pump-anchored traversal returns 1 row; that is the engine's own model, not an export defect — live `fluidSegments` also reports the pump box (`surface:box:<unit>:1`) as its own segment while the adjacent pipe segment holds the water. Anchor on a pipe or a consumer instead. Every other fluid entity, machines included, does report its ports.

**Circuit and logistic networks**

```cypher
MATCH (e:Entity)-[r:ON_CIRCUIT]->(net:Network)
RETURN net.id, r.connector, r.color, count(e) AS entities
```

```cypher
MATCH (e:Entity)-[:IN_LOGISTIC_NETWORK]->(net:Network)
RETURN net.id, count(e) AS entities ORDER BY entities DESC
```

**Ghost inventory**

```cypher
MATCH (g:Ghost) RETURN g.ghostName, g.ghostType, g.status, count(*) AS n
```

**Fan-in / fan-out ranking**

```cypher
MATCH (m:Machine)<-[:DROPS_TO]-(i:Inserter)
RETURN m.recipe, m.unit, count(i) AS feeders ORDER BY feeders DESC LIMIT 20
```

**Burner machines with no coal upstream (content sample)**

```cypher
MATCH (m:Machine) WHERE m.fuelCount = 0
  AND NOT EXISTS { MATCH (m)<-[:DROPS_TO]-(:Inserter)-[:PICKS_FROM]->(b:Belt)
                   WHERE any(x IN coalesce(b.left,[]) + coalesce(b.right,[]) WHERE x STARTS WITH 'coal') }
RETURN m.unit, m.recipe, m.inputCount
```

**Who produces an item (any producer, recipe or not)**

```cypher
MATCH (e:Entity)-[p:PRODUCES]->(i:Item {name: "iron-plate"})
RETURN e.name, e.unit, e.status, p.via, p.perMinute ORDER BY p.perMinute DESC
```

**Supply vs demand per item (nameplate rates)**

```cypher
MATCH (i:Item)
OPTIONAL MATCH ()-[p:PRODUCES]->(i) WHERE p.perMinute IS NOT NULL
WITH i, sum(p.perMinute) AS supply
OPTIONAL MATCH (:Entity)-[q:CONSUMES]->(i)
RETURN i.name, supply, sum(q.perMinute) AS demand
ORDER BY coalesce(demand,0) - coalesce(supply,0) DESC
```

**Recipe inputs/outputs of a machine, and where an item is stored**

```cypher
MATCH (m:Machine {unit: $u})-[:RUNS]->(r:Recipe)
OPTIONAL MATCH (r)-[c:CONSUMES]->(in:Item)
OPTIONAL MATCH (r)-[p:PRODUCES]->(out:Item)
RETURN r.name, r.energy,
       collect(DISTINCT in.name) AS inputs, collect(DISTINCT out.name) AS outputs
```

```cypher
MATCH (c:Chest)-[x:CONTAINS]->(:Item {name: "coal"})
RETURN c.name, c.unit, x.count, x.quality ORDER BY x.count DESC
```

## Memgraph Lab visualization recipes

Run these separately in Lab's query editor, then choose **Graph**. Return nodes,
relationships or paths, not just scalar columns, to draw connectivity. The queries
read the loaded snapshot only; they do not refresh it. Through `memgraph.query`,
paths serialize as `{nodes, relationships}`.

### Physical transport: individual belt tiles

```cypher
MATCH p = ()-[r]->()
WHERE type(r) IN [
  "PICKS_FROM",
  "DROPS_TO",
  "BELT_OUT",
  "FLOWS_INTO"
]
RETURN p;
```

Shows arms, source/destination entities and tile-to-tile belt links. `FLOWS_INTO`
adds stretch-to-stretch joins; `IN_STRETCH` is deliberately omitted, so tile and
stretch subgraphs may appear disconnected. Add `DROPS_ON` to include drills'
direct belt drops.

### Compact transport: replace belt tiles with stretches

```cypher
MATCH p = (inserter:Inserter)-[r]->(target)
WHERE
  (
    type(r) IN ["PICKS_FROM", "DROPS_TO"]
    AND NOT target:Belt
  )
  OR type(r) IN ["FEEDS_STRETCH", "TAKES_FROM_STRETCH"]
RETURN p
UNION
MATCH p = (:BeltStretch)-[:FLOWS_INTO]->(:BeltStretch)
RETURN p;
```

Keeps chest/machine ↔ arm connections and arm ↔ stretch transfers while hiding
individual belt tiles. This inserter-focused view omits direct drill/loader
transfers; use the next view for every non-belt transfer endpoint.

### Transport, recipes and items, including isolated stretches

```cypher
MATCH path = (source)-[relationship]->(target)
WHERE type(relationship) IN [
  "PICKS_FROM",
  "DROPS_TO",
  "FEEDS_STRETCH",
  "TAKES_FROM_STRETCH",
  "FLOWS_INTO",
  "RUNS",
  "CONSUMES",
  "PRODUCES",
  "CONTAINS",
  "HOLDS",
  "BURNS"
]
AND NOT source:Belt
AND NOT target:Belt
RETURN path AS graph
UNION
MATCH (stretch:BeltStretch)
RETURN stretch AS graph;
```

Both UNION branches return one column named `graph`; values may be paths or
nodes. The second branch preserves stretches with no matching relationship.
Item nodes are shared by name within a surface: many producers/consumers meeting
at one Item node does **not** prove a physical transport connection between them.

### Compare a blocked producer with a downstream feed belt

Example from the copper-feed investigation:

```cypher
MATCH (f:Machine {unit:185})
MATCH p = (b:Belt)-[:BELT_OUT*0..30]->(:Belt {unit:150})
          <-[:PICKS_FROM]-(i:Inserter)-[:DROPS_TO]->(m:Machine)
RETURN f, p;
```

Returns furnace 185 alongside upstream belt paths feeding the consumer of belt
150. No link from `f` to `p` is required: disconnected results are intentional.
These units are example snapshot identities, not universal factory constants.
For reuse, replace `185` with `$producer` and `150` with `$feedBelt`; pass
`params:{producer:185,feedBelt:150}` through `memgraph.query`.
If either required MATCH has no result, the whole query returns no rows.

### Reading the schema and arrows

| Layer | Nodes and links | Interpretation |
|---|---|---|
| Physical entities | `:Entity` plus `:Chest`, `:Inserter`, `:Machine`, `:Belt`, etc. | In-game objects, addressed by `id` or `unit`; `x`/`y`, status and `tick` are snapshot observations. |
| Belt abstraction | `(:Belt)-[:IN_STRETCH]->(:BeltStretch)` | Membership; `index` orders tiles. Not item movement. Stretches are derived runs, not game entities. |
| Transport | `PICKS_FROM`, `DROPS_TO`, `DROPS_ON`, `BELT_OUT`, `FEEDS_STRETCH`, `TAKES_FROM_STRETCH`, `FLOWS_INTO` | Physical transfer topology; connectivity does not prove items are moving. |
| Production | `(:Machine)-[:RUNS]->(:Recipe)-[:CONSUMES|PRODUCES]->(:Item)` | Recipe ingredients/products, not physical wiring. Entity → Item edges provide configured production/demand rates. |
| Stored contents | `(:Chest)-[:CONTAINS]->(:Item)`; machine/drill `HOLDS`; `BURNS` | Counts and quality, with `HOLDS.slot` identifying input/output/fuel. `BURNS` records sampled fuel, not measured burn rate. |
| Metadata | `:Snapshot`, `snapshot`, `scope`, `tick` | Latest loaded snapshot describes current graph contents; old Snapshot rows do not preserve historical entity versions. |

Critical arrow conventions:
- `(source)<-[:PICKS_FROM]-(arm)-[:DROPS_TO]->(destination)`.
- `(arm)-[:FEEDS_STRETCH]->(run)` means arm drops **onto** run.
- `(arm)-[:TAKES_FROM_STRETCH]->(run)` means arm picks **from** run:
  arrow points opposite item travel on this hop.
- `(upstream)-[:FLOWS_INTO]->(downstream)` follows item travel.
- Recipe → Item `amount` is expected yield for products (probability applied);
  `perMinute` on Entity → Item is nameplate capacity, not observed throughput.
- `Item.kind` distinguishes item/fluid; quality-specific inventory edges share
  one Item node and carry their own `quality`.

Use bounded paths like `*0..30` only for focused traces. Whole-factory views above
return each selected edge directly, avoiding enumeration of every possible path.
An API row limit or Lab display limit can hide part of a chain. Check
`truncated` before interpreting an absent connection.

## Rules

### MUST

1. **Check freshness first.** Read `memgraph.schema.snapshot` (or latest `:Snapshot {loaded:true}`) and compare with `status.tick`; refresh with the mutation or pass `sync:true` on the read before concluding anything structural about the current save.
2. **Record `snapshot` id and node `tick`** with every finding; a graph answer is dated evidence.
3. **Return stable ids** (`n.id`, `n.unit`) and address by them; the node map's `id` is a Memgraph element id.
4. **Use parameters** (`params:{u:...}`) instead of string interpolation for numbers/strings.
5. **Bound work:** `limit` ≤ needed, `timeoutMs` sane, `LIMIT`/`SKIP` in Cypher for large sets; prefer label+property anchors so indexes (`Entity(id)`, `Entity(unit/name/type/status)`, `Machine(recipe)`) apply.
6. **Treat `incomplete` as failure.** Rerun the export; do not load a hole.
7. **Verify a surprising answer** against live queries before reporting a graph or API defect.

### SHOULD

1. Export only the surfaces you need when debugging a subset; the mutation prunes only exported scopes.
2. Use `includeLineLinks` only when transport-line links matter (extra `BELT_OUT` `kind:"line"` edges).
3. Prefer the graph for topology; prefer live queries for rate, contents, status and freshly built entities.
4. Ask several questions in one document via aliases (each alias is a separate read transaction).
5. Use `explain`/`profile` (allowed start keywords) to check a slow statement's plan.

### MUST NOT

1. **Do not read the graph as live state.** Any entity change, recipe change or rebuild after the export is invisible.
2. **Do not infer throughput, lane purity or flow direction** from wiring: `BELT_OUT` is geometry, `FLUID_LINK` is undirected raw port data, `left`/`right` samples are point-in-time and belt side mapping is unverified.
3. **Do not use `CALL`/MAGE, write clauses, or DDL through `memgraph.query`** — the guard rejects them; writes belong in mgconsole/Lab, and schema DDL in `memgraph/schema.cypherl`.
4. **Do not treat `count(:Snapshot)` or an unfiltered `MATCH (n)` as the current factory** — stale Snapshot metadata and stale unexported-surface scopes persist.
5. **Do not run the export mutation or `sync:true` concurrently** (the lock is per process, so two serve processes still race) or during active construction where `drift` would mislead; rerun instead.
6. **Do not claim schema completeness from `memgraph.schema`** — it is derived from loaded data and omits declared indexes/constraints.

## Troubleshooting

| Symptom | Cause / fix |
|---|---|
| `Memgraph query failed (is the stack up? just graph-up)` | stack down or wrong Bolt env on the `serve` process |
| `memgraph.query is read-only: DROP/SET/… is not allowed` on a pure MATCH | a forbidden keyword appears as an **alias** or bare identifier, e.g. `RETURN dst.id AS drop`; rename the alias (`dropId`). Property/label names after `.`/`:` are exempt |
| `params must be a JSON object` | pass `{...}` (or a JSON object variable), not a scalar/array |
| `limit must be between 1 and 10000` / `timeoutMs must be between 100 and 60000` | clamp inputs |
| `truncated: true` | hit `limit`; add Cypher `LIMIT`/`SKIP` or raise up to 10000 |
| `Memgraph query exceeded timeoutMs=…` | tighten anchors/indexes, add `LIMIT`, raise `timeoutMs` |
| empty `memgraph.schema.labels` and null snapshot | no completed load yet |
| `sync before memgraph.query: another Memgraph export is already running` | sync requested while an export holds the lock; retry after it finishes, or drop `sync` and read the existing snapshot |
| `sync before memgraph.query: snapshot … is incomplete …` | the sync export found a hole and was not loaded; the query aborted and the previous graph is unchanged — rerun |
| `sync:true` seems to hang | the export is not covered by `timeoutMs`; it takes seconds and blocks the game thread per leaf read. Use the mutation with `surfaces`/`leafMax` to narrow it |
| `another Memgraph export is already running` | serialize refresh calls; the lock is per process, so also make sure only one serve/CLI exports at a time |
| snapshot status `drift` | world changed mid-scan; rerun for an exact view |
| snapshot status `incomplete` | hole; export failed before load — rerun (or `allowIncomplete` consciously) |
| zero rows where entities exist | stale snapshot, wrong scope/surface, entity is neutral/other force (only `:External` if it participates in an edge), or wrong label/type |
| value is a stringified timestamp/point | temporal/spatial Bolt values are stringified by design |

## Quick reference: full operation surface

```sh
gqlcli queries --args --desc | grep -i memgraph
gqlcli mutations --args --desc | grep -i memgraph
gqlcli describe Query.memgraph --depth 1
gqlcli describe Memgraph --depth 1
gqlcli describe Memgraph.query --desc
gqlcli describe Mutation.exportMapSnapshotToMemgraph --desc
```

```graphql
extend type Query {
  memgraph: Memgraph!
}
type Memgraph {
  schema: MemgraphSchema!
  query(input: MemgraphQueryInput!): MemgraphQueryResult!
  supplyChain / ingredientCoverage / downstreamChain   # trace reads
  rootCause / nominalRateBalance / removalImpact / chainCapacity / routeBetween / itemSources   # insights
  areaGraph   # entities in a box and their edges
}
extend type Mutation {
  exportMapSnapshotToMemgraph(input: ExportMapSnapshotToMemgraphInput! = {}): MemgraphExportResult!
}
input MemgraphQueryInput {
  cypher: String!  params: Any  limit: Int! = 1000  timeoutMs: Int! = 10000  sync: Boolean! = false
}
```

## Validated field-by-field (2026-11-06, serve on the working tree)

All eleven fields were exercised against a live `complete` snapshot: `schema`, `query`, `supplyChain`, `ingredientCoverage`, `downstreamChain`, `rootCause`, `nominalRateBalance`, `removalImpact`, `chainCapacity`, `routeBetween`, `itemSources`. The six insights fields return `panic: not implemented` only when the running `serve` binary predates their resolver wiring (`graphql_memgraph_insights.resolvers.go`); a stale process is not an API defect — rebuild a serve (`go build -o /tmp/fap . && /tmp/fap serve --listen 127.0.0.1:8082`) before judging a field.

Behaviour confirmed useful (use these, they are cheap and exact):

- `supplyChain(unitNumber, resolution:BELT|STRETCH)`: root may be a Machine, Chest or Belt; other kinds are rejected with a clear message; unknown unit → "no node with unit N ... (it may not exist, or the snapshot predates it)". `outline` is the read-first answer (`<-` = is fed by, `arm N [status]`, `belts A..B (n)`); `graphCypher` runs unchanged through `memgraph.query` (returns path rows).
- `downstreamChain(unitNumber)`: BELT resolution only; sinks + `sink:belt` attention catch a lane nothing picks from.
- `ingredientCoverage(unitNumber, upstream)`: per-ingredient `feedingArms` + a `gaps` list; it attributes an arm to an ingredient from the source's recipe/producer as well as sampled lane contents, so an empty lane does not hide a feed path.
- `memgraph.query` guards behave as documented: `limit` 1..10000, `timeoutMs` 100..60000, `params` must be a JSON object (integral JSON numbers become int64, so `{"u":"391"}` silently matches nothing while `{"u":391}` matches), `CALL` as the start keyword is rejected, forbidden keywords inside string literals/comments/backtick identifiers are allowed, `truncated` is set on row-limit cut, and every result carries `snapshot`.
- **A Cypher syntax error is reported as "Memgraph query failed (is the stack up? `just graph-up`)"** because `memgraph_api.go` wraps every Bolt error; read the Neo4jError text, not the hint (ticket factorio-ai-player-6d6).
- **`MATCH (n) RETURN ...` and the `Snapshot` label are polluted**: the graph held 1832 `:Snapshot` nodes for 465 factory nodes (one per load, never pruned) while `{loaded:true}` correctly returns one. Filter by label or `snapshot` property, never trust a bare node count (ticket factorio-ai-player-yxl).
- `BeltStretch.itemsLeft/itemsRight` are lists of `name:count` strings (`['iron-gear-wheel:87']`), not scalars; `mixedLeft/Right` are absent unless true.
- Trace reads are fast once the JIT warms (≈0.1–0.3 s for 30-row statements after the first call); the first statement of a session can take seconds, so do not report slowness from a single call.

How this was validated (repeat after a schema change): `gqlcli describe Memgraph --depth 1`, then one document per field with a real unit (a working machine, a chest root, a belt root), plus negative cases (`unitNumber:"43"` inserter, `999999`, `maxHops:0`, `maxPaths:0`, `limit:0/10001`). Cross-check one chain against live `inspectMany` pickup/drop so the graph is proven, not assumed.


Validated insights behaviour (fresh serve, snap-20261106T180445Z, after the new green cell existed):

- `rootCause(143)` → `stalled:true` with `causes:[]` plus a summary that hedges "may be blocked on its output"; `rootCause(370)` → `stalled:false` with one `ORIGIN_BELT` cause whose `routeText` is `370 <- arm 390 <- belt 391`. The cause set is wiring-only (ORIGIN_BELT / NO_FEEDING_ARM / MACHINE_TROUBLE), so a wired-but-starved chain comes back causeless; `stalled` follows the root's own status only (docs reconciled, ticket 32p).
- `nominalRateBalance` returns per-item produced/consumed with `unboundedSources` counting infinity chests so ore shows produced 0; deficits are plain sentences (`iron-plate: net -873.75/min (produces 206.25 by 11, consumes 1080 by 7)`).
- `removalImpact(707)` correctly listed only the two consumers that lose a route (708 loses iron-gear-wheel, 709 loses transport-belt).
- `chainCapacity` numbers are nameplate-exact: logistic-science-pack recipe `energy:6` at AM1 speed 0.5 → 5/min; transport-belt `energy:0.5` with `products:2` → 120/min; gear 60/min. `limiting` is empty when no stage caps *below* the root's nominal, so an exactly-matched supplier is not flagged. Do not misread 5/min logistic science as a starved or backpressured cell: that is exactly one assembling-machine-1 running at nameplate, and live `production` agrees with `chainCapacity`; the lever is machine count (~12 AM1, or AM2/AM3, per 60 packs/min). Same trap for any 6 s science recipe.
- `routeBetween(370,709)` walked the real new build (370 → arm 715 → belts 722/758/…/765 → arm 766/770 → 709) — a good end-to-end proof that an export captured freshly placed entities.
- `itemSources(item, nearX/nearY)` orders purely by distance (belts can outrank producers); belt sources are `BeltStretch` nodes with `unitNumber:null`, so read `node.id`. Unknown item returns an empty list, not an error.
