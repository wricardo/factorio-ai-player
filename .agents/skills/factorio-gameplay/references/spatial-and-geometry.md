# Spatial reasoning and cell geometry

Most failed builds in this project are geometry mistakes, not API mistakes (see closed ticket `rxi.7`: inserters placed one tile off, drops missing targets, every adjacent placement colliding with an assembler). This file gives exact rules, verified against the live save (Factorio 2.0.77), and worked layouts. Always confirm with a read: `inspect`/`worldLogistics` report `pickupPosition` and `dropPosition`; `mapArea` shows the result.

## 1. Coordinates and directions

- Tile grid: **x grows east, y grows south** (north is negative y).
- Engine directions (`direction: Int`, 0-15): `0` north, `4` east, `8` south, `12` west. Odd-ish values (2, 6, 10, 14, and the 16-way set) matter only for rails/some 8-way entities. Use only 0/4/8/12 for everything else.
- Unit vectors used below: `N=(0,-1)`, `E=(1,0)`, `S=(0,1)`, `W=(-1,0)`.
- **Entity centre rule:** footprint width/height odd => centre on `.5`; even => centre on an integer. Evidence from the live save: 2x2 burner drill at `(-115,-39)` and 2x2 stone furnace at `(-113,-39)` are exactly adjacent (2 apart); 1x1 inserters sit at `(-113.5,-37.5)`; 3x3 assemblers use `.5`; 3x2 boiler `(45,-6.5)`.
- Rotation swaps width and height. A 3x2 boiler facing N/S is 3 wide by 2 tall; facing E/W it is 2 wide by 3 tall. Steam engine (3x5) likewise. Splitter is 2x1 across the travel direction: an east/west belt line uses a splitter 1 wide (x) by 2 tall (y) with centre y on an integer.
- Which coordinate does each operation take?
  - **Placement** (`place`, `placeMany`, `placeNextTo`, `connectEntities`): entity-centre position (snapped to the legal centre). Response `position` is the actual centre. Use that for everything that follows.
  - **Entity-addressed** (`inspect`, `transfer`, `fuel`, `remove`, `setRecipe`, ...): reported centre from `entities`/`nearby`, or `unitNumber` + `entity`.
  - `placeTile`: whole tile coordinates. `probePlacement`: samples tile **centres** (`x.5`) and reports blocked tile centres. Blueprint position is the top-left corner.

## 2. Footprints (verified with `prototype`)

| Entity | W x H | Notes |
|---|---|---|
| transport-belt, underground-belt, inserters, small/medium pole, chests, pipe, pipe-to-ground, offshore-pump | 1x1 | offshore-pump is 1x1 in this build; its fluid connection is on its output side |
| splitter | 2x1 | 2 across travel direction |
| pump | 1x2 | |
| stone-furnace, steel-furnace, burner-mining-drill, gun-turret, accumulator, big-pole, substation, train-stop | 2x2 | integer centres |
| electric-furnace, assembling-machine-1/2/3, chemical-plant, electric-mining-drill, pumpjack, lab, storage-tank, radar, solar-panel | 3x3 | `.5` centres |
| boiler | 3x2 | rotate to change |
| steam-engine | 3x5 | |
| roboport | 4x4 | integer centres |
| oil-refinery | 5x5 | `.5` centres |
| rocket-silo | 9x9 | `.5` centres |
| locomotive | 2x6 | |

`prototype(name:"x") { entity { tileWidth tileHeight collisionBox selectionBox } }` returns exact data. Ask it instead of trusting this table for a prototype you have not used.

Electric pole numbers (`supplyArea` = distance from centre; `wireReach` = max wire length; wire reach between two poles is the **smaller** of their two reaches):

| Pole | supplyArea | covers | wireReach |
|---|---|---|---|
| small-electric-pole | 2.5 | 5x5 | 7.5 |
| medium-electric-pole | 3.5 | 7x7 | 9 |
| big-electric-pole | 2 | 4x4 (its 2x2 body plus 1 tile) | 32 |
| substation | 9 | 18x18 | 18 |

A machine is powered if **any** part of its footprint lies in a pole's supply area. big pole to medium pole links only reach 9.

## 3. Inserters: the one rule

**An inserter's `direction` points to its PICKUP side. It drops on the opposite side.**

```
pickup = position + unit(direction) * 1        (long-handed: * 2)
drop   = position - unit(direction) * 1        (long-handed: * 2)
```

Live proof: burner inserters at `(-113.5,-37.5)` with `direction 0` report `pickupPosition (-113.5,-38.5)` (north) and `dropPosition (-113.5,-36.30)` (south). The drop point is ~0.2 tile past the tile centre toward the destination; for a belt this lands on the **far lane** (the lane farther from the inserter).

Design by saying what the inserter must do, then derive direction:

| Want | Inserter at | direction |
|---|---|---|
| take from the WEST neighbour, put into the EAST neighbour | between them | `12` (pickup west) |
| take from the EAST, put WEST | | `4` |
| take from the NORTH, put SOUTH | | `0` |
| take from the SOUTH, put NORTH | | `8` |

Adjacency arithmetic for a machine of half-size `h` (3x3: h=1.5, 2x2: h=1) centred at `(mx,my)`: the inserter touching its **east** face is at `(mx + h + 0.5, my + k)` where `k` is a row offset inside the machine's span (`-1,0,+1` for a 3x3 at integer rows from centre). Its far end (pickup or drop target) is at `mx + h + 1.5`. Examples (3x3 assembler at `(ax,ay)`, east side):

- output inserter: position `(ax+2, ay)`, direction `12` (pickup west = assembler), drop at `(ax+3, ay)` -> chest or belt tile.
- input inserter: position `(ax+2, ay)`, direction `4` (pickup east `(ax+3,ay)`), drop `(ax+1,ay)` inside the assembler.
- Placing an inserter at `(ax+1, ay)` or `(ax+1.5, ...)` collides with the assembler. `[INFERENCE]` this is the cause of the repeated "adjacent placement hits assembler collision" blocker recorded in `rxi.7`.

**Direct insertion between two 3x3 machines with one free tile column between them:** centres 4 apart (`A` at `ax`, `B` at `ax+4`); inserter at `ax+2`, `direction 12` moves A -> B (pickup `ax+1` inside A, drop `ax+3` inside B). Use `direction 4` for B -> A. See 6.3.

After every inserter batch run `scripts/inserter-audit.js` (or `inspect{logistics}`) and confirm `pickup`/`drop` are the intended entities.

Throughput (base game, measure before trusting): basic inserter chest-to-chest ~0.8-0.9 items/s, ~1.2/s from a belt; burner ~0.6/s; fast ~2.3/s; long-handed ~1.15/s. One inserter feeds one furnace/assembler comfortably; one inserter cannot carry 1 plate/s sustained for a hungry cell.

## 4. Belts

- A belt tile with direction `d` moves items toward `unit(d)`. It has two lanes: **left** and **right** relative to travel (for east-moving belt, left = north lane, right = south lane). `beltStretch` reports `left/right/unknown` totals per side.
- Inserter drops on the **far lane**. An inserter south of an east-moving belt fills the north (left) lane; one north of it fills the south (right) lane. To use both lanes put inserters on both sides.
- Side-loading: a belt joining from the side fills only the near lane of the receiving belt. Turn belts keep lane identity; a head-on merge needs a splitter.
- Yellow transport-belt: `beltSpeed 0.03125` tiles/tick = 1.875 tiles/s; 15 items/s total, 7.5 per lane. Fast: 0.0625 (30 items/s). Underground-belt `maxUndergroundDistance 5` (entrance and exit up to 5 tiles apart, i.e. at most 4 tiles between); `pipe-to-ground` distance is 10.
- Place belts **with the final direction**; `setDirection`/`rotate` are not reliable for belts in 2.0.77 (scripted direction writes are ignored). If a belt faces wrong, `remove` and re-place.
- Underground pair: set `undergroundType: INPUT` on the entrance (item enters) and `OUTPUT` on the exit, same direction as travel. The halves pair as soon as a facing half of the same tier exists, so entrance and exit may be placed in one `placeMany` batch or in separate calls; without the flags both halves become entrances and nothing connects. `connectEntities` handles this automatically.
- `undergroundNeighbour` on `inspect` is always null on 2.0.77 (README); prove pairing with `beltStretch` (a tunnel collapses to one hop) or with measured flow.

## 5. Drills, furnaces, chests

**A mining drill's `direction` is its DROP side** — the same sense as belt travel direction, and the *opposite* sense to an inserter (section 3):

| `direction` | 0 | 4 | 8 | 12 |
|---|---|---|---|---|
| drill / belt move toward | north (`-y`) | east (`+x`) | south (`+y`) | west (`-x`) |
| inserter (same int) drops toward | south (`+y`) | west (`-x`) | north (`-y`) | east (`+x`) |

The drop tile is one tile past the middle of the facing edge (3x3 drill: `centre ± 2`). Verified live on Factorio 2.0.77, tick 2919457: a drill at `(217.5,-93.5)` with `direction 8` dropped coal on the **ground** at `(217.5,-91.652)`, south of itself, reading `waiting_for_space_in_destination`; `direction 0` put the coal on the north belt and it read `working`. Re-confirmed tick 3672835 on the Z2 coal row: u11569 `(217.5,-93.5)` dir 0 and u11576 `(217.5,-97.5)` dir 8 both feed the west-bound belt at `y=-95.5` — the first from the north side, the second from the south side. `inspect.dropPosition` is null for drills, so confirm the side by where the ore lands.

- A mining drill outputs onto the tile in front of its facing edge (direction = output direction): a burner drill (2x2) facing east at `(-115,-39)` drops into a furnace at `(-113,-39)`; the furnace occupies the tile the drill outputs to. No inserter needed.
- Electric drill (3x3) outputs at the middle of the facing edge.
- Burner drills/furnaces/inserters burn fuel; electric ones do not. Fuel supply is part of the design (see `bootstrap-and-progression.md`).
- Pumpjack: centre tile must sit on the crude patch; fluid output box at `centre + (+1,-2)` (rotation-dependent: confirm with `inspect.fluidboxNeighbours`).
- Chests: `allowedSlots` is the usable slot count (null = no limit); `bar` is the raw engine index where the blocked area starts (see `engine-traps.md`).

## 6. Worked layouts

All coordinates are illustrative; recompute from your own anchor, probe, then place. `x ->` east, `y` down.

### 6.1 Drill-direct furnace with plate belt (iron cell, proven in the live save)

```
y=-39  [D D][F F]            D = burner-mining-drill 2x2 at (-115,-39), dir 4 (E)
y=-38  [D D][F F]            F = stone-furnace 2x2 at (-113,-39)
y=-37.5      i               i = burner-inserter (-113.5,-37.5) dir 0: picks N (furnace), drops S
y=-36.5 > > > > > > > >      belt row, direction 4 (E), plates travel to base
```

- Repeat every 5 tiles (`-115,-110,-105,...`), the 5 inserters share one belt row.
- Measured: 5 pairs gave 78-90 iron plates/min. Fuel: a burner drill burns ~1 coal per 26 s, furnaces slower; deliver coal or the cell stalls (the live save's burner cells are now `no_fuel`: fuel automation is part of the cell).

### 6.2 Assembler fed from a belt, output into a chest (north-to-south column)

Assembler centre `(ax,ay)`, 3x3, so it covers tile rows `ay-1`, `ay`, `ay+1`. Everything below is the column `x=ax`, south positive:

| y | tile | note |
|---|---|---|
| `ay-3` | belt (any direction, carrying the ingredient) | inserter pickup |
| `ay-2` | input inserter, `direction 0` | pickup `(ax,ay-3)` belt, drop `(ax,ay-1)` assembler |
| `ay-1 .. ay+1` | assembler | |
| `ay+2` | output inserter, `direction 0` | pickup `(ax,ay+1)` assembler, drop `(ax,ay+3)` |
| `ay+3` | chest (bounded with `setContainerBar`) or belt | |

Both inserters use `direction 0` because items flow south, so each pickup is the tile **north** of the inserter. A second ingredient uses a second inserter at `(ax+1, ay-2)` or `(ax-1, ay-2)` from the same or a parallel belt. Verify with `inspect(assembler){statusName recipe}` and `inspect{logistics{inserters{pickup{name} drop{name}}}}`.

### 6.3 Two-stage direct insertion (gears -> red science)

Gear assembler `G` at `(gx,gy)`, science assembler `S` at `(gx+4,gy)` (one free tile column at `gx+2`). Inserter at `(gx+2,gy)`, `direction 12` (pickup west = G at `(gx+1,gy)`, drop east = S at `(gx+3,gy)`). Feed `G` with iron plates from a belt/inserter on its north or south face (`6.2` pattern); feed `S` copper plates the same way on its own north or south face; take packs out of `S` with an inserter at `(gx+6,gy)`, `direction 12`, into a chest or a lab chain. Check `recipe` ratios (`iron-gear-wheel` 2 iron -> 1 gear in 0.5 s; `automation-science-pack` 1 copper + 1 gear, 5 s) so one gear assembler can feed several science assemblers.


### 6.4 Steam power block (live-proven chain)

```
water -> offshore-pump -> pipe(s) -> boiler (fuelled) -> steam-engine(s) -> poles -> consumers
```

Proven positions (placeholders): pump `(48.5,-6.5)` dir 4 (faces water east, outputs west); water pipes west of it; boiler `(45,-6.5)`; steam engine `(38.5,-6.5)`; coal chest `bar 2` + burner inserter feeding the boiler; poles every <=7 tiles. Rules: keep water and steam segments single-fluid; boiler needs fuel inserter; verify `fluids` on boiler and engine, and `electricNetworks` production > 0 for >=1800 ticks.

## 7. Placement procedure that avoids collisions

1. `mapArea` the target box (read legend). Note existing entities and resource tiles.
2. `probePlacement(prototype, area, mode:BLOCKED_TILES)` for the **largest** prototype in the block; for 3x3+ machines prefer `mode:SITES` with `siteWidth/siteHeight`, or `nearestPlacement` from an origin. For tiles under a machine, `probePlacement` samples centres only: the machine centre must be legal for the whole footprint, which `worldPlacement` / `nearestPlacement` check with the engine.
3. Compute all centres with the centre rule and the inserter formulas; write them down.
4. Place machines first, then inserters, then belts/pipes, then poles (poles can be skipped by `connectEntities kind:POWER_POLE_CHAIN`).
5. Read back with `entities` and `inspect{logistics}`; run `inserter-audit.js`.
6. Fix by `remove`+re-place, not by repeated `rotate`.
