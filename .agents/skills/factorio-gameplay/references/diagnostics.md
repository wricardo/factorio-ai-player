# Diagnosing a stalled or underperforming factory

Goal: find the single root cause fastest, fix it, and prove the fix. Start broad (`factoryBriefing`), narrow to one machine, walk upstream until the first broken dependency, then repair that and re-measure. Do not add machines until a working machine has been shown to be the limit.

## 1. Five-minute triage

```sh
gqlcli script --strict=false --file .agents/skills/factorio-gameplay/scripts/briefing.js \
  --arg '{"limit":20,"watch":[{"item":"iron-plate","minimum":100},{"item":"copper-plate","minimum":100}]}'
```

Read in this order:

1. `paused` / `speed`: a frozen game explains everything.
2. `attentionCounts` and `attention[]` (CRITICAL first). One shared root cause usually explains most rows: many `NO_FUEL` rows = no fuel path; many `NO_POWER` = pole gap or dead generator.
3. `machines[]` per type: `working` vs `noFuel/noPower/noIngredients/outputFull/resourceDepleted`. A type with `working 0` and `noIngredients N` points upstream.
4. `power{productionWatts consumptionWatts}` and `networks`: multiple networks or `production == consumption` at the limit mean undersupply or a split grid.
5. `research{currentTechnology progress queue}`: null current research is lost lab time.
6. `production[]` rates and `stockouts[]` for named items; `stockCategory` defaults to `STORED`, so pass `ALL` or another category to include player/worker/ground/belt items.
7. `warnings[]` list skipped sections (stockouts need `watchItems`) and capped scans; ghost counts follow the briefing scope, the area when given and otherwise the whole surface.

Once attention rows point at one block, stop reading status rows and run `factoryDefects(input:{area, surface, force, limit, kinds?})` over that block: it returns the deterministic structural faults (`BELT_DEAD`, `BELT_STARVE_HEAD`, `BELT_MIXED_LANE`, `INSERTER_DANGLING_*`, `MACHINE_NO_INPUT`, `GHOST_NO_ROBOPORT`, ...) with entity ids, so escalation moves straight to the actionable list instead of another scan.

Live example (2026-11-01): `attentionTotal 40`, drills and furnaces `NO_FUEL`, one lab idle, no research. Root cause class = fuel supply + idle research, not "needs more machines".

## 2. Status meaning and first action

Engine status names (`statusName` / `status`). **[base]** names come from the engine's entity status set.

| Status | Meaning | First check / fix |
|---|---|---|
| `no_fuel` | burner entity empty | where does its coal come from? inserter, belt lane, chest (`bar` reached?), `fuelMany` for a one-off |
| `no_power` / `low_power` / `not_plugged_in_electric_network` | no or insufficient electricity | `powerConsumers(onlyIssues:true)`; pole coverage; boiler fuel and water; generation vs load |
| `no_recipe` | assembler without recipe | `setRecipe` (machine idle/empty); furnaces choose recipe by input |
| `no_ingredients` / `item_ingredient_shortage` | an input is missing | which one? `machines.diagnostics.shortfall`; trace the supplying inserter/belt |
| `fluid_ingredient_shortage` / `missing_required_fluid` / `no_input_fluid` | fluid input missing | `fluidStock`, pump status, wrong-fluid segment |
| `full_output` | cannot drop product | downstream consumer missing or full chest/belt/box; fix the sink first |
| `waiting_for_source_items` | inserter idle, no source | source empty: go upstream |
| `waiting_for_space_in_destination` | inserter or drill blocked | destination full; a drill spilled onto a ground tile; pumpjack stub pipe |
| `no_minable_resources` | drill/pumpjack on depleted or off-centre position | `worldResources` under its centre; pumpjack centre on crude tile |
| `no_research_in_progress` | lab idle, no technology | `startResearch` / `queueResearch` |
| `missing_science_packs` | lab lacks a pack type | feed chain; inserter from previous lab |
| `disabled_by_control_behavior` | circuit/enable condition false | `inspect.settings`; probe signals |
| `marked_for_deconstruction` | order pending | `constructionOrders` |
| `waiting_to_launch_rocket` | silo has the parts | `launchRocket` |

## 3. Decision order (stop at the first hit)

1. **Frozen or slowed game:** `status.paused`, `speed`.
2. **Missing recipe or technology:** `recipe(...).enabled`, `researchInsights(unlocksRecipe:...)`.
3. **Power:** `electricNetworks`, `powerConsumers(onlyIssues:true)`. Dead boiler (no fuel/water) takes the whole grid down.
4. **Fuel:** every burner entity. Trace coal source.
5. **Ingredient shortfall:** `machines(...).diagnostics { blocked shortfall }`.
6. **Inserter endpoints:** `scripts/inserter-audit.js` (NO_PICKUP/NO_DROP), `inspect{logistics}`. Wrong direction/offset is the most common build error (rule: direction points to pickup).
7. **Belt lanes and gaps:** `beltStretch` `ends[].gap`, `left/right` item mix, `beltStretchMeasure` for crossings. A jam shows large totals with zero crossings.
8. **Fluid connectivity:** see `fluids-and-oil.md`.
9. **Output full:** find and fix the sink (consumer missing, chest `bar` reached).
10. **Upstream producer rate:** only now compare production vs implied ceiling (`production-math.md` section 8).

## 4. Tools per question

| Question | Read |
|---|---|
| What is everything doing in this block? | `factoryGraph(area)` (machines, belts, chests, inserters with edges); `factoryAlerts(area)` for every unhealthy status-bearing entity in one scan |
| Which structural defects exist in this block? | `factoryDefects(input:{area<=1024 tiles, surface, force, limit, kinds?})`: composes graph, belt components, power, ghosts and logistic networks in one call. Rows are deterministic (`BELT_DEAD`, `BELT_STARVE_HEAD`, `BELT_STALL_END`, `BELT_MIXED_LANE`, `INSERTER_DANGLING_PICKUP`, `INSERTER_DANGLING_DROP`, `INSERTER_SHARED_SOURCE`, `MACHINE_NO_INPUT`, `BOILER_NO_FUEL_INSERTER`, `PRODUCER_UNPLUGGED`, `ORPHAN_ENTITY`, `GHOST_NO_ROBOPORT`) with `severity`, `entity`, `unitNumber`, `position`, `detail`; `counts[]` covers the whole scan while `defects[]` is capped by `limit`. Sections come from separate RCON calls, so read the per-section `*Tick` fields |
| Which entity feeds which? | `inspect{logistics{inserters{pickup drop}}}`, `factoryGraph.edges` |
| Is this item short, and who starves? | `itemBalance(item)` (observed flow + stock + starved consumers), then `machines{diagnostics{blocked shortfall mining burner}}` |
| Is the belt flowing? | `beltStretchMeasure(target, durationTicks 600, wait:true)` or `beltThroughputStart` then `beltThroughput(id)` |
| Does the lane carry mixed items? | `beltStretch` side totals + `mixed` flags |
| Where is an item? | `stock(item, category: STORED\|IN_TRANSIT\|GROUND\|PLAYER\|WORKER)` locations and totals |
| Real rate vs ceiling? | `scripts/measure-rate.js` (stock delta over N ticks) and `production(window)` |
| Geometry sanity | `mapArea`, `adjacency` (touching vs connected) |
| Misrouted pipes | `adjacency`, `fluidStock` by location, `inspect.fluidboxNeighbours` |

## 5. Frequent root causes in this project

- **Burner supply loop missing:** drills/furnaces `no_fuel` while a coal belt exists nearby. Fix by delivering fuel automatically (inserters taking from the coal belt, or chest + inserter), not by repeated `fuelMany`.
- **Inserter geometry:** drop onto the wrong tile; inserter placed on top of an assembler edge and refused; pickup tile is empty. Use `pickup = pos + unit(dir)`.
- **Mixed lanes:** two items share a lane; one backs up and jams both. Keep one item per lane; check after every merge/splitter/underground.
- **Idle research:** labs unpowered or without packs, or no technology active. Check `scienceBudget`, `researchInsights.currentTechnology`.
- **Sink-limited zero flow:** a measured 0/min where the destination chest/belt is full. Inspect the sink before expanding supply.
- **Shared stock drain:** a staging buffer or requester chest eats the plates that science needs. Bound buffers; read `itemFlow`/`stock`.
- **Unbounded mall outputs** starve science intermediates. Use `setContainerBar` or request limits.
- **Contaminated fluid segment:** one pipe-to-ground facing the wrong way merges fluids (see `fluids-and-oil.md`).
- **Depleted or misplaced miners:** `no_minable_resources`; check `worldResources` under the drill and patch `totalAmount`.
- **Constructed but unpowered:** new machines outside any pole's supply area; `powerConsumers(status:"no_power")`.

## 6. Proof and record

A diagnosis is complete when: baseline (tick, symptoms) recorded; root cause named with entity ids/positions; one smallest change made; same reads repeated after at least one machine cycle show the status leaves the bad state and the rate moves the right way; any residual risk named. If the cause was an API defect or unexpected engine behaviour, stop that workstream and write it up (see `multi-agent-and-operations.md` section on escalation).
