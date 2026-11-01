# Fluids and oil: build, balance, repair

Oil is the most failure-prone domain in this project (contaminated segments, stalled refineries, mis-sized cracking). Rules here come from the oil-block repair work (tickets `7de.*`), the fluid audit/repair skills and live reads. Fluid mutation `setFluid` is administrative; normal play uses pumps, pipes, tanks.

## 1. Model: segments, boxes, one fluid per segment

- A connected set of pipes, tanks and machine fluid boxes forms a **segment**. A segment holds exactly **one fluid**. Joining two different fluids contaminates or blocks the whole segment.
- `inspect.fluidNetwork` returns a **transitive neighbour closure**: several segments can appear without being merged. Machine boxes report synthetic segment ids, so **neighbouring PIPE segment ids are the only reliable connection evidence**.
- `fluidStock` pipe amount = `network_fluid / network_capacity * 100`, so equal per-pipe amounts mean one segment. Use this to tell a genuine producer line from a mis-filled one before clearing anything.
- `inspect.fluidboxNeighbours` shows engine neighbours per box; use it after **every** pipe connection.

## 2. Pipe-to-ground (the usual contamination cause)

- `direction` is the side the tile FACES / surface-connects to; the underground span runs the opposite way.
- Horizontal pair: WEST end `dir 12`, EAST end `dir 4`. Vertical pair: NORTH end `dir 8`, SOUTH end `dir 0`. Maximum span: `maxUndergroundDistance 10` (live).
- An unpaired pipe-to-ground dead-ends after one tile and the neighbouring regular pipe of another fluid silently takes the connection.
- Attach branches to the faced side of the tile, never the span side.
- Verify each span immediately: both tiles and one pipe on each flank show the intended fluid; `fluidNetwork` segment ids grew into the target network. `connectEntities kind:PIPE` plans underground spans with the right flags and `allowFluidAdjacency:false` keeps a route away from foreign pipes.

## 3. Building an oil chain

1. **Tech and recipe discovery:** `researchInsights(unlocksRecipe:"basic-oil-processing")`; `itemRecipes`; `recipe` for every fluid input/output. Technology order: `fluid-handling` -> `oil-gathering` -> `oil-processing` (completes by mining crude: trigger) -> `plastics`, `sulfur-processing`, `flammables`, later `advanced-oil-processing` (needs `chemical-science-pack`).
2. **Survey:** `worldResources(resourceName:"crude-oil")`; water route for later sulfur/cracking/acid; `probePlacement SITES` for the refinery block.
3. **One pumpjack first:** centre tile ON a crude tile (centre off crude = `no_minable_resources`). Its output box is at about `centre + (+1,-2)` (confirm with `fluidboxNeighbours`). A stub-only output pipe forms its own tiny segment and leaves the pumpjack `waiting_for_space_in_destination` until the pipe touches an existing pipe of that fluid.
4. **Refinery/chemical plant:** place with room for every fluid connection; inspect each box and neighbour. 5x5 refinery, 3x3 chemical plant.
5. **Storage/consumption for every output.** One full output stops the entire recipe (refinery `full_output`).
6. **Segregate:** crude, water, heavy, light, petroleum gas, lubricant, sulfuric acid each in their own corridor; never share a segment.
7. **Pumps and tanks** for priority and buffering. `storage-tank` is 3x3, 500 hp.
8. **Prove:** source amount falls, product rises, consumers `working`, `fluidStock` shows disjoint segment sets per fluid, product item appears in `production`.

## 4. Ratios and balance

Per refinery recipe run (5 s, live recipes): advanced oil = 50 water + 100 crude -> 25 heavy + 45 light + 55 petroleum gas, i.e. 9 light/s, 5 heavy/s, 11 gas/s per refinery (refinery speed 1). Basic oil processing = 100 crude -> 45 petroleum gas per 5 s (9 gas/s). Cracking recipes are not read here: query them with `itemRecipes` when you need them.

Demands (live recipes): plastic 20 gas + 1 coal -> 2 (1 s); sulfur 30 gas + 30 water -> 2 (1 s); rocket fuel 10 solid fuel + 10 light oil (15 s); lubricant 10 heavy -> 10 (1 s). There is no recipe named `solid-fuel`: `itemRecipes(input:{item:"solid-fuel"})` lists `solid-fuel-from-light-oil`, `-from-heavy-oil`, `-from-petroleum-gas` (all disabled until researched) plus recycling. With `solid-fuel-from-light-oil` (10 light -> 1 [base]) one rocket fuel costs **110 light oil** (10 direct + 10 solid fuel x 10 light each); confirm with `billOfMaterials`.

Procedure for a fluid supply/demand model:

1. Sample twice with a recorded tick gap (use `waitUntil{condition:{tick:T+600}}`): `fluidStock totals` and per-tank `inspect.fluids`.
2. Rate per fluid = `delta amount / delta tick * 60`. A tank at 0 with consumers `fluid_ingredient_shortage` proves immediate consumption, not abundance.
3. Supply = producers x amount / time x speed; demand = every consumer including intermediates (cracking, solid fuel, rocket fuel, lubricant).
4. Check the by-product outlet: a fluid with no consumer (heavy oil with lubricant full) fills its tank and then blocks the whole refinery. Compute the stall ETA from the measured slope.
5. State the binding constraint, the sustainable product rate, and the time to a named target.

`itemFlow` theoretical rates are unreliable for chemical plants; use `recipe.energy` and machine speed. `production` has no fluids. `machines.machineFluids` may show input boxes only.

Do **not** gate cracking or "preserve" a fluid without checking all consumers of the same feed: petroleum chains (plastic, sulfur, acid, batteries) usually share the light-oil/gas source. Prefer capacity over priority when a block is oversubscribed several times over.

## 5. Repairing a contaminated or blocked network

Symptoms: refinery `full_output` with product stuck in a box; consumers `fluid_ingredient_shortage` with source tanks full; a fluid existing only inside machine boxes; pumps `waiting_for_space_in_destination` for no reason.

1. **Map:** `fluidStock(area, limit 800)` and group locations by fluid. Equal per-pipe amounts = one segment. The real producer line has a distinct amount signature.
2. **Find the join:** `adjacency` around suspect tiles; pipe-to-ground orientation (section 2); any regular pipe touching a foreign fluid tile.
3. **Decide:** was the fluid deliberately repurposed? Ask the owner. Otherwise it is damage.
4. **Clear only the bad segment:** `setFluid` mode `CLEAR` on the exact entities (it reports `before/after`, `boxesAffected`). Exclude the genuine producer line by its amount signature or you delete the resource you meant to keep. (Example: clearing 106 converted entities back to heavy oil while excluding the 12 true lubricant pipes preserved the lubricant line.)
5. **Restore intent:** connect the cleared network to a pipe/box that still holds the right fluid; re-pair underground pipes (`setDirection`); remove and re-place any pipe that stays on a private stub.
6. **Prove:** two windows >= 60 s apart show: every affected segment holds exactly one fluid (segment id, capacity, fluid), blocked machines leave `full_output`/`fluid_ingredient_shortage`, pumps `working`, totals move the expected direction.

`setFluid` can also seed a test or prime a segment (`SET`/`ADD`); treat it as admin, record the entity and amount, and prefer physical supply.

## 6. Boundaries

- Other agents may edit the same block mid-task: re-baseline counts, never delete foreign entities, record unexplained deltas instead of reconciling them.
- Never stop/restart the server for fluid repair.
- Waiting for fluid: `waitUntil{ fluid:{ target:{entity:"storage-tank", unitNumber:"..."}, fluid:"light-oil", atLeast:1000 } }` instead of polling. Targets need `entity` and `unitNumber` (`entity not found by unit number; pass entity too`).
