# Production math, ratios and sizing

Use this file to turn a target rate into machine counts, belts and power **before** placing anything, and to compute the implied ceiling of a live block. Recipe data below was read from the live save on 2026-11-01 (`recipe`, `prototype`); base-game constants are marked **[base]** and must be sanity-checked with a live read (`recipe`, `prototype`, `inspect`) when a number decides a build. `enabled:false` in a recipe means not yet researched in this save.

## 1. Core formulas

```
machines = demand_per_s x recipe_time_s / (craftingSpeed x products_per_craft)
rate_per_s (per machine) = craftingSpeed x products_per_craft / recipe_time_s
items per belt = 15 /s yellow (7.5 /s per lane), 30 /s fast  [base; beltSpeed 0.03125 / 0.0625 tiles per tick, live]
lab units per second = labs x labSpeed / unit_time_s            (unit_time_s = researchUnitEnergy / 60)
research time (s) = researchUnitCount x unit_time_s / (labs x labSpeed)
```

`craftingSpeed` (live `prototype`): assembler-1 0.5, assembler-2 0.75, assembler-3 1.25, chemical plant 1, refinery 1, stone furnace 1, steel/electric furnace 2, lab speed 1 (base). `productsPerCraft` is the product amount in the recipe (belt recipe yields 2).

## 2. Verified recipes (live save)

| Item | Time | Machine class | Ingredients -> products |
|---|---|---|---|
| iron/copper plate | 3.2 s | furnace | 1 ore -> 1 plate [base] |
| steel-plate | 16 s | furnace | 5 iron-plate -> 1 |
| stone-brick | 3.2 s | furnace | 2 stone -> 1 |
| iron-gear-wheel | 0.5 s | crafting | 2 iron-plate -> 1 |
| copper-cable | 0.5 s | electronics | 1 copper-plate -> 2 |
| electronic-circuit | 0.5 s | electronics | 1 iron-plate + 3 copper-cable -> 1 |
| advanced-circuit | 6 s | electronics | 2 plastic-bar + 4 copper-cable + 2 electronic-circuit -> 1 |
| processing-unit | 10 s | electronics-with-fluid | 20 electronic-circuit + 2 advanced-circuit + 5 sulfuric-acid -> 1 |
| pipe | 0.5 s | crafting | 1 iron-plate -> 1 |
| transport-belt | 0.5 s | pressing | 1 iron-plate + 1 iron-gear-wheel -> **2** |
| inserter | 0.5 s | crafting | 1 iron-plate + 1 iron-gear-wheel + 1 electronic-circuit -> 1 |
| burner-inserter | 0.5 s | crafting | 1 iron-plate + 1 iron-gear-wheel -> 1 |
| small-electric-pole | 0.5 s | electronics | 1 wood + 2 copper-cable -> **2** |
| iron-chest / wooden-chest | 0.5 s | crafting | 8 iron-plate / 2 wood -> 1 |
| stone-furnace | 0.5 s | crafting | 5 stone -> 1 |
| burner-mining-drill | 2 s | crafting | 3 iron-plate + 3 gear + 1 stone-furnace -> 1 |
| electric-mining-drill | 2 s | crafting | 10 iron-plate + 5 gear + 3 circuit -> 1 (needs tech `electric-mining-drill`) |
| assembling-machine-1 | 0.5 s | crafting | 9 iron-plate + 5 gear + 3 circuit -> 1 |
| assembling-machine-2 | 0.5 s | crafting | 2 steel-plate + 5 gear + 3 circuit + 1 AM1 -> 1 |
| lab | 2 s | crafting | 10 gear + 10 circuit + 4 transport-belt -> 1 |
| boiler | 0.5 s | crafting | 4 pipe + 1 stone-furnace -> 1 |
| steam-engine | 0.5 s | crafting | 10 iron-plate + 8 gear + 5 pipe -> 1 |
| offshore-pump | 0.5 s | crafting | 2 gear + 3 pipe -> 1 |
| repair-pack | 0.5 s | crafting | 2 gear + 2 circuit -> 1 |
| automation-science-pack (red) | 5 s | crafting | 1 copper-plate + 1 gear -> 1 |
| logistic-science-pack (green) | 6 s | crafting | 1 transport-belt + 1 inserter -> 1 |
| chemical-science-pack (blue) | 24 s | crafting | 1 sulfur + 3 advanced-circuit + 2 engine-unit -> **2** |
| military-science-pack | 10 s | crafting | 1 piercing-rounds-magazine + 1 grenade + 2 stone-wall -> **2** |
| production-science-pack | 21 s | crafting | 30 rail + 1 electric-furnace + 1 productivity-module -> **3** |
| utility-science-pack | 21 s | crafting | 2 processing-unit + 1 flying-robot-frame + 3 low-density-structure -> **3** |
| engine-unit | 10 s | advanced-crafting | 1 steel-plate + 1 gear + 2 pipe -> 1 |
| electric-engine-unit | 10 s | crafting-with-fluid | 2 circuit + 1 engine-unit + 15 lubricant -> 1 |
| flying-robot-frame | 20 s | crafting | 1 steel + 2 battery + 3 circuit + 1 electric-engine-unit -> 1 |
| low-density-structure | 15 s | crafting | 20 copper-plate + 2 steel-plate + 5 plastic-bar -> 1 |
| plastic-bar | 1 s | chemistry | 1 coal + 20 petroleum-gas -> **2** |
| sulfur | 1 s | chemistry | 30 water + 30 petroleum-gas -> **2** |
| lubricant | 1 s | chemistry | 10 heavy-oil -> 10 |
| rocket-fuel | 15 s | organic-or-assembling | 10 solid-fuel + 10 light-oil -> 1 |
| rocket-part | 3 s | rocket-building | 1 processing-unit + 1 low-density-structure + 1 rocket-fuel -> 1 |
| rocket-silo | 30 s | crafting | 1000 steel + 1000 concrete + 200 processing-unit + 200 electric-engine-unit + 100 pipe -> 1 |
| pumpjack | 5 s | crafting | 5 steel + 10 gear + 5 circuit + 10 pipe -> 1 |
| oil-refinery | 8 s | crafting | 15 steel + 10 gear + 10 circuit + 10 pipe + 10 stone-brick -> 1 |
| chemical-plant | 5 s | crafting | 5 steel + 5 gear + 5 circuit + 5 pipe -> 1 |
| basic-oil-processing | 5 s | oil-processing | 100 crude-oil -> 45 petroleum-gas |
| advanced-oil-processing | 5 s | oil-processing | 50 water + 100 crude-oil -> 25 heavy-oil + 45 light-oil + 55 petroleum-gas |
| solid-fuel-from-light-oil | 1 s | chemistry | 10 light-oil -> 1 solid-fuel |
| sulfuric-acid | 1 s | chemistry | 1 iron-plate + 5 sulfur + 100 water -> 50 sulfuric-acid |
| battery | 4 s | chemistry-or-cryogenics | 1 iron-plate + 1 copper-plate + 20 sulfuric-acid -> 1 |
| concrete | 10 s | crafting-with-fluid | 1 iron-ore + 5 stone-brick + 100 water -> 10 |
| steel-furnace | 3 s | crafting | 6 steel-plate + 10 stone-brick -> 1 |
| electric-furnace | 5 s | crafting | 10 steel-plate + 5 advanced-circuit + 10 stone-brick -> 1 |
| splitter / underground-belt | 1 s | pressing | 5 iron + 5 circuit + 4 belt -> 1 / 10 iron + 5 belt -> 2 |
| medium-electric-pole | 0.5 s | electronics | 2 steel-plate + 4 iron-stick + 2 copper-cable -> 1 |
| gun-turret | 8 s | crafting | 20 iron-plate + 10 copper-plate + 10 gear -> 1 |
| firearm-magazine / piercing-rounds-magazine | 1 s / 6 s | crafting | 4 iron -> 1 / 2 copper + 1 steel + 2 firearm-magazine -> 2 |

Space Age: `space-science-pack` = 1 ice + 2 iron + 1 carbon -> 5 (platform item, not Nauvis ground production). Always `recipe(...)` before planning: mods, planets and quality change both ingredients and machine categories.

## 3. Raw cost per science pack (derived from the table; iron + copper plates)

| Pack | Iron | Copper | Other |
|---|---|---|---|
| red | 2 (gear) | 1 | |
| green | 5.5 (inserter 4 + belt 1.5) | 1.5 | |
| blue (per pack) | 12 | 7.5 | 37.5 petroleum-gas, 1.5 coal, 7.5 water |

Blue derivation per 2 packs: 3 advanced circuits (6 iron, 15 copper, 6 plastic) + 2 engines (18 iron) + 1 sulfur; 6 plastic needs 60 gas + 3 coal; 1 sulfur needs 15 gas + 15 water. So one belt of 15 plates/s cannot feed more than roughly 1.25 blue packs/s of iron alone; check copper and gas first.

## 4. Sizing worked examples

Red science at 1 pack/s: assembler-1 makes 0.5 x 1 / 5 = 0.1/s -> **10 assemblers**; they consume 1 copper/s + 1 gear/s = 2 iron/s; a gear assembler-1 makes 0.5 x 1 / 0.5 = 1 gear/s, so **1 gear assembler** feeds them (belt 3 plates/s total). Furnaces for 3 plates/s: 3 / 0.3125 = **10 stone furnaces** (5 with steel furnaces).

Smelting: stone furnace 0.3125 plate/s = 18.75/min; to saturate one yellow belt (15/s) you need 48 stone / 24 steel-or-electric furnaces. A cell of 5 burner-drill -> furnace pairs measured 78-90 iron plates/min in this save (ticket `rxi.5`); the [base] burner-drill ceiling would be 15 ore/min per drill, so always trust the measured number and re-measure after changes.

Mining [base]: burner drill 0.25 ore/s, electric drill 0.5 ore/s; one electric drill roughly feeds 1.6 stone furnaces. About 30 electric drills saturate a yellow belt. Check patch size: `totalAmount` / (drills x 0.5/s) = depletion time.

Labs: tech `logistic-science-pack` = 75 units x 5 s (`researchUnitEnergy 300` ticks); 5 labs finish in 75 x 5 / 5 = 75 s if packs keep up. `rocket-silo` = 1000 units x 60 s (`researchUnitEnergy 3600`) with red+green+blue: 20 labs take 3000 s (50 min at speed 1). Sustained pack need = labs / unit_time per pack type: 20 labs at 60 s units = 0.33 pack/s of each type.

Inserters: one inserter moves ~0.8-1.2 items/s, so a furnace that consumes 0.3125 ore/s is fine with one; a belt-fed cell that needs 2 plates/s needs two inserters or a stack bonus.

## 5. Power sizing [base, confirm with `electricNetworks`]

| Item | Value |
|---|---|
| steam engine | 900 kW |
| boiler | 1.8 MW (feeds 2 engines); burns 4 MJ coal at full load ~0.45 coal/s |
| offshore pump | 1200 water/s (supports 20 boilers) |
| lab 60 kW, assembler-1 75 kW, assembler-2 150 kW, assembler-3 375 kW, electric drill 90 kW, inserter 13 kW, electric furnace 180 kW, chemical plant 210 kW, refinery 420 kW, pumpjack 90 kW, radar 300 kW | |

Live evidence: one boiler + one engine + 7 poles carried a lab and test load with ~893 kW spare. Plan generation >= 1.3 x projected load; add boilers/engines before exceeding 85 %. Solar is ~42 kW average and needs accumulators for night; judge it across a whole day cycle.

## 6. Fuel, wood and chests

Coal 4 MJ, wood 2 MJ, solid fuel 12 MJ, rocket fuel 100 MJ [live prototype]. One burner drill ~1 coal per 26 s; one stone furnace uses 90 kW (0.0225 coal/s) [base]. A bounded chest (`bar 2` = 1 slot = 50 coal) next to a burner inserter is the standard boiler/burner feed; do not leave burner cells on manual refuelling.

## 7. Rocket totals for one 50-part launch (derived)

50 rocket-parts => 50 processing-unit + 50 low-density-structure + 50 rocket-fuel, i.e. 1000 circuits + 100 advanced circuits + 250 sulfuric acid + 1000 copper + 100 steel + 250 plastic (LDS) + 500 solid fuel + 500 light oil (RF). Plus silo build: 1000 steel, 1000 concrete, 200 PU, 200 EEU, 100 pipe. Verify with `billOfMaterials` and the live `rocketParts` requirement before committing capacity.

## 8. Implied-ceiling checklist (before blaming anything)

1. Count working machines of the recipe (`machines` summaries or `factoryBriefing.machines`).
2. Multiply by `rate_per_s`. That is the ceiling; compare it to measured production (`production` window, or `scripts/measure-rate.js` stock deltas).
3. Measured << ceiling: some machines are not `working` (diagnose). Measured ~= ceiling and demand is higher: add machines or faster recipe.
4. Compare upstream ceilings in the same way until one stage is short. That stage is the binding constraint; fix it, then re-measure.
