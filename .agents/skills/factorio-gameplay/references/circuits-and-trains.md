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
- `setCircuitBehavior(input:{actor,target, circuitEnableDisable, circuitCondition:{comparator,constant,firstSignal,secondSignal}, ... })` writes only the supported fields of that entity's control behavior. `SignalInput { name, type ("item"|"virtual"|"fluid"...), quality }`. Comparator strings: `>`, `<`, `=`, `>=`, `<=`, `!=` (also Unicode forms the engine accepts; verify with readback). Read the entity back: `inspect.settings`.
- `setCombinatorParameters(input:{actor,target,parameters:{arithmetic:{...} | decider:{...}}})` replaces arithmetic or decider parameters; exactly one family; arithmetic operations limited to `* / + - % ^ << >> AND OR XOR`; decider condition/output lists have 1-100 entries; invalid input fails before RCON.
- `setInserterSettings`, `setLampSettings`, `setSplitterSettings`, `setContainerBar`, `setLogisticRequests`, `setTrainStop` are the focused setters; read `inspect.settings` first and preserve unrelated settings.

## 2. Reading live circuit values (the `circuitNetwork` workaround)

Older `serve` builds fail `circuitNetwork(input:{target, color:RED|GREEN})` with `"red is not a valid WireColor"` (Lua returns lowercase wire names; the GraphQL enum is uppercase). The working tree normalizes the color; rebuild `serve` first. Until then, read a signal with a **probe inserter**:

1. Pick a tile within wire reach of the combinator/pole whose signal you need, where the inserter's **pickup and drop tiles hold no belt, chest or machine** (it must never move items). Check with `probePlacement`/`entities`.
2. `place` the inserter; `connectWire` GREEN from probe to the signal source.
3. `setCircuitBehavior` on the probe: `circuitEnableDisable:true`, `circuitCondition:{comparator:">", constant:K, firstSignal:{type:"virtual", name:"signal-C"}}` (use `type:"item"` for item signals).
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

- Reads: `trains(input:{surface, force, area?, station?})` shows state, manual mode, speed, schedule records, cargo/fluid. Locomotive is 2x6.
- Writes: `setTrainSchedule(input:{actor, target, clear, records:[{station, waitCondition:{type, ticks|condition, compareType}}]})` replaces the schedule. `type` is one of `TIME INACTIVITY FULL EMPTY ITEM_COUNT FLUID_COUNT PASSENGER_PRESENT PASSENGER_NOT_PRESENT ROBOTS_INACTIVE CIRCUIT OTHER`; read-back exposes the first common wait condition as `waitCondition` and the complete list as `waitConditionRawJSON`. Return type is `TrainInfo { id manualMode status speed locomotives wagons schedule cargo fluids }` (the train id is `id`, not `unitNumber`). `trainControl(input:{actor,target,manualMode,goToStation})` toggles manual mode and sends to a named station. `setTrainStop(name, train limit, priority, color)`.
- Both resolve the locomotive/carriage entity and enforce reach when `FACTORIO_ENFORCE_REACH=1`.
- Rails and belts ignore scripted `setDirection` in 2.0.77: place them with the final direction. Rail piece geometry/signals have no dedicated helper: use `createBlueprint*` + `placeBlueprint` and verify.
- Verify: schedule readback, station name, manual mode off, train moves (sample position across ticks), fuel, cargo, unloading capacity.
- Never assume a train route works from the schedule alone: signals, fuel, station capacity and unloading buffers are separate failure modes.
