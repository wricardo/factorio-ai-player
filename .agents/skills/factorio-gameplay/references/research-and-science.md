# Research and science

Research is the longest timer in a run. Keep a technology active at all times and keep every lab fed. Use `scripts/tech-path.js` for planning and `researchInsights` / `scienceBudget` for live state.

## 1. Reads

```graphql
{
  status { tick speed paused }
  researchInsights(input:{force:"player", status:AVAILABLE, limit:100}) {
    currentTechnology progress researchQueue researchEnabled
    pageInfo { total hasMore }
    technologies { name researchUnitCount researchUnitEnergy researchTriggerJSON
      prerequisites { name researched } scienceIngredients { name amount } effects { type recipe } }
  }
  scienceBudget(force:"player", surface:"nauvis") {
    technology triggerResearch progress remainingResearchUnits researchUnits labCount missingPackLabCount
    sciencePacks { name nominalPacksLeft availableItems nominalShortfall }
  }
}
```

- `status`: `ALL RESEARCHED AVAILABLE LOCKED IN_PROGRESS`. `researchInsights(unlocksRecipe:"x")` or `(technology:"x")` narrows. `prerequisites` is a list of `{name researched}`, not strings.
- Unit time (seconds) = `researchUnitEnergy / 60` (live: `automation` 600 ticks = 10 s; `rocket-silo` 3600 = 60 s).
- `researchTriggerJSON` non-null = **trigger technology**: it completes when its trigger happens (craft/mine/build), not by lab work. Verified triggers: `steam-power` (craft 50 iron-plate), `electronics` (craft 10 copper-plate), `oil-processing` (mine crude-oil), `uranium-processing` (mine uranium-ore), `space-science-pack` (build asteroid-collector). `scienceBudget.triggerResearch` is true while one is active.
- `scienceBudget` counts stock in containers, lab inputs and characters, not belts or ground items.

## 2. Control

```graphql
mutation { startResearch(input:{force:"player", technology:"logistic-science-pack"}) { started currentResearch researchQueue } }
mutation { queueResearch(input:{force:"player", technology:"steel-processing"}) { researchQueue } }
mutation { cancelResearch(input:{force:"player"}) { currentResearch researchQueue } }   # drops dependent queue entries
mutation { clearResearchQueue(input:{force:"player"}) { researchQueue } }                # current research keeps running
```

- Start or queue only technologies whose prerequisites are researched; `queueResearch` appends and requires enabled + unresearched + prerequisites done.
- Never cancel useful progress. Switching starts a new current technology but `savedProgress` is kept by the engine per technology.
- Verify with `waitUntil{ research:{ force:"player", technology:"x" } }` for completion, or two progress samples a known tick apart.
- `unlockAllTechnologies` is an administrative sandbox shortcut; do not use it in normal play.

## 3. Feeding labs

- A lab consumes one of each required pack per unit; unit rate = `labSpeed / unit_time`. Packs/s of each type for L labs = `L / unit_time`.
- Chain labs with inserters (lab -> inserter -> lab) so packs pass downstream; a belt of mixed packs can jam when one type backs up: give each pack a lane or use sushi only with proven consumers.
- Each lab reports `missingSciencePacks`; `scienceBudget.missingPackLabCount > 0` means at least one lab lacks a pack or cannot accept it.
- Lab power is 60 kW [base]; unpowered labs report `no_power`. An idle lab reports `no_research_in_progress` until a technology is started.

## 4. Pack production summary (see `production-math.md` for ratios)

| Pack | Needs |
|---|---|
| red | gear + copper plate; 5 s |
| green | transport-belt + inserter; 6 s |
| blue | sulfur + advanced circuits + engines; 24 s -> 2 packs; needs oil |
| military | piercing ammo + grenade + walls; 10 s -> 2 |
| production | rails + electric furnace + productivity module; 21 s -> 3 |
| utility | processing units + flying robot frame + LDS; 21 s -> 3 |
| space | Space Age platform item; 5 per craft (ice + iron + carbon) |

Rocket-only path needs red + green + blue. For a first launch skip military/production/utility unless the technology graph for your target says otherwise (`researchInsights(unlocksRecipe:...)`); the ordered rocket sequence is section 6.

## 5. Planning checklist

1. `tech-path.js` for any target, or section 6 below for the rocket sequence: units, per-pack totals, trigger techs, order.
2. Compute lab count from the desired finish time: `labs = labSeconds / (target_seconds x labSpeed)`.
3. Build pack cells sized to `labs / unit_time` per type; see `bootstrap-and-progression.md`.
4. Start the first technology now; research while building the next pack line.
5. Keep a queue of at least two technologies; check `status.paused` if progress stalls.
6. Verify progress increases over two samples; pack stock in chests without progress is not proof.

## 6. Rocket launch: the optimal research sequence

Numbers below are the live technology graph (Factorio 2.0.77, read 2026-11-02, tick ~2.15M). Re-read them with the GraphQL documents in 6.5; a new save or a mod changes the tree. Derive plans from those reads, not from ad-hoc helper scripts.

**The bill is fixed, the dead time is not.** Ordering never lowers total lab-seconds; it only removes (a) lab fleet idle time and (b) build steps waiting on a lock. So the sequence below is built on three rules, not on a cost sort.

### 6.1 The bill

| View | Techs | Units | Lab-seconds | Red/green/blue packs |
|---|---|---|---|---|
| Fresh run: closure of `rocket-silo` + pack-production techs | 29 | 4229 | 155,075 | 4225 / 4090 / 2650 |
| Live save (2026-11-02), still unresearched | 19-20 | 3826-3876 | ~149,250-150,000 | 3825-3875 / 3825-3875 / 2650 |

- `rocket-silo` alone is 1000 units x 60 s = **60,000 lab-seconds**: 39% of the whole bill, one technology.
- The three rocket-part lines (`processing-unit`, `low-density-structure`, `rocket-fuel`) are 900 units / 36,000 lab-seconds, and they are `rocket-silo`'s own prerequisites.
- Blue work is 2650 units / 118,500 lab-seconds (76% of lab time). Red/green-only work is 36,575 lab-seconds in the fresh run (2,825 bootstrap + 33,750 phase 1; 30,750 still open on the live save): the budget you can spend before any blue pack exists.
- The prerequisite closure alone under-counts a fresh run: blue packs need `engine-unit`, which needs the `engine` technology, and `inserter` needs `electronics`. Always add the unlock technology of every pack ingredient (`unlocksRecipe`).

### 6.2 Sequence

Phase 1 runs on red/green only, so it never waits for blue. Phase 2 consumes blue and starts the moment blue packs flow. Phase 0 (bootstrap, already done on any live base) is the trigger pair `steam-power` (craft 50 iron-plate) and `electronics` (craft 10 copper-plate) plus `automation-science-pack` (craft a lab), then `automation` (10 units), `logistic-science-pack` (75), `steel-processing` (50), `engine` (100), `automation-2` (40): 2,825 lab-seconds, all red/green — see `bootstrap-and-progression.md`.

| # | Technology | Units | Unit s | Lab-s | Unlocks / why here |
|---|---|---|---|---|---|
| 1 | `fluid-handling` | 50 | 15 | 750 | pipes, tanks, pumps: every later line needs fluid plumbing |
| 2 | `oil-gathering` | 100 | 30 | 3,000 | pumpjacks |
| 3 | `oil-processing` | 1 | - | 0 | **trigger** (`mine-entity crude-oil`): refineries, chemical plants, basic oil processing. Do the action; never `startResearch` it |
| 4 | `plastics` | 200 | 30 | 6,000 | plastic bar: advanced circuits and LDS depend on it |
| 5 | `advanced-circuit` | 200 | 15 | 3,000 | red circuits: blue packs, processing units |
| 6 | `sulfur-processing` | 150 | 30 | 4,500 | sulfur + sulfuric acid: blue packs, processing units |
| 7 | `chemical-science-pack` | 75 | 10 | 750 | the blue pack recipe — the gate on the whole second half |
| 8 | `battery` | 150 | 30 | 4,500 | prerequisite of `robotics` |
| 9 | `advanced-material-processing` | 75 | 30 | 2,250 | prerequisite of `advanced-material-processing-2` and `concrete` |
| 10 | `concrete` | 250 | 30 | 7,500 | silo material: 1000 concrete |
| 11 | `flammables` | 50 | 30 | 1,500 | pure gate for `rocket-fuel`, unlocks no recipe: last of the phase |
| 12 | `advanced-oil-processing` | 75 | 30 | 2,250 | advanced refining + both cracking recipes + solid fuel from light/heavy oil: the refinery rebuild |
| 13 | `lubricant` | 50 | 30 | 1,500 | heavy oil -> lubricant |
| 14 | `electric-engine` | 50 | 30 | 1,500 | electric-engine-unit: 200 for the silo |
| 15 | `robotics` | 75 | 30 | 2,250 | flying robot frame |
| 16 | `advanced-material-processing-2` | 250 | 30 | 7,500 | electric furnace: plate/steel scale for the silo kit |
| 17 | `logistic-robotics` | 250 | 30 | 7,500 | roboport + logistic robot: silo prerequisite and the build force for the silo |
| 18 | `processing-unit` | 300 | 30 | 9,000 | PU line (circuits + acid) |
| 19 | `low-density-structure` | 300 | 45 | 13,500 | LDS line (copper + steel + plastic) |
| 20 | `rocket-fuel` | 300 | 45 | 13,500 | RF line (solid fuel + light oil) |
| 21 | `rocket-silo` | 1000 | 60 | 60,000 | silo item + `rocket-part`. Last, and it buys the build window |

Phase 0 + phase 1 = 36,575 lab-seconds with zero blue packs (2,825 + 33,750); phase 2 = 118,500 lab-seconds on blue (2650 units).

### 6.3 Why this order

1. **Never idle the fleet.** Phase 1 is the only research work available until blue packs exist, so it must be *all* red/green-only work, done while the oil and blue cells are being built. Do not switch to a blue technology whose packs are not stocked; do not let blue pack production back up either (blue packs stack to 200, so the whole 2650-pack run fits in 14 slots of one chest).
2. **Build-gate first, cost second.** Among ready technologies, prefer the one whose unlocked recipe the next build step needs. A technology that unlocks no recipe (`flammables`) is a pure gate: it buys nothing until `rocket-fuel` is imminent, so it goes last.
3. **Trigger technologies are actions, not research.** `startResearch` refuses them (`technology is a trigger technology (...)`; `add_research` returns false), and the engine fires them when the trigger happens. `oil-processing` completes on the first `crude-oil` mine: do that as soon as `oil-gathering` finishes, or the refinery build waits.
4. **Silo last.** The graph forces it (the three ingredient lines are its prerequisites) and the sequence exploits it: 60,000 lab-seconds is the largest build window available, and the silo needs 1000 steel + 1000 concrete + 200 PU + 200 EEU + 100 pipe (live recipe) plus a commissioned PU/LDS/RF supply. Research it while that kit is produced.
5. **Queue legality.** `queueResearch` rejects a technology with an unresearched prerequisite (`could not queue X: unmet prerequisites ...`), so queue only one or two deep and in the order of 6.2; derive the next legal pair from that table, not from a script.
6. **Nothing after the silo.** Rocket-part production (50 parts [memory], read the silo's own requirement) needs no further research. Do not queue branches outside the closure behind it.

### 6.4 Sizing: labs and packs

Lab-seconds to wall time (fresh run 155,075; live save 149,250; no lab-speed modifier, so `researchInsights.researchUnitEnergy / 60` is the real per-unit time):

| Labs | Game hours at speed 1 | Wall hours at speed 3 |
|---|---|---|
| 3 | 14.4 | 4.8 |
| 6 | 7.2 | 2.4 |
| 12 | 3.6 | 1.2 |
| 24 | 1.8 | 0.6 |

Pack draw per lab is `60 / unitSeconds` of each pack per minute, so the pack-hungry front of the tree is the earlier phases, not the tail:

| Unit seconds | Pack sets/min/lab | Technologies |
|---|---|---|
| 60 | 1.0 | `rocket-silo` |
| 45 | 1.33 | `low-density-structure`, `rocket-fuel` |
| 30 | 2.0 | most red/green and blue technologies |
| 15 | 4.0 | `advanced-circuit`, `fluid-handling` |
| 10 | 6.0 | `chemical-science-pack` |

Consequence: size the blue cell for the 15-30 s technologies, not for the silo. At 12 labs on 30 s units that is 24 packs/min of each type = 24 blue/min; with the live recipe (24 s, 2 packs: 1 sulfur + 3 advanced-circuit + 2 engine-unit) that is ~7 assembling-machine-2 (0.75 speed, 3.75 blue/min each) fed by ~36 advanced-circuit, 24 engine-unit and 12 sulfur per minute.

### 6.5 Re-derive it with plain GraphQL

Four documents cover the whole plan. No helper script needed.

```graphql
# 1. The graph. Page it (275 technologies on the live save): repeat with
#    offset 100, 200, 300... until pageInfo.hasMore is false.
{ researchInsights(input:{force:"player",status:ALL,limit:100,offset:0}) {
    pageInfo { total hasMore }
    technologies { name researched researchUnitCount researchUnitEnergy researchTriggerJSON
      prerequisites { name researched } scienceIngredients { name amount }
      effects { type recipe } } } }
#    Client-side: walk prerequisites from "rocket-silo" to get the closure, then
#    labSeconds = researchUnitCount * researchUnitEnergy / 60 per technology.

# 2. Pack ingredients the closure misses: blue packs need engine-unit, whose
#    technology is not a prerequisite of rocket-silo.
{ recipe(input:{actor:{player:"wricardo"}, recipe:"chemical-science-pack"}) { ingredients { name type } } }
{ researchInsights(input:{force:"player",unlocksRecipe:"engine-unit",status:ALL,limit:10}) { technologies { name } } }
#    -> "engine"; the same lookup on "inserter"/"electronic-circuit" -> "electronics".
#    Add those closures too, then re-check for new packs.

# 3. Live state: what is running, what is queued, whether the labs are fed.
{ status { tick speed paused }
  researchInsights(input:{force:"player",status:IN_PROGRESS}) {
    currentTechnology progress researchQueue researchEnabled }
  scienceBudget(force:"player", surface:"nauvis") {
    technology triggerResearch progress remainingResearchUnits labCount missingPackLabCount
    sciencePacks { name nominalPacksLeft nominalShortfall } }
  machines(input:{surface:"nauvis",type:"lab"}) { pageInfo { total } summaries { type total working idle } } }

# 4. Next action, one or two deep and in prerequisite order. Only queue the next
#    technology of the table: queueResearch rejects an unresearched prerequisite.
#    (Example below assumes oil-processing already fired; substitute the next
#    technology of 6.2 that is `available` in document 1.)
mutation { queueResearch(input:{force:"player", technology:"plastics"}) { researchQueue } }
```

Then prove progress rather than assuming it: `machines(type:"lab")` for `total`/`working`/`idle`, `scienceBudget.missingPackLabCount` for feed faults, and `waitUntil{ research:{ force, technology } }` or two `researchInsights` progress samples a known tick apart. A queue entry is not progress; pack stock without progress is not proof.
