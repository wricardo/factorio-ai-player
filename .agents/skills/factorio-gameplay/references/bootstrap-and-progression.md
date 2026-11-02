# Bootstrap and progression: from nothing to a launched rocket

A stage ladder with entry conditions, actions, exit proofs and the traps that stalled earlier runs. Written from the closed `rxi.*` bootstrap tickets, the research graph read live on 2026-11-01, and the verified recipes in `production-math.md`. The live save, schemas and technology graph win over any number here. Use `scripts/tech-path.js` for exact research planning.

## 0. The bootstrap on this project (what actually happened)

Epic `rxi` ("automate red science from raw ore to lab consumption") on a fresh freeplay Space Age save with no enemies:

| Ticket | Outcome | Lesson |
|---|---|---|
| `rxi.1` trigger techs | `steam-power` (craft 50 iron plates), `electronics` (craft 10 copper plates), `automation-science-pack` (craft a lab) all researched by smelting and hand-crafting | Trigger techs complete on **production counts**, not lab time. Verify with `researchInsights`. Chart-tag the patches and base site. |
| `rxi.2` steam power | pump -> boiler -> engine -> 7 poles, lab powered, 1800+ ticks sustained | Single-fluid segments; coal chest `bar 2` + burner inserter to the boiler; measure `electricNetworks` across >=1800 ticks under a temporary load. |
| `rxi.3` research `automation` | 10 hand-crafted red packs (1 copper + 1 gear each) | A detached character is the worker; the human's character is never moved. |
| `rxi.4` coal | 4 burner drills + 125-tile belt = 60 coal/min; depot chests bar 2; boiler refilled by inserter for 2439 ticks | **The drills themselves still needed manual fuel** (ticket `yoi`): a self-fuelling coal loop is part of "automated". |
| `rxi.5` iron | 5 burner drill -> furnace pairs + burner inserters + one belt row = 78-90 plates/min | Drill output feeds the furnace directly. Fuel for drills, furnaces and inserters must be belted/loaded. |
| `rxi.6` copper | 3 drills, 3 independent ore belt segments, 145-tile plate belt, 45-50 plates/min over 15 k ticks | One drill per furnace pair avoids ore-sharing starvation. |
| `rxi.7` gear + science cells | **blocked**: wrong inserter offsets, mixed lanes, test drops exhausted stock | See `spatial-and-geometry.md` section 3 and 6; build one cell, audit, then continue. |
| `rxi.8-.11` | open: labs fed automatically, fuel belts to cells, end-to-end verification | Stalled burner cells (live: most drills/furnaces `no_fuel`) are the current binding constraint. |

Take these lessons as the default plan for any new save.

## 1. Stage ladder

Each stage lists the proof that it is done. Do not advance on intent.

| # | Stage | Do | Exit proof |
|---|---|---|---|
| 0 | Orient | `status`, actor/inventory, `survey.js`, `researchInsights(status:AVAILABLE)`, `surfaces`; chart-tag patches | patch table with distances; actor has a character |
| 1 | Hand bootstrap | mine/smelt for trigger techs, craft lab, steam parts | triggers researched, 1 lab + steam parts in inventory |
| 2 | Power | water + boiler + engine + poles, bounded coal chest | production>0 for 1800 ticks, no manual refuel |
| 3 | Fuel automation | coal drills -> belt -> bounded depot; burners fed by inserter/belt | no `no_fuel` in `factoryBriefing` for 1800 ticks |
| 4 | Plates | iron + copper cells (drill->furnace direct), belt to base | `production` window shows plate rates; buffers bounded |
| 5 | Red science | gear cell + science cell + lab feed | packs/min matches labs; `scienceBudget.missingPackLabCount 0` |
| 6 | Early research | `automation`, `electric-mining-drill`, `logistics`, `logistic-science-pack`, `steel-processing` | recipes enabled (verify with `recipe`) |
| 7 | Electric mining + smelting | electric drills, bigger furnace array, steel | measured ore/plate belt rates |
| 8 | Green science + mall | belts/inserters/circuits cells, mall (see `automation-and-mall.md`) | green packs flowing; mall refills after removing items |
| 9 | Oil | `fluid-handling` -> `oil-gathering` -> crude mining (trigger) -> refinery | single-fluid segments; products consumed |
| 10 | Blue science | plastics, sulfur, advanced circuits, engines | `chemical-science-pack` produced at target rate |
| 11 | Pre-rocket tech | AMP2, concrete, robotics, LDS, processing unit, rocket fuel, rocket silo | `researchInsights` shows `rocket-silo` researched |
| 12 | Rocket parts | PU, LDS, rocket fuel lines | silo `rocketParts` rising |
| 13 | Silo + launch | build silo, supply, `launchRocket` | `rocketSiloStatus` leaves `waiting_to_launch_rocket`, evidence in `rocket-and-endgame.md` |

Run **research and construction in parallel**: start the longest lab timer early (stage 6) and never leave labs idle.

## 2. Hand-bootstrap procedure

1. `characterSpawn` a detached worker near spawn (or `playerRestoreCharacter`); keep the human's character untouched. Record its `unitNumber`.
2. Survey: `scripts/survey.js` (patches by distance), plus water edge via `mapArea` (`~`). Choose iron, copper, coal, stone near each other and near water; record bounds, `totalAmount`, centroid.
3. Mine coal and stone first (`mine` is instant; stay within ~1-2 tiles). Craft stone furnaces (5 stone), then burner drills (3 iron + 3 gear + 1 furnace).
4. Place burner drills on ore feeding furnaces; fuel them (`fuelMany`). Smelt 50 iron and 10 copper plates to trigger `steam-power` and `electronics`.
5. Craft a lab (10 gear + 10 circuit + 4 belt) to trigger `automation-science-pack`.
6. Craft steam parts (offshore-pump, boiler, steam-engine, pipes), small poles (needs wood: harvest trees with `harvestArea`), place, fuel, verify power.
7. Hand-craft 10 red packs, research `automation` (recipe `assembling-machine-1` becomes enabled). This is the **last** hand-crafting stage.

Hand-craft items only until the first assembler exists. After that, every repeated item belongs in a cell or the mall.

## 3. Research plan (verified graph, 2026-11-01)

Early red-only techs: `automation` (10 x 10 s), `electric-mining-drill` (25 x 10 s), `logistics` (20 x 15 s), `logistic-science-pack` (75 x 5 s), `steel-processing` (50 x 5 s), `fast-inserter` (30), `repair-pack` (25), `stone-wall` / `military` / `gun-turret` / `radar` / `lamp` (10-20 each). Then red+green: `automation-2` (needs automation + logistic-science-pack + steel-processing), `engine`, `fluid-handling` (automation-2 + engine), `oil-gathering` (fluid-handling), `oil-processing` (**trigger**: mine crude), `plastics`, `sulfur-processing`, `advanced-circuit` (needs plastics), `battery`, `flammables`, `advanced-material-processing` (steel furnace), `concrete`, `electric-energy-distribution-1`, `solar-energy`, `research-speed-1`, `mining-productivity-1`, `circuit-network`, `toolbelt`.

Chain to blue science: `automation-2` and `engine` -> `fluid-handling` -> `oil-gathering` -> (mine crude) `oil-processing` -> `plastics` + `sulfur-processing` -> `advanced-circuit` -> `chemical-science-pack`.

Full path to `rocket-silo` from the live technology graph: **29 technologies, 4229 research units, 155,075 lab-seconds** from scratch (19-20 technologies / ~149,250 lab-seconds on the 2026-11-02 save). Packs required: 4225 red, 4090 green, 2650 blue. `rocket-silo` itself is 1000 units at 60 s each. Its prerequisites: `advanced-material-processing-2`, `concrete`, `logistic-robotics` (needs `robotics` <- `battery` + `electric-engine` <- `lubricant` <- `advanced-oil-processing` <- `chemical-science-pack`), `low-density-structure`, `processing-unit`, `rocket-fuel`. No military, production or utility packs are needed for the rocket itself. The ordered sequence, the per-phase lab-second totals and the sizing math are in `research-and-science.md` section 6.

Rules:
- Queue the next technology before the current one finishes (`queueResearch`); never leave labs idle. Check `status.paused` and `speed` before concluding a stall.
- Order by dependency + payoff: unlocks that remove bottlenecks first (`electric-mining-drill`, `logistics`, `steel-processing`, `logistic-science-pack`).
- Space Age planet techs (`planet-discovery-*`) need space science and platform work; they are off the rocket critical path unless the objective says otherwise.

## 4. Fuel and power discipline

- Every burner entity (drill, furnace, inserter, boiler) is a recurring fuel demand: coal 4 MJ; one burner drill ~1 coal per 26 s; stone furnace 90 kW. Design the fuel path **with** the cell: coal belt past burners with inserters, or chest + inserter per cluster, bounded `bar 2-3`.
- Replace burner drills/inserters with electric ones as soon as `electric-mining-drill` and power headroom exist; keep the burner path only as a cold-start fallback for the boiler.
- Keep generation ahead of load (>= 30 % headroom): `electricNetworks` plus `powerConsumers(onlyIssues:true)`.

## 5. Stage 5 (red science) build outline

1. Gear cell: assembler `iron-gear-wheel` fed from the iron belt (6.2 in `spatial-and-geometry.md`).
2. Science cell: assembler `automation-science-pack` receiving gears by direct insertion (6.3) and copper by a belt inserter; output inserter into a chest or straight to labs.
3. Labs: an inserter chain between labs (lab -> inserter -> lab) passes packs along; check each lab for `missingSciencePacks`.
4. Power: verify with `powerConsumers(status:"no_power")`.
5. Audit: `scripts/inserter-audit.js` on the cell; `inspect` both assemblers; measure packs/min over two production windows.

Throughput target: size packs/s to labs: `labs / unit_time`. For `logistic-science-pack` (5 s units) 5 labs need 1 red pack/s: 10 assemblers; do not overbuild before the labs exist.

## 6. When blocked

Follow `diagnostics.md`. Record the exact blocker, tick and GraphQL document in the ticket. If the cause is an API defect (not game behaviour), file it separately (see `multi-agent-and-operations.md`).
