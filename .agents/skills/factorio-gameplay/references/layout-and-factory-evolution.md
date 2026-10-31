# Layout, main bus, and factory evolution

## Design goal

Layout must support current production and the next likely expansion. Optimize for clear material flow, observable failures, and safe extension before optimizing compactness. Empty space costs little; rebuilding a tightly packed live factory costs production time and creates uncertain state.

## Factory stages

### Bootstrap

Build only enough manual or temporary production to establish automated plates, power, basic intermediates, construction items, and the first science packs. Temporary lines may be compact, but keep their outputs accessible and avoid placing them across obvious future routes.

### Early factory

Establish stable mining, smelting, power, basic circuits, science, and a small mall. Choose a material-flow spine: a small main bus, distributed production with dedicated belts, or another explicit routing plan. Reserve space for additional lanes and production blocks.

### Mid-game expansion

Oil, multiple science packs, upgraded logistics, construction robots, and larger power demand make tight layouts expensive. Separate production into modules. Add dedicated intermediate production where shared supply is saturated. Extend the bus or use trains for distant resources instead of threading long routes through crowded blocks.

### Late factory

Use rail, logistic networks, circuits, modules, beacons, and specialized production districts when scale justifies them. Preserve observable interfaces between blocks: named stations, bounded buffers, measured belts, dedicated fluids, and clear power networks.

## Main-bus strategy

A main bus is a shared corridor carrying common materials past production blocks. It is a strategy, not a mandatory shape and not proof of adequate throughput.

Good bus candidates have broad recurring demand:

- iron plates;
- copper plates;
- electronic circuits;
- steel plates;
- advanced circuits and processing units after demand grows;
- selected stone, brick, coal, plastic, sulfur, or batteries when local routing is worse.

Avoid putting every item on the bus. Low-volume products, fluids with local consumers, and single-use intermediates often fit better in dedicated branches or direct insertion.

Bus practices:

- select one extension direction and keep it unobstructed;
- reserve empty lanes before they are needed;
- place production blocks on one or both sides with clear branch points;
- take materials through splitters or dedicated taps; configure priority only for a demonstrated need;
- route block outputs back to the bus only when several downstream consumers need them;
- keep fluids in separate, labeled corridors and never mix fluid systems;
- measure lane starvation before adding balancers;
- treat a full belt as stored inventory, not proof of adequate production.

Four belt lanes separated by two tiles is a useful grouping convention. It is not a requirement. Actual spacing must account for underground-belt reach, splitter placement, crossings, poles, pipes, and future belt tiers.

## Expansion space

Reserve space intentionally for:

- extra machine rows in each production block;
- additional bus lanes and branch splitters;
- belt and pipe crossings;
- larger power poles and substations;
- roboports and logistic coverage overlap;
- train approaches, stackers, stations, and unloading buffers;
- beacons around machines if late-game upgrades are likely;
- walking, vehicle, or navigation corridors.

Before placing a block, record its bounds and extension direction. Use `probePlacement` for the current footprint and likely extension. Query nearby structures so a legal empty tile does not consume a strategic route.

Avoid these mid-game traps:

- building assemblers directly against both sides of every bus lane;
- enclosing production with permanent pipes or rail;
- routing unrelated belts through reserved extension space;
- placing stations without room for train length and signals;
- fitting oil production into a footprint with no output-storage expansion;
- growing steam power where water or fuel routing cannot scale;
- expanding mall buffers instead of increasing sustainable supply.

## Modular production blocks

A production block should expose clear interfaces:

- named inputs and required rates;
- named outputs and target rates;
- power and fluid requirements;
- bounded input or output buffers where needed;
- a legal footprint and extension direction;
- observable machines, belts, pipes, or stations for diagnosis.

Build one working slice before cloning a block. Verify recipe, power, every input, every output, and sustained operation. Clone only after the slice proves the design.

## Factory evolution

Do not treat the starter factory as the final layout. Evolve through measured cutovers:

1. Observe the current bottleneck and record existing producers, consumers, stock, and flow.
2. Decide whether to extend, duplicate, upgrade, or replace the constrained block.
3. Build the new path beside the old path when space permits.
4. Supply and test the new path without disabling the old one.
5. Transfer consumers or routing in bounded steps.
6. Verify production and downstream supply after each cutover.
7. Remove obsolete structures only after the replacement is stable.

Prefer parallel replacement over destructive in-place rebuilds for power, smelting, oil, science, and core intermediates. In-place upgrades are suitable when entity replacement preserves connections and buffers, but still require post-upgrade verification.

Common evolution choices:

- starter smelting to larger dedicated arrays;
- temporary coal delivery to automated fuel routing;
- hand-fed mall to belt-fed mall, then logistic-assisted mall;
- mixed science lines to modular science blocks;
- local ore belts to train-fed smelting or production districts;
- steam-only power to expanded steam, solar/accumulator, or nuclear systems;
- yellow logistics to higher tiers only where throughput or underground reach requires them.

Do not upgrade every belt because a faster tier is unlocked. Faster belts do not repair insufficient production, bad inserter geometry, blocked outputs, or incorrect lane routing.

## Choosing transport

Use transport based on distance, rate, and operational cost:

- direct insertion for stable adjacent producer-consumer pairs;
- belts for visible continuous local flow;
- pipes for one compatible fluid per connected system;
- logistic robots for flexible low-to-moderate volume inside adequate coverage;
- trains for distant or high-volume routes that justify stations, signaling, fuel, and buffers.

Avoid adding transport layers before their prerequisites are reliable. A train route without fuel, signals, station capacity, and unloading space creates more failure modes than a long belt.

## Verification through factorio-ai-player

Use:

- `worldResources`, `worldLogistics`, `entities`, and `surfaces` for site context;
- `probePlacement` for present and future footprints;
- `factoryGraph`, `adjacency`, and `beltRun` for structural connections;
- `beltThroughputStart` for measured flow;
- `machines`, `stock`, and `itemFlow` for bottlenecks and dependencies;
- `electricNetworks`, `powerConsumers`, and `fluidStock` for infrastructure;
- `trains` and `logisticNetworks` for scaled transport.

Layout proof: new block works, planned extension remains unobstructed, upstream systems sustain it, and downstream consumers receive required flow. Evolution proof: replacement works before obsolete path is removed, and production remains stable after cutover.
