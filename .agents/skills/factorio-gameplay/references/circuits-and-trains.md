# Circuits, combinators and trains

Use only when a task needs wiring, conditions, counters or rail. Prefer plain belts, inserters and direct insertion; circuits make debugging harder. Re-read the live schema (`gqlcli describe CircuitBehaviorInput --args`) before writing: field sets differ per entity type.

## 1. Wiring and settings

```graphql
mutation { connectWire(input:{actor:{character:"N"}, color:GREEN,
  from:{target:{entity:"inserter", unitNumber:"A"}}, to:{target:{entity:"arithmetic-combinator", unitNumber:"B"}} })
  { connected } }
```

- `WireInput { actor, color: RED|GREEN, from, to: WireEndpointInput { target: EntityRefInput!, connector } , message }`. Targets need `entity` + `unitNumber`. Wire reach: copper wire length depends on the poles/entities involved (`prototype.wireReach`); small pole 7.5, medium 9, big 32; ordinary circuit entities 9.
- `disconnectWire` mirrors `connectWire`.
- `setCircuitBehavior(input:{actor,target, circuitEnableDisable, circuitCondition:{comparator,constant,firstSignal,secondSignal}, ... })` writes only the supported fields of that entity's control behavior. `SignalInput { name, type, quality }` where `type` is the `SignalType` enum `ITEM|FLUID|VIRTUAL|ENTITY|RECIPE|SPACE_LOCATION|ASTEROID_CHUNK|QUALITY` (default `ITEM`); the engine rejects any other type, and `virtual-signal` does not exist. Comparator strings: `>`, `<`, `=`, `>=`, `<=`, `!=` (also Unicode forms the engine accepts; verify with readback). Read the entity back: `inspect.settings`.
- `setCombinatorParameters(input:{actor,target,parameters:{arithmetic:{...} | decider:{...}}})` replaces arithmetic or decider parameters; exactly one family; arithmetic operations limited to `* / + - % ^ << >> AND OR XOR`; decider condition/output lists have 1-1000 entries; invalid input fails before RCON.
- `setInserterSettings`, `setLampSettings`, `setSplitterSettings`, `setContainerBar`, `setLogisticRequests`, `setTrainStop` are the focused setters; read `inspect.settings` first and preserve unrelated settings.

## 2. Reading live circuit values

Read the network directly, no probe needed:

- `circuitNetwork(input:{target, color:RED|GREEN, connector})` returns `signals{type name quality value}`, `connected`, `connector`, `networkId` and `networkCircuitCount`. The default connector is `circuit_<color>`, which on a combinator is its INPUT connector; pass `connector:"combinator_output_red"` (or `_green`) for the network the combinator drives.
- `circuitNetworks(input:{area, limit, memberLimit})` groups every network reachable from entities in an area by `networkId`, with that network's signals and its member connectors (entity, type, unit number, position, connector). Use it to understand a circuit setup instead of diffing per-entity reads.
- `entitySettings(input:{area, names, types, limit})` reads the settings snapshot (container/lamp/logistic/circuit/constantCombinator sections) for many entities in ONE Lua call instead of `entities{inspection{settings}}` (2 RCON calls per entity, and it fails for entities on another force).
- Constant combinators: `inspect.settings.constantCombinator { enabled, sections{index active multiplier group slots{index signal{type name quality} count}} }`; write them with `setConstantCombinator(input:{actor,target,enabled,sections,clearSections})`, where each slot `count` becomes the engine slot `min`.

A **probe inserter** still works when you need a boolean gate rather than the signals:

1. Pick a tile within wire reach of the combinator/pole whose signal you need, where the inserter's **pickup and drop tiles hold no belt, chest or machine** (it must never move items). Check with `probePlacement`/`entities`.
2. `place` the inserter; `connectWire` GREEN from probe to the signal source.
3. `setCircuitBehavior` on the probe: `circuitEnableDisable:true`, `circuitCondition:{comparator:">", constant:K, firstSignal:{type:VIRTUAL, name:"signal-C"}}` (use `type:ITEM` for item signals).
4. Read `inspect(probe).settings.inserter.circuit.disabledByControlBehavior` or `statusName`: `disabled_by_control_behavior` means value <= K; otherwise value > K.
5. Bisect K (one write + one read per step) to the exact value; confirm with both neighbouring thresholds.
6. `removeMany` the probe, recover it, and re-probe the tile to confirm clear. Unpowered probes prove nothing about the gate.

## 3. Counters, gates and memory cells

- A self-feeding arithmetic combinator (`C + 0 -> C`) latches whatever it last computed, including drift. Reset only while the counted pipeline is empty: `setCombinatorParameters` arithmetic with `firstConstant:0, secondConstant:0` and the same output signal (forces 0), then restore `firstSignal` with `networks:{green:true, red:true}` and `secondConstant:0`; verify 0 with the probe before trusting the gate.
- `enabled:false` is rejected for ordinary inserters; use circuit enable conditions.
- A condition only inhibits an inserter that is physically **wired**; an unwired condition does nothing.
- When a gate closes, held and in-transit items still land: expect finite overshoot.
- Mixing a container's item signal with inserter HOLD signals on one network produces spurious edges (counter drift). Rename the container signal through a one-tick arithmetic diode (`item * 1 -> virtual signal`) so edge detectors see only hands.
- Total accounting `T = staged + in-flight` cannot see items a player removes from staging; reopen the gate only when in-flight is truly 0 and the counter is accurate.
- Use `beltStretch` for physical sequences and totals; `beltRun` includes distant transport-line owner links (structural, not adjacency).

## 4. Trains

Use rail only when distance or throughput justifies stations, signals, fuel and unloading space.

- Reads: `trains(input:{surface, force, area?, station?})` shows state, manual mode, speed, schedule records, cargo/fluid, plus `trainId` (the Lua id, not a unit number) and `carriages` with their unit numbers. `trainStops(surface, force)` lists stops with `backerName`, nullable `trainsLimit` (null = unlimited), `trainsCount`, `assignedTrains` and `priority`. Locomotive is 2x6.
- Writes: `setTrainSchedule(input:{actor, target|trainId, clear, records:[{station, waitConditions:[...]}]})` replaces the schedule; a record needs at least one typed condition. `type` is one of `TIME INACTIVITY FULL EMPTY ITEM_COUNT FLUID_COUNT PASSENGER_PRESENT PASSENGER_NOT_PRESENT ROBOTS_INACTIVE CIRCUIT OTHER`; read-back exposes the typed `waitConditions` list, the first common condition as `waitCondition`, and the complete unmodified list as `waitConditionRawJSON`. Return type is `TrainInfo { trainId manualMode status speed locomotives carriages schedule cargo fluids }`. `trainControl(input:{actor,target|trainId,manualMode,goToStation})` toggles manual mode and sends to a named station. `setTrainStop(name, trainsLimit, clearTrainsLimit, priority, color)`.
- Both resolve the locomotive/carriage entity and enforce reach when `FACTORIO_ENFORCE_REACH=1`.
- Rails ignore scripted `setDirection` in 2.0.77, so `setDirection` falls back to quarter-turn `rotate` steps (`turns:1..3`) and you should place rails with the final direction. Transport belts do take the direct write (`turns:0`, readback confirmed, live tick ~1.22M). Rail piece geometry/signals have no dedicated helper: use `createBlueprint*` + `placeBlueprint` and verify.
- Verify: schedule readback, station name, manual mode off, train moves (sample position across ticks), fuel, cargo, unloading capacity.
- Never assume a train route works from the schedule alone: signals, fuel, station capacity and unloading buffers are separate failure modes.
