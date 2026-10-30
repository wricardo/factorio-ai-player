# Factorio agents: a Pi Durable harness

Three durable agent roles over the repository's GraphQL API and its beads tickets.

```
planner  ──human chat──> files tickets, reads the factory, never mutates
foreman  ──judgment────> claims tickets, spawns one worker per ticket, steers, validates
worker   ──execution───> executes exactly one ticket, appends evidence, cannot close it
```

Each role is a separate conversation with its own tool set, in one process over one SQLite file.

## Run

```sh
just start            # the Factorio server (RCON)
just graphql          # GraphQL over HTTP on 127.0.0.1:8080 (repository-root .gqlcli.json)

just agents-up              # web console in the background: http://127.0.0.1:8787
just agents-down            # stop it (it is the only writer on the storage)
just agents-logs            # its log
just agents ui              # same console in the foreground, when nothing else runs
just agents chat            # interactive console, planner
just agents chat foreman    # interactive console, foreman
just agents ask "what should we work on next?"        # one planner turn, prints the answer
just agents auto foreman "claim the next ready ticket and get it done"
just agents status
```

## Web console

`just agents ui` serves a single page on loopback (`http://127.0.0.1:8787`, `--port N` to move it).

- **conversations**: planner, foreman, and every worker — click to switch; a green dot means a run is in flight.
- **transcript**: committed entries only, streamed with Server-Sent Events; the in-flight answer renders as a live
  line (`thinking…` while the model reasons, then the answer text).
- **composer**: Enter sends. While the conversation is working the message is submitted with `whenBusy: "steer"`,
  so it joins the current turn instead of queueing behind it. `abort` stops the run.
- **tickets ready / recent**: `bd ready` and `bd list --all`, so a closing ticket stays visible.
- **live tasks**: the durable task graph with each task's owner.

It runs in the same process as the harness, because Pi Durable allows one writer per storage: a second
harness process is refused by `agents/writer.lock` with the owner's pid (two writers corrupt entry ids and
kill both sessions). `just agents-up` is idempotent, so it is safe to run it twice; `just agents-down`
releases the lock. The page is a view over
committed state, so a reload reconstructs everything; the only server-side state is a 3 s `bd` cache.

## How the loop runs by itself

`app.dispatch` (a durable task on the foreman's conversation) polls `bd ready` every 60 s. For every ticket it
has not announced yet it posts one `[dispatch]` message to the foreman: id, lane, title, acceptance. It never
claims and never spawns — the foreman decides — but it enforces the lanes: nothing `world` is announced while
another `world` ticket is in progress, and announcements are remembered for 6 h so a restart does not spam.

So the loop is: **you talk to the planner → planner files tickets → dispatcher wakes the foreman → foreman claims
and spawns one worker per ticket → worker posts evidence → foreman validates with its own query and closes.**

## Talking to them

`chat` is the control surface: answers stream, tool calls print as `→ name {...}` / `← name result`, and
a message typed while a run is working is delivered as a **steer** (it joins the turn at its next step).

| command | effect |
|---|---|
| `/planner`, `/foreman` | switch which conversation you are talking to |
| `/history [n]` | replay the last n entries (default 10) |
| `/status` | conversation ids, live task count, workers, spend |
| `/tasks` | every live task with its status and owner |
| `/workers` | one line per worker: ticket, working/idle, conversation |
| `/abort` | stop the current run; the conversation stays usable |
| `/clear` (console) / **clear** button (web) | start a new context: older entries stay in storage, the model stops seeing them |
| `Ctrl-C` | interrupts a running turn, twice exits |
| `/quit` | close the harness |

Only one process may own the SQLite storage. A viewer (web UI, another TUI) attaches with
`Harness.open(storage, { readOnly: true })` plus `watch()` / `watchTaskGraph()`; it must not write.

`auto <role> <message>` asks once and then stays up until no task is runnable or sleeping, so
background reporter tasks can deliver worker answers back to the foreman.

Model: `opencode-go/deepseek-v4.1-flash` (pi-ai `opencodeGoProvider`). The key comes from
`OPENCODE_API_KEY`, else the gitignored `.opencodego-api-key` at the repository root.

## Capabilities are the design

| role | GraphQL | bd | workers |
|---|---|---|---|
| planner | read-only (`gqlcliExtension` without `mutations`) | ready/list/show/comments + create | — |
| foreman | read-only | + claim, heartbeat, update, close | spawn/send/stop/status |
| worker | read **and mutate** (`mutations: true`) | + comment only | — |

Consequences, not conventions:

- The planner cannot corrupt the save: no mutation tool exists in its registry.
- A worker cannot close its own ticket: `bd_close` is not in its extension.
- A crashed mutation is never repeated: gqlcli's mutation tool is not replay-safe, so Pi Durable
  reports the interruption and the model must query the world before acting.

## Durability

- Every tool call and model request is a task with a checkpoint. `harness.resume()` on start
  continues whatever the last process was doing.
- A worker conversation is owned by a background **anchor** task, so it outlives the foreman's
  turn and its Esc. A background **reporter** task delivers each message and posts the answer
  back, with request ids that make a restart neither resend nor re-report.
- Tickets are the shared source of truth: `bd` claim is the mutex, `bd heartbeat` is the lease,
  `bd_comments` is the evidence trail, `acceptance_criteria` is the validation contract.
- Only the foreman closes a ticket, and only after re-running the acceptance query itself.

## Files

| file | what |
|---|---|
| `src/config.ts` | paths, provider/model, key loading |
| `src/bd.ts` | typed `bd` tools per role (no CLI flags in the prompt) |
| `src/dispatcher.ts` | `bd ready` poller that wakes the foreman; lane rules live here, not in the prompt |
| `src/factory.ts` | the two gqlcli extensions and the role prompt laws |
| `src/workers.ts` | worker document, anchor + reporter tasks, the `worker` tool |
| `src/main.ts` | harness boot, conversation wiring, CLI |
| `src/list-models.ts` | `npm run models`: the provider catalog |

State lives in `agents/agents.sqlite` (gitignored). One process owns it: a UI attaches with
`Harness.open(storage, { readOnly: true })` and uses `watch()` / `watchTaskGraph()`.
