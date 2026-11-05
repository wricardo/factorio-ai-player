# Automation and mall strategy

## Objective

Build systems that continuously produce, move, and buffer required items. Hand crafting is a bootstrap tool, not the factory's production model. A task is not complete when the actor owns enough items for one build; it is complete when recurring demand has a supplied production path.

Use hand crafting only when one of these conditions holds:

- bootstrapping the first machines before automation exists;
- producing a low-volume singleton whose automation would cost more than likely future demand;
- recovering from a broken factory when the repair item is unavailable;
- proving a recipe before committing space and infrastructure.

Repeated hand crafting, repeated manual machine loading, or repeated chest-to-chest transfers reveal missing automation. Record the repeated item, identify its recipe and consumers, then add it to a production cell or mall. **Bulk hand-shuttling of production input/output is not a supply method** (MUST NOT 12): `transfer`/`transferMany` batches and chest shuttling that keep a cell running belong in a belt, robot route or producing cell — if the item has to move again, build that instead. Setup and hand-off transfers are exempt: worker craft-ingredient staging, robot loading, provider-chest bulk loads for construction, construction kits, and actor hand-offs. When the player directs a bulk move, record it and name the automated path that replaces it.

## Automation ladder

Progress through these levels. Do not skip verification between levels.

1. **Manual bootstrap:** obtain enough plates, gears, circuits, and machines to create the first automated cells.
2. **Automated intermediates:** continuously produce gears, circuits, and other ingredients consumed by several products.
3. **Early mall:** automate common construction items with bounded output buffers.
4. **Automated science:** feed science assemblers and labs without actor intervention.
5. **Automated resource expansion:** mining, smelting, power, and logistics replace temporary manual feeds.
6. **Mid-game mall:** add upgraded logistics, fluid infrastructure, rail components, modules, and replacement equipment.
7. **Late-game supply:** use logistic robots, trains, circuits, and upgrade chains where they reduce operating cost or expansion time.

Proof for each level: inputs arrive without actor transfers, machines work over multiple cycles, outputs leave machines, and buffers refill after items are removed.

## Early mall

A mall is a bounded group of production cells that makes factory-building items. It supports expansion; it must not starve science or core intermediates.

Good early products:

- transport belts;
- underground belts;
- splitters;
- inserters and long-handed inserters;
- electric mining drills;
- small and medium electric poles as technologies permit;
- assembling machines;
- stone or steel furnaces when still useful;
- pipes and underground pipes;
- chests;

Start small. One assembler per product is usually enough until measured demand proves otherwise. Share gears, circuits, belts, and plates through a bus or dedicated supply belts. Prefer direct insertion where two adjacent mall recipes form a stable producer-consumer pair, but keep shared intermediates available to science.

Every mall output needs a bound. Use chest bars, small dedicated buffers, or logistic request limits. Unlimited mall output can consume all plates and circuits while completed products sit unused.

## Growing the mall

Grow from observed construction demand, not from a list of every craftable item.

1. Query stock and inspect mall output buffers.
2. Identify items repeatedly hand-crafted, manually transferred, or missing during expansion.
3. Discover their enabled recipes and technology prerequisites.
4. Reuse existing intermediates when capacity exists; add upstream capacity when measured consumption causes shortages.
5. Reserve a legal footprint and route inputs before placing machines.
6. Add one complete cell with bounded output.
7. Remove items from the buffer and verify automatic refill.

Typical mid-game additions:

- fast belts, underground belts, and splitters;
- filter, stack, and fast inserters;
- medium and big electric poles, substations;
- higher-tier assembling machines and furnaces;
- pumps, storage tanks, chemical plants, refineries;
- rails, signals, stations, locomotives, and wagons;
- roboports, construction robots, logistic robots, and logistic chests;
- accumulators, solar panels, and steam expansion parts;
- modules and beacons when their cost is justified;

Do not replace every low-tier mall cell immediately after unlocking an upgrade. Preserve low-tier production when the upgraded recipe consumes that item. Add an upgrade cell downstream, keep bounded buffers for both tiers, and increase upstream supply only when measured demand requires it.

## Mall layout

Use a repeatable pattern:

- shared input corridor for plates, gears, circuits, stone, and other common materials;
- assembler row with room for extra inserters and recipe changes;
- output side with one bounded chest or logistic provider per product;
- power and walking or robot-access corridor;
- reserved extension at one end for new products;
- labels or stable recorded coordinates for output lookup.

Avoid weaving mall outputs back through input lanes. Avoid placing permanent power poles, pipes, or unrelated machines in the extension corridor. If the mall outgrows its supply or footprint, build a larger replacement beside the old mall, verify it, then retire obsolete cells deliberately.

## Protecting science and production

Mall demand is bursty. Science demand is sustained. Use these controls:

- bound every mall output;
- give critical intermediates separate production or explicit routing priority only after observing starvation;
- avoid large buffers for expensive, rarely used items;
- measure belt flow before adding balancers or duplicate producers;
- inspect `stock`, `itemFlow`, machine diagnostics, and belt throughput after mall expansion;
- reserve construction inventory without draining fuel, science packs, or active machine inputs.

## Verification through factorio-ai-player

Useful operations:

- `itemRecipes`, `recipe`, and `billOfMaterials` for recipe and dependency discovery;
- `stock` and actor `inventory` for demand and buffer checks;
- `itemFlow` for configured producers and consumers;
- `probePlacement` for cell and extension footprints;
- `machines` and `inspect` for working state and inventory movement;
- `setContainerBar` for bounded chest outputs;
- `beltRun` and `beltThroughputStart` for structure and measured supply;
- `factoryGraph` for connections between cells.

Automation proof requires observation across time. Query before taking mall output, remove a known quantity, wait for production, then confirm the buffer refills and upstream production remains healthy.
