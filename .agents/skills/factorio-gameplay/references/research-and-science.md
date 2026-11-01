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

Rocket-only path needs red + green + blue. For a first launch skip military/production/utility unless the technology graph for your target says otherwise (`tech-path.js`).

## 5. Planning checklist

1. `tech-path.js` for the target: units, per-pack totals, trigger techs, order.
2. Compute lab count from the desired finish time: `labs = labSeconds / (target_seconds x labSpeed)`.
3. Build pack cells sized to `labs / unit_time` per type; see `bootstrap-and-progression.md`.
4. Start the first technology now; research while building the next pack line.
5. Keep a queue of at least two technologies; check `status.paused` if progress stalls.
6. Verify progress increases over two samples; pack stock in chests without progress is not proof.
