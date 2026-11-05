---
name: factorio-memgraph
description: "Factory graph in Memgraph from factorio-ai-player: refresh the snapshot over GraphQL (exportMapSnapshotToMemgraph) or sync inside the read (queryMemgraph sync:true), introspect the loaded schema (memgraphSchema), run read-only Cypher (queryMemgraph) with parameters and limits, and use a validated cookbook of topology, belt, inserter, chest, furnace, fluid, power, circuit and content queries."
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
gqlcli query '{ memgraphSchema { snapshot { id status nodes edges loadedAt } } }'
```

- Bolt target is **server configuration**, never a GraphQL input: `MEMGRAPH_BOLT` (default `bolt://127.0.0.1:7687`), `MEMGRAPH_USER`, `MEMGRAPH_PASSWORD` are read by the `serve` process. The same env drives `just graph-load`.
- `just graph-sync`, `just graph-export`, `just graph-load --in DIR`, `just graph-shell` (mgconsole), `just graph-schema`, `just graph-down`, `just graph-wipe` (deletes the volume).
- Memgraph stack down → `queryMemgraph` fails with "is the stack up? `just graph-up`".

## Operation 1 — refresh the snapshot (GraphQL mutation)

`exportMapSnapshotToMemgraph` runs export **and** load in one call: reads the live save, writes a snapshot directory, MERGEs it into Memgraph, then prunes every node/network/relationship in the exported scopes that the new snapshot does not contain. One export at a time — a concurrent call fails with `another Memgraph export is already running`.

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
```

Input (all optional; defaults shown):

| Field | Type | Default | Meaning |
|---|---|---|---|
| `force` | String | `"player"` | force whose entities are exported |
| `surfaces` | [String] | null = every surface the force owns entities on | surfaces to export |
| `leafMax` | Int | `2000` (1..20000) | estimated entities per leaf read; each leaf is one RCON call that blocks the game thread for its duration |
| `concurrency` | Int | `4` (1..6) | parallel leaf reads (still capped by the shared `rconConcurrency` 6) |
| `includeLineLinks` | Boolean | `false` | also emit transport-line links as `BELT_OUT` edges with `kind:"line"` |
| `allowIncomplete` | Boolean | `false` | load a snapshot whose reconciliation found a hole |

Result semantics (verified live):

- `status` is the **worst** surface status: `complete` (before = after = exported), `drift` (world changed mid-scan; each node is correct at its own `tick`), `incomplete` (a counted entity was not exported).
- `incomplete` is **written but not loaded** unless `allowIncomplete`; the call fails and the previous Memgraph data stays. Rerun rather than loading a hole.
- `snapshot` id looks like `snap-20261105T035638.342Z`; `directory` is on the GraphQL host (`.factorio/graph/<snapshot>`); the newest 5 directories are kept and `LATEST` names the newest.
- `nodes` = Entity + Network rows written (networks are included); `edges` = relationships written; `pruned*` = stale rows removed from the exported scopes.
- Scopes are `surface|force`. Only the exported surfaces are replaced; other surfaces/forces are untouched (stale scopes can remain if you export a subset).
- The one-export lock is **per process**: two `serve` instances (or CLI plus serve) can export concurrently and prune each other's rows. Run exports from one process.
- Export cost grows with entity count and with leaf count; measured 4.3 s export + 69 ms load for 313 nodes/358 edges, docs: 6.1 s / 41.6 k entities. Do not sync during active construction; drift means rerun.

Snapshots on disk (`nodes.jsonl`, `edges.jsonl`, `manifest.json`, `LATEST`) are reloadable without re-reading the game: `just graph-load --in .factorio/graph/<snap>`.

## Operation 2 — read the graph (read-only Cypher)

One statement per `queryMemgraph` field; pass `sync:true` to export and load a fresh snapshot first, so the same call answers from current data. Batch several by aliasing fields in one GraphQL document — each alias is its **own read transaction**, so aliases are not one atomic view.

```graphql
query Run($cypher: String!, $params: Any) {
  queryMemgraph(input: {cypher: $cypher, params: $params, limit: 200, timeoutMs: 10000}) {
    columns
    rows
    truncated
    durationMs
  }
}
```

```sh
gqlcli query --file run.graphql \
  --variables '{"cypher":"MATCH (b:Belt) WHERE b.unit = $u RETURN b.id, b.x, b.y","params":{"u":19269}}' \
  --format json
# inline form
gqlcli query '{ queryMemgraph(input:{cypher:"MATCH (m:Machine) RETURN m.unit, m.recipe, m.status", limit:5}) { columns rows truncated } }'
# fresh data in one call: export+load first, then read
gqlcli query '{ queryMemgraph(input:{sync:true, cypher:"MATCH (c:Chest) RETURN c.unit, c.items"}) { columns rows } }'
```

`sync:true` runs after the statement and params are validated (an invalid statement never costs an export) and before `timeoutMs` starts. It is slower (seconds; per-leaf game-thread stalls), fails if another export is already running, and does not change the result shape — read `memgraphSchema.snapshot` if you also need the new snapshot id.

Input:

| Field | Type | Default | Notes |
|---|---|---|---|
| `cypher` | String! | — | one read-only statement |
| `params` | Any | null | JSON **object** (`{u:19269}`), referenced as `$u`; integral JSON numbers become int64, so `b.unit = $u` matches |
| `limit` | Int | `1000` | 1..10000; rows beyond it are dropped and `truncated` is true. Applied after the query starts — add `LIMIT`/`SKIP` in Cypher to bound server work |
| `timeoutMs` | Int | `10000` | 100..60000; deadline for the Bolt read transaction only — never the sync export |
| `sync` | Boolean | `false` | export a fresh snapshot from the live save and load it **before** running the statement, using the mutation's defaults (player, all owned surfaces, leafMax 2000, concurrency 4) |

Output: `columns` are the Cypher expression texts, `rows` are arrays aligned with columns. Values:

- `int`/`float`/`bool`/`string`/`null` pass through.
- A node becomes `{id, labels, properties}` and a relationship `{id, type, startId, endId, properties}` — those `id`s are **Memgraph element ids**, not the factory id. Address entities by `n.properties.id` (or return `n.id` in Cypher).
- A path becomes `{nodes, relationships}`. Temporal/spatial values are stringified.

Read-only guard (lexical, not a permission boundary): statement must start with `MATCH`, `OPTIONAL MATCH`, `WITH`, `UNWIND`, `RETURN`, `EXPLAIN` or `PROFILE`; must not contain `CREATE`, `MERGE`, `DELETE`, `DETACH`, `SET`, `REMOVE`, `DROP`, `FOREACH`, `CALL` or `LOAD` outside string literals/comments/quoted identifiers. Consequences: no MAGE procedures, no writes; use `just graph-shell` (mgconsole) or Lab for those, and use a read-only Memgraph user when the endpoint is shared.

## Operation 3 — introspect what is actually loaded

```graphql
query Schema {
  memgraphSchema {
    labels { name nodes properties { name count } }
    relationships { name edges properties { name count } }
    snapshot { id createdAt loadedAt status force surfaces nodes edges }
  }
}
```

- Derived from loaded data, so a never-loaded label/type/property is absent; declared constraints/indexes are not reported (they live in `memgraph/schema.cypherl`; `SHOW INDEX INFO`/`SHOW CONSTRAINT INFO` via mgconsole).
- One read transaction; empty lists and null snapshot before the first completed load; fails when the stack is down.
- `snapshot` = latest **loaded** snapshot (`:Snapshot {loaded:true}` ordered by id).
- Labels count per label, so `Entity` (all entities) plus category labels overlap; `nodes` in a label is not a partition.

## Model

**Identity.** `id = surface:unit` when the entity has a unit number, else `surface:type:name:x:y` (resources have no unit). Unit numbers are global; the surface prefix keeps ids readable and cross-surface linked belts distinct.

**Node labels.** Every entity is `:Entity` plus category labels from engine type.

| Label | Engine types |
|---|---|
| `:Machine` | assembling-machine, furnace, lab, rocket-silo, agricultural-tower, asteroid-collector |
| `:MiningDrill` | mining-drill |
| `:Belt` | transport-belt, underground-belt, splitter/`:Splitter`, loader, loader-1x1, linked-belt, lane-splitter |
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

**Point-in-time contents sample** (taken at the leaf tick, not live): `fuel`, `input`, `output` (furnaces, assemblers, burner drills/boilers/inserters), `items` (chests), `left`/`right` (transport-belt lanes), each a sorted list of `name:count` (`name@quality:count` when not normal). Matching `…Count` is present whenever the entity has that inventory — `fuelCount:0` = empty burner, absent `fuelCount` = no fuel inventory. Belt `left`/`right` side mapping is not independently verified; do not read it as lane purity proof.

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

Fluid, wire and rail edges are undirected in meaning: match them without an arrow. Belt, inserter, drill and beacon edges are directional.

**Coverage limits.** Captured: static entities of the force on exported surfaces (incl. ghosts, poles, rails, combinators, defenses), vehicles/characters/rolling stock (one-tick mobile pass), belt/inserter/drill/beacon/fluid/wire/rail links and network membership, recipe/modules/status/quality/direction. Not captured: robots and transient effects (counted as skipped), live item/belt contents and throughput (only the point-in-time sample above), splitter lane routing, heat pipes, train schedules/paths, tiles, trees, enemies.

## Verification and staleness

```graphql
# freshness + coverage
{ memgraphSchema { snapshot { id status force surfaces nodes edges loadedAt createdAt } } }
```

```graphql
# count cross-check against the latest snapshot; pass --variables '{"snap":"<snap-id>"}'
query Check($snap: String!) {
  nodes: queryMemgraph(input:{cypher:"MATCH (n) WHERE (n:Entity OR n:Network) AND n.snapshot = $s RETURN count(n)", params:{s:$snap}}) { rows }
  edges: queryMemgraph(input:{cypher:"MATCH ()-[r]->() WHERE r.snapshot = $s RETURN count(r)", params:{s:$snap}}) { rows }
  live: status { tick }
}
```

```graphql
# fresh in one call: export+load, then read (status.tick is a separate operation)
{ queryMemgraph(input:{sync:true, cypher:"MATCH (e:Entity) RETURN min(e.tick) AS minTick, max(e.tick) AS maxTick"}) { rows } }
```

- After a sync, expect `max(e.tick)` within seconds of `status.tick`; a large gap means the export was not the one you think (shared graph, failed sync) — recheck the snapshot id.
- `MATCH (s:Snapshot {loaded:true})` returns **every historical snapshot** (observed 14 while only 5 directories are kept); always `ORDER BY s.id DESC LIMIT 1` or read `memgraphSchema.snapshot`.
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

## Rules

### MUST

1. **Check freshness first.** Read `memgraphSchema.snapshot` (or latest `:Snapshot {loaded:true}`) and compare with `status.tick`; refresh with the mutation or pass `sync:true` on the read before concluding anything structural about the current save.
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
3. **Do not use `CALL`/MAGE, write clauses, or DDL through `queryMemgraph`** — the guard rejects them; writes belong in mgconsole/Lab, and schema DDL in `memgraph/schema.cypherl`.
4. **Do not treat `count(:Snapshot)` or an unfiltered `MATCH (n)` as the current factory** — stale Snapshot metadata and stale unexported-surface scopes persist.
5. **Do not run the export mutation or `sync:true` concurrently** (the lock is per process, so two serve processes still race) or during active construction where `drift` would mislead; rerun instead.
6. **Do not claim schema completeness from `memgraphSchema`** — it is derived from loaded data and omits declared indexes/constraints.

## Troubleshooting

| Symptom | Cause / fix |
|---|---|
| `Memgraph query failed (is the stack up? just graph-up)` | stack down or wrong Bolt env on the `serve` process |
| `queryMemgraph is read-only: DROP/SET/… is not allowed` on a pure MATCH | a forbidden keyword appears as an **alias** or bare identifier, e.g. `RETURN dst.id AS drop`; rename the alias (`dropId`). Property/label names after `.`/`:` are exempt |
| `params must be a JSON object` | pass `{...}` (or a JSON object variable), not a scalar/array |
| `limit must be between 1 and 10000` / `timeoutMs must be between 100 and 60000` | clamp inputs |
| `truncated: true` | hit `limit`; add Cypher `LIMIT`/`SKIP` or raise up to 10000 |
| `Memgraph query exceeded timeoutMs=…` | tighten anchors/indexes, add `LIMIT`, raise `timeoutMs` |
| empty `memgraphSchema.labels` and null snapshot | no completed load yet |
| `sync before queryMemgraph: another Memgraph export is already running` | sync requested while an export holds the lock; retry after it finishes, or drop `sync` and read the existing snapshot |
| `sync before queryMemgraph: snapshot … is incomplete …` | the sync export found a hole and was not loaded; the query aborted and the previous graph is unchanged — rerun |
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
gqlcli describe Query.memgraphSchema --depth 1
gqlcli describe Query.queryMemgraph --desc
gqlcli describe Mutation.exportMapSnapshotToMemgraph --desc
```

```graphql
extend type Query {
  memgraphSchema: MemgraphSchema!
  queryMemgraph(input: MemgraphQueryInput!): MemgraphQueryResult!
}
extend type Mutation {
  exportMapSnapshotToMemgraph(input: ExportMapSnapshotToMemgraphInput! = {}): MemgraphExportResult!
}
input MemgraphQueryInput {
  cypher: String!  params: Any  limit: Int! = 1000  timeoutMs: Int! = 10000  sync: Boolean! = false
}
```
