# Multi-agent play, tickets, operations and escalation

How several agents (and a human) share one live save without damaging it, how work is tracked, and when to stop and report.

## 1. Shared-world rules

- **One live server, one shared save.** Never `just stop`, `just restart`, `shutdown`, reinstall the mod, change `setGameSpeed`, or switch saves during gameplay work. `just save` (`save` mutation) is safe and must precede any authorized restart: **`shutdown` does not save.**
- Before every write re-read the target: other agents edit the same area. Unfamiliar structures, markers and unit numbers belong to someone else; never delete or re-delete them.
- Verify mutations with reads and net-zero probes (place/remove) rather than speculative changes.
- Only one Factorio instance may use the default write-data directory. Creating a new map (or a new sandbox) requires the managed server to be stopped by an authorized human; follow `AGENTS.md` ("Starting a new sandbox game") and the `factorio-ai-player-new-map` skill, and always pass `FACTORIO_SAVE=<zip>` to `just start/restart` (the script defaults to `ai-player.zip`, silently reverting to a different save). The HTTP endpoint on `127.0.0.1:8080` stays valid across save changes.
- Keep RCON and the `serve` endpoint private: both are unauthenticated administrative control.
- Reach checks are off by default (`FACTORIO_ENFORCE_REACH=1` enables; restart `serve` to change). Navigation, mining and some interactions still behave best when the actor is close.

## 2. Workers (detached characters)

- `characterSpawn(input:{surface,x,y,force})` creates a free, empty detached character with its own reach and inventory; use its `unitNumber` as `actor:{character:"..."}` everywhere. The human's character is never moved by agent work.
- `characters(input:{surface, detachedOnly:false, limit})` lists all characters (no 256-tile area cap but a whole-surface scan per call).
- Workers persist in the save and are never cleaned up automatically: reuse them, drain inventories to a chest and leave them idle; `characterReturn` only works for characters created by `characterCreate` for a disconnected player.
- Hand-offs: `transfer` only moves between an actor and an entity inventory. To pass items between actors use a wooden chest (2 wood): depositor `transfer TO_ENTITY inventory:CHEST`, receiver `transfer TO_ACTOR`. Player main inventory cannot be addressed from a character; the player drains the chest.
- Parallel crafting: spawn N workers, `transfer` full ingredient sets, `craft(wait:false)` on each (queue is per actor and consumes all ingredients up front), then `waitUntil{craftQueueEmpty}` per worker.
- Navigation: `navigateStart(input:{actor, position|targetCharacter|targetEntity, tolerance, wait:true, waitTimeoutSeconds, timeoutTicks})`. One job per character; `tolerance` 0.5-10; chase needs tolerance >= 1 and fails `NO_PATH` among clutter: navigate to a position ~2 tiles short instead. Status is `EngineStatus{category rawName terminal}`; accept `ARRIVED`, treat other terminal names as failures (`NO_PATH`, `TIMEOUT`, `TARGET_MISSING`), do not loop. For harvest/mine reach go within ~1-2 tiles (`tolerance 1`).
- A route through unmapped territory can kill an unarmed worker if enemies exist; keep workers near the base or clear the route.
- One subagent per detached character works well (each holds its own ticket and drives `navigateStart` -> work -> verify). RCON serializes calls while navigation jobs advance on game ticks, so parallel workers do not block each other. All of them use gqlcli over HTTP.

## 3. Ticket workflow (Beads)

All gameplay work is tracked in `bd`, with `BEADS_ACTOR=ai-factorio-player`. The project agent `ai-factorio-player` (`.omp/agents/ai-factorio-player.md`) encodes this; the short version:

1. `bd prime`; `bd show <id>`; `bd update <id> --claim` (never steal active work).
2. Start comment: baseline tick/state, bounded outcome, actor/character, target area/entities, acceptance proof, stages; ASCII map when spatial.
3. Work in small dependency-ordered batches: obtain materials -> probe -> place core -> connect input/output -> power/fluid -> configure -> stabilize -> prove.
4. Comment every finding, anomaly and blocker with the exact GraphQL document, variables, tick, observed vs expected, entity ids/positions.
5. Close only after a final comment with the decisive verification query/result, changed entities, stock effects and remaining risks. `bd close` needs `--force` when actor differs from assignee.
6. Reproducible API/tool defects get their own issue with reproduction and acceptance criteria; link with a comment and `bd dep add` when blocking. Finish independent safe work before stopping.
7. Do not commit, push or sync Git/Dolt unless explicitly authorized.

Acceptance criteria that worked (copy the pattern): "rate >= N/min over **two** production windows (>= 600 ticks each); every drill/furnace `working`; **no manual feeding for 1800 ticks**; bounded buffers; unit numbers and positions recorded; no administrative operations used."

## 4. Narration, markers and screenshots

- `announce(message)` prints to all connected players; humans only see in-game chat. Announce each bounded batch before starting (<= 60 chars) and each verified milestone after it completes. Never announce success from a mutation acknowledgement, ghosts or queued crafts. Avoid unescaped quotes/backslashes. Blank messages fail.
- Chart tags (`addChartTag`, `chartTags`, `removeChartTag`) are durable shared landmarks: planned sites, patches, route endpoints, hazards, blockers. List nearby tags before adding; include ticket id and purpose in the text; record `tagNumber` in the ticket; remove temporary tags after acceptance; never remove unfamiliar tags; tags are annotations, never proof of current state. `addChartTag` needs nonblank text or an icon.
- `screenshot` needs a connected graphical client (`just observe`) whose `script-output` is readable by the GraphQL host (`FACTORIO_SCREENSHOT_DIR`); success = a verified PNG of the requested size. Screenshots are supplementary; exact GraphQL reads are the evidence.
- ASCII maps are useful for plans: derive every cell from `mapArea`/`entities`/`worldResources`, mark unsampled space `?`, state bounds and scale, distinguish existing from proposed.

## 5. Escalation: stop and raise anomalies

If a query/mutation, Lua payload, RCON call or in-game result looks wrong (unexpected error, null where data was promised, success with unchanged state, machine behaviour contradicting docs): **stop that workstream** and report the exact command/document, variables, tick, observed output and expected result. Do not hide it with console commands, scripts that mask the failure, or blind retries. Separate tool defect from game behaviour only after comparing schema, generated Lua and live response; mark unresolved suspicions `[INFERENCE]` and name the query/log line that would settle it.

Known open defects at time of writing (re-verify before filing duplicates):
- Older `serve` builds decode `circuitNetwork` wire colours as lowercase (invalid `WireColor`); the working tree normalizes them.
- Older `serve` builds decode inserter prototype `inserterPickup`/`inserterDrop` arrays as `Position`; the working tree normalizes them.

Valuable API gaps: record a concise proposal (need, workaround, suggested field/behaviour, cost) rather than implementing mid-task.

## 6. Administrative shortcuts

`teleport`, `setGameSpeed`, `setFluid`, `buildGhosts`, `unlockAllTechnologies` (and `setEntityTags` experiments) bypass normal progression. Use them only in deliberately chosen sandbox play or recovery with explicit authority, record each use, and never for acceptance criteria that forbid administrative operations.
