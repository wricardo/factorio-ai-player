# gqlcli workflow for Factorio play

How to drive the live game efficiently through `gqlcli` against `http://127.0.0.1:8080/query`. Load the `gqlcli` skill for the full CLI manual; this file records what matters for this API. Everything here was exercised against the live server unless marked `[INFERENCE]`.

## 1. Session start checklist

```sh
cd /Users/wricardo/go/src/github.com/wricardo/factorio-ai-player     # gqlcli reads .gqlcli.json from CWD
gqlcli config list                                                   # env `local` must point at http://127.0.0.1:8080/query
gqlcli op list                                                       # ~48 saved named ops; run these before writing documents
gqlcli query '{ status { tick speed paused players { name } } }'    # proves endpoint + RCON + game are alive
gqlcli schema refresh                                                # only if a field you expect is missing/invalid
```

- **Saved ops are the play interface.** `gqlcli op list` prints name + type + document for each entry; `gqlcli op show --name <name>` prints the document (name/type/query; the variables are yours to pass). Play with `gqlcli query --op <name> --variables '{...}'` / `gqlcli mutation --op <name> --input '{...}'` (stored variables merge with `--variables`; explicit values win). They cover the ordinary moves — read (`status`, `entities`, `nearby`, `inspect`, `inspect-many`, `stock`, `production`, `power`, `briefing`, `factory-alerts`, `belt-stretch`, `dead-belts`, `fluids`, `fluid-segments`, `research`, `trains`, `construction`, `ghosts`, `occupancy`) and write (`connection-plan`, `build-connection`, `place-belt`, `place-item`, `place-many`, `build-cell`, `set-recipe`, `transfer`, `remove-many`, `queue-research`, `spawn-character`, `wait-until`) — so pick the op for the move you want instead of assembling an equivalent request. Add a genuinely missing repeatable move with `gqlcli op save` (shared file: ask first, keep existing entries).

- **CWD matters.** Run from the repository root. From another directory gqlcli has no `.gqlcli.json`, falls back to a default URL and fails with `404 page not found` / `introspection failed`. Fix: `cd` to the repo or pass `--url http://127.0.0.1:8080/query`.
- If the endpoint is down, check `just logs` / the `serve` process before blaming the API, and report it. Never stop or restart the managed server for a gameplay task. gqlcli over HTTP is the only interface: no `go run`, no raw RCON.
- `status.paused` true or `speed` 0 means the world is frozen: crafting, navigation and `waitUntil` tick conditions never advance.
- Schema cache lasts 10 minutes. After a rebuild of the serve binary run `gqlcli schema refresh`.

## 2. Discovery commands (use before every unfamiliar operation)

```sh
gqlcli queries --args --desc | less            # every query with arg types and docs
gqlcli mutations --args --desc
gqlcli queries --filter belt --args --desc     # substring filter
gqlcli describe Mutation.placeMany --args --depth 1   # one operation + its input/return types
gqlcli describe InventoryKind                   # enum values
gqlcli describe EntityInspection --desc --depth 1
gqlcli validate --mutation 'mutation { ... }'  # offline schema check; nothing is sent
gqlcli query --validate-only '{ ... }'
```

Schema descriptions are the contract and often hold semantics the type name hides (for example `beltThroughputStart` rejects splitters and undergrounds; `connectEntities` refuses to change the world when the plan has conflicts). Read `--desc` before first use of any operation.

## 3. Output control

| Goal | Command |
|---|---|
| One value or a filtered list | `gqlcli query '...' --jq '.data.status.tick'` (built-in jq on the response envelope; start at `.data`) |
| Pretty JSON | `-f json-pretty` ; compact token-cheap default is `toon` |
| Save big output | `--output /tmp/x.json` then read the file; do not paste huge payloads into context |
| Fail on GraphQL errors | default `--strict=true` exits non-zero when `errors` is present |

`gqlcli script` has **no `--jq`** (`query` and `mutation` do). Truncated output (`artifact://...`) is normal for big reads; narrow selection sets and limits rather than paging through everything.

## 4. Choosing the right gqlcli mode

| Need | Mode |
|---|---|
| One operation | `query` / `mutation` |
| N independent reads known up front | one GraphQL document with aliases: `{ a: status{tick} b: production(...){...} }` (one HTTP call, still N separate RCON reads) |
| N independent operations, different shapes | `batch` (NDJSON, one op per line) |
| Dependent steps, loops, branching, waiting, computed positions | `script` with `async function run(gql, input)` |
| Repeated workflow | `gqlcli script save` / `gqlcli op save` into `.gqlcli.json` (ask before editing the shared file; keep existing entries) |

Variables beat string interpolation: pass `--variables '{"input":{...}}'` for queries, or `--input '{...}'` for a mutation whose argument is `$input`.

## 5. Script rules that bite

- **Always run mutation scripts with `--strict=false`.** In strict mode (default) any GraphQL error makes `gql.query/mutation` **throw** `GoError: GraphQL errors in response`, the `errors[]` and partial `data` are lost, and the script dies mid-batch. With `--strict=false` the call returns `{data, errors}` and the script decides. After a throw or lost response the world may already have changed: inspect before retrying.
- Inside a script, the thrown error carries details in `e.value.Response.errors` when you do wrap in try/catch.
- Scripts have no overall timeout. Use `gql.mutation(waitUntil ...)` (max 10 s per call) instead of busy polling in JS, and `--timeout 60` to bound each request.
- Return plain JSON-able values; large arrays should be reduced in JS before returning.
- `gql.each(items, worker, {concurrency, stopOnError})`: reads can use concurrency 4-8. **Mutations that touch the same entity or actor must use `concurrency: 1`** (RCON serializes calls, but ordering and shared inventories still matter).
- Numbers: `UnitNumber` arrives as a string; keep it a string. Positions are floats; compare with exact `.5` centers, not `===` on computed values.
- Read-only scripts that ship with this skill live in `scripts/`:

| Script | Purpose | Example |
|---|---|---|
| `scripts/briefing.js` | `factoryBriefing` + status in one compact result | `gqlcli script --strict=false --file .agents/skills/factorio-gameplay/scripts/briefing.js --arg '{"limit":10}'` |
| `scripts/survey.js` | Resource patches sorted by distance from a point | `... survey.js --arg '{"center":{"x":0,"y":0},"radius":128}'` |
| `scripts/measure-rate.js` | Real item rate as stock delta across N game ticks (uses `waitUntil`) | `... measure-rate.js --arg '{"items":["iron-plate"],"ticks":1800}'` |
| `scripts/inserter-audit.js` | Every inserter in an area has a real pickup and drop endpoint | `... inserter-audit.js --arg '{"area":{...}}'` |
| `scripts/place-row.js` | Probe then `placeMany` a straight line of one item (dry run by default) | `... place-row.js --arg '{"actor":{...},"item":"transport-belt","start":{"x":10.5,"y":4.5},"step":{"x":1,"y":0},"count":12,"direction":4}'` |

`place-row.js` places only when `apply:true`, and refuses when any covered tile is blocked. It still returns the placeMany envelope so you can verify; never re-run it blindly.

## 6. Waiting without burning tokens

Prefer `waitUntil` to repeated polling. Each poll is a separate RCON read; the game keeps simulating; nothing is mutated.

```graphql
mutation { waitUntil(input:{timeoutSeconds:10, pollSeconds:1,
  condition:{ machineStatus:{ target:{entity:"lab", unitNumber:"60"}, statuses:["working"] } } })
  { satisfied timedOut waitedSeconds tick last { status detail } } }
```

Condition families (exactly one per call): `tick`, `ticks` (N game ticks from the first poll — follows game speed), `inventory` (actor holds N of item), `craftQueueEmpty`, `machineStatus` (entity status name in a set), `research` (technology researched), `fluid` (entity holds N of a fluid), `production` (force produced N in a window). Rules:

- `timeoutSeconds` is 1-10 wall seconds (default 10; larger values are clamped). Every wait, including `tick` and `ticks`, is bounded by it: at speed 1, 10 s is ~600 ticks, at 3x ~1800. For longer game-time waits raise the simulation speed and restore it, then chain another bounded call; do not expect a single call to cover minutes.
- `satisfied:false, timedOut:true` is a normal result, not an error. A condition that becomes unobservable (entity removed, unknown technology) is an **error**, because the outcome is unknown.
- It is not a barrier for other agents; it blocks only your own call.
- Use `machineStatus` to wait for a machine to *leave* a bad state: poll for `working`, not for the absence of `no_power`.

### Never `sleep` the shell for long

- **`sleep` in bash is capped at 10 seconds.** Anything longer is a defect: it burns the tool deadline, looks hung, gives no progress signal, and leaves you blind while the world moves. If you catch yourself writing `sleep 30`, the wait is game time and it belongs in a `waitUntil` condition.
- Waiting at speed 1 costs wall time (60 ticks/s), and each call is capped at 10 s. `ticks` is a count to wait from the first poll, so it follows game speed and never needs a baseline tick read from you; for long game-time waits raise the simulation speed, wait, then restore the speed — chaining calls works for anything longer than one 10 s window:

```graphql
# 1) read the current speed S (and the tick, for your own notes)
query { status { tick speed paused } }
# 2) raise speed for the bounded wait
mutation { setGameSpeed(input:{speed:5, message:"speed 5: waiting 3600 ticks"}) { tick speed paused } }
# 3) wait 3600 game ticks (the CLI samples the baseline tick itself); multiplier ~5x shrinks wall time
mutation { waitUntil(input:{timeoutSeconds:10, pollSeconds:1, condition:{ ticks: 3600 } })
  { satisfied timedOut waitedSeconds tick last { detail } } }
# 4) restore S from step 1 (1 unless someone deliberately changed it)
mutation { setGameSpeed(input:{speed:1, message:"speed back to 1"}) { tick speed paused } }
```

- Costs of a speed bump: it changes tick rate for **every** force, machine and player on the server, so other agents' tick windows, rate measurements and job timeouts all compress or stretch with it. Restore the original `status.speed` in the same work batch, never leave the world accelerated, and say the multiplier and the restore in chat.
- Do not compensate for a slow measurement by raising speed silently: `production` windows and `measure-rate.js` deltas are game-tick based, so state the speed you measured at, or measure back at speed 1.
- `speed:0` or `paused:true` freezes the world for everyone (including your own `waitUntil`). Only use it to take a consistent snapshot of a shared save, and resume immediately.

## 7. Efficient reads

- Alias several reads in one document: status + briefing + inventory costs one round trip.
- `factoryBriefing` first when you do not yet know what is wrong; targeted queries after.
- Area reads cap at 1024 tiles per side. Window larger factories.
- Select only needed fields. `inspect`, `machines` and `factoryGraph` are wide; ask for specific sub-objects.
- Read-then-act: capture the `tick` of the baseline; compare against the `tick` of the verification read before computing rates.

## 8. Mutation hygiene

- `message` on mutations overrides the in-game notification text; leave it unset unless you are narrating. `announce(message)` posts free chat.
- Always include the result fields that prove effect (`unitNumber`, `position`, `moved`, `placed failed errors{index code message}`), then verify with a read.
- Batch mutations (`placeMany`, `removeMany`, `fuelMany`, `transferMany`, `connectEntities`) continue past per-item failures. Read `errors[]` (typed `code`: `VALIDATION LOOKUP REACH INVENTORY COLLISION ENGINE`, one-based `index`). `errors` lists only the first 20 failures; compare `placed + failed` with the request count.
- On a transport error or timeout the outcome is unknown. Query the targeted entity/inventory before any retry.

## 9. Query-field traps seen in live sessions

These exact mistakes appeared repeatedly in a 2026-10-31/11-01 gameplay session. Validation is local and free; run `gqlcli validate` or `query --validate-only` before executing an unfamiliar document.

| Wrong | Correct |
|---|---|
| `ActorInput { actor: ... }` | `ActorInput { player: "name" }` or `{ character: "123" }` |
| `inventory(input:{actor:{player:"x"}})` | `inventory(input:{player:"x"})` |
| `RecipeInput { item: "..." }` | `RecipeInput { actor: ActorInput, force: "player", recipe: "name" }` (actor optional) |
| `EntityRefInput { type: "..." }` | use `entity: "prototype"`; `type` is not an input field |
| `entity(input:{unitNumber:"42"}) { inspection { ... } }` | `entity(input:{entity:"lab",unitNumber:"42"}) { entity { inspection(actor:{character:"7"}) { ... } } }`; the actor argument is optional |
| `Entity.recipeName`; `EntitySnapshot.inspection` | recipe is in `EntityInspection.recipe`; snapshot wraps `.entity` |
| `EntityInspection { power }` | power is in `machines.diagnostics.power` or `powerConsumers` |
| `StockLocation { item count name inventory }` | `StockLocation { stack { name quality count } inventoryKind }` |
| `StockInput { category: "STORED" }` | `category: STORED` (or omit for all); filters locations, totals, and items |
| `ElectricNetworkInsight { production consumption networkId }` | `{ productionWatts consumptionWatts id }` |
| `PowerEntityPage { consumers }` | `{ powerEntities }` |
| `ProductionItem { name }`; `StockTotal { name }` | `{ item }` |
| `FactoryOverview { tick }` | `{ machineTick productionTick }` |
| bare `EngineStatus` | `status { rawName category terminal rawCode }` |
| `prerequisites` / `effects` without subfields | `prerequisites { name researched }`, `effects { type recipe dataJSON }` |
| `ResearchInsightsStatus.COMPLETED` | `RESEARCHED` |
| `ResearchState.currentTechnology` | `currentResearch` (the `technology` field echoes the requested input) |
| quoted enum values (`"CHEST"`, `"RESEARCHED"`, `"FROM"`) | unquoted `CHEST`, `RESEARCHED`, `TO_ACTOR` |
| `TransferDirection.FROM` | `TO_ENTITY` or `TO_ACTOR` |
| `TransferInput { from, to, announce }` | `{ actor, target, inventory, direction, item, count }` |
| `WorldResourcesInput { name }` | `{ resourceName }` |
| `WorldResourceEntry { bounds }` | `bounds` exists on `patches[]`, not individual entries |
| `FactoryAreaInput { min, max }` | `{ leftTopX, leftTopY, rightBottomX, rightBottomY }` |
| `EntitiesInput { query: ... }` | no `query`; `area` is required; select `entities { ... }` on the page |
| `mapArea(maxColumns:2000)` | maxRows/maxColumns are 1..1024 |
| `Mutation.transferItems`, `closeBeadsTicket` | not part of this API; use `transfer`/`transferMany` and `bd` |

Also: areas and most scans cap at 1024 tiles per side; `probePlacement` caps at 262144 sampled tiles; `adjacency` caps at 1024 tiles per side. Select object subfields (`position {x y}`, `from { ... }`, `status { ... }`) — GraphQL requires them. On error, gqlcli prints human text; use `-f json` if a downstream parser needs JSON.
