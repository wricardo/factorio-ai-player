# Rocket and endgame

Victory condition for the harness is a **verified rocket launch**. This file covers research prerequisites, ingredient lines, silo construction, readiness and launch, and post-launch verification. Facts are from live recipes and earlier silo work (tickets `7de.*`); items marked `[memory]` must be re-verified on the current save.

## 1. Dependency tree (live recipes, Space Age 2.0.77)

```
rocket-silo (30 s): 1000 steel + 1000 concrete + 200 processing-unit + 200 electric-engine-unit + 100 pipe
rocket-part (3 s, rocket-building): 1 processing-unit + 1 low-density-structure + 1 rocket-fuel
  processing-unit (10 s, electronics-with-fluid): 20 electronic-circuit + 2 advanced-circuit + 5 sulfuric-acid
  low-density-structure (15 s): 20 copper-plate + 2 steel-plate + 5 plastic-bar
  rocket-fuel (15 s): 10 solid-fuel + 10 light-oil
electric-engine-unit (10 s, crafting-with-fluid): 2 electronic-circuit + 1 engine-unit + 15 lubricant
  engine-unit (10 s): 1 steel-plate + 1 iron-gear-wheel + 2 pipe
advanced-circuit (6 s): 2 plastic-bar + 4 copper-cable + 2 electronic-circuit
concrete (10 s, crafting-with-fluid): 1 iron-ore + 5 stone-brick + 100 water -> 10 concrete   (1000 concrete = 100 crafts = 100 iron ore + 500 stone-brick + 10,000 water)
sulfuric-acid (1 s, chemistry): 1 iron-plate + 5 sulfur + 100 water -> 50 acid
solid-fuel-from-light-oil (1 s, chemistry): 10 light-oil -> 1 solid-fuel
```

Research path (`scripts/tech-path.js`, current save): 25 technologies / 4216 units. Silo prerequisites: `advanced-material-processing-2`, `concrete`, `logistic-robotics`, `low-density-structure`, `processing-unit`, `rocket-fuel`, then `rocket-silo` itself (1000 units, 60 s each). Packs needed: red, green, blue.

## 2. Plan quantities from engine state, not memory

1. `recipe(rocket-part)`, `recipe(rocket-silo)`, `billOfMaterials` for the batch.
2. After a silo exists, `inspect(silo).settings.rocketSilo { rocketParts rocketSiloStatus sendToOrbitAutomatically }` shows required parts and status.
3. Earlier runs measured **50 rocket parts** per rocket for the Space Age silo (so a first launch needs ~50 PU + 50 LDS + 50 rocket fuel, not 1000) [memory]. Derived totals are in `production-math.md` section 7. Prior planning that assumed 1000 of each was wrong; always read the silo.
4. Compute the ceiling of each ingredient line (`production-math.md` section 8). The slowest of PU/LDS/RF rates sets rocket-part rate; the silo itself needs 3 s per part, i.e. 150 s for 50 parts at speed 1.

## 3. Ingredient lines (design notes)

- **Plastic and sulfur** from petroleum gas; **LDS** needs copper, steel and plastic; **PU** needs 20 circuits each (the largest circuit sink), advanced circuits and sulfuric acid; **RF** needs solid fuel and light oil (see `fluids-and-oil.md`); **EEU** needs lubricant (heavy oil).
- Build and prove one line at a time; put a bounded buffer at the output of each (`setContainerBar`), and feed the silo with continuous supply, not a one-off burst.
- Stack sizes (live): processing-unit 100, LDS 50, rocket-fuel 20, pipe 100, steel/plates 100. A chest slot holds exactly one stack; `bar:N` leaves N-1 slots.
- Staging chests mixing an item signal with inserter HOLD signals on one circuit network make edge-detector counters drift; resync only when the belt is empty, or isolate the chest signal behind an arithmetic diode (`circuits-and-trains.md`).
- Rocket parts are created inside the silo; never transfer `rocket-part`.

## 4. Silo construction and feed

1. Verify technology, silo item (hand-craft only as a singleton: `rocket-silo` is category `crafting`, 1000 steel + 1000 concrete + ...), and a 9x9 legal powered footprint. `probePlacement` is unreliable for the silo's footprint: confirm with `worldPlacement` / `nearestPlacement(prototype:"rocket-silo")` and a read-back.
2. Place; record `unitNumber`, position. Connect power. Feed PU, LDS and rocket fuel with inserters, or `setLogisticRequests` (silo accepts logistic requests) when a robot network exists (requester slots fill to `min`; set `min=max`).
3. After stabilization, read inspect: status, input inventory, `rocketParts`.

## 5. Launch and verification

```graphql
{ inspect(input:{actor:{character:"123"}, target:{entity:"rocket-silo", unitNumber:"999"}}) {
    statusName inventories { input { name count } output { name count } }
    settings { rocketSilo { rocketParts rocketSiloStatus sendToOrbitAutomatically } } } }
```

- Ready: `statusName` = `waiting_to_launch_rocket` with `rocketParts` at the requirement (50 in earlier runs).
- Launch: `mutation { launchRocket(input:{actor:{character:"123"}, target:{entity:"rocket-silo", unitNumber:"999"}}) { rocketSilo { rocketParts rocketSiloStatus sendToOrbitAutomatically } } }`. On refusal the message carries the engine reason: stop, capture it plus silo state, retry only after state changes.
- `setRocketSilo` toggles auto-launch; auto-launch off is the default and lets you decide the moment.
- Verify the launch: sample silo status over several ticks (`launching_rocket` -> back to preparing/idle), `rocketParts` resets, cargo pod entity position changes or despawns. **A cargo pod resting on a loaded silo is the PRE-launch pod, not evidence of a launch.** Check surfaces (`surfaces`) for platform effects only when the save has Space Age content to receive cargo.
- Mutation success alone is not proof. Record tick, silo unit number, before/after status, measured ingredient stock and the evidence that proved completion.

## 6. After the first launch

A completed launch resets the silo. Decide explicitly whether to continue (keep ingredient supply and the labs running) or stop; record that decision with the tick and unit numbers. Base game vs Space Age launch semantics differ (satellites, cargo, platforms): never hard-code landing-pad or platform prerequisites without live evidence.
