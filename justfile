# List available workflows.
default:
    @just --list

# Start the real save as a local RCON-enabled server.
start:
    @bash scripts/server.sh start

# Install the navigation mod without overwriting an existing installation.
navigation-install:
    #!/usr/bin/env bash
    set -euo pipefail
    mod_dir="${FACTORIO_MOD_DIR:-$HOME/Library/Application Support/factorio/mods}"
    target="$mod_dir/factorio_ai_navigation_1.0.0"
    mkdir -p "$mod_dir"
    if [[ -e "$target" || -L "$target" ]]; then
        echo "Navigation mod already exists at $target; refusing to overwrite." >&2
        exit 1
    fi
    ln -s "$PWD/mods/factorio_ai_navigation" "$target"
    echo "Navigation mod linked at $target. Restart Factorio to load it."

# Launch a client with separate data to watch the local server.
observe:
    #!/usr/bin/env bash
    set -eu
    client_dir="$HOME/factorio-client"
    mkdir -p "$client_dir"
    if [[ ! -e "$client_dir/config.ini" ]]; then
        printf '[path]\nread-data=__PATH__system-read-data__\nwrite-data=%s\n' "$client_dir" > "$client_dir/config.ini"
    fi
    exec "${FACTORIO_BIN:-/Applications/factorio.app/Contents/MacOS/factorio}" --config "$client_dir/config.ini"

# Save and cleanly stop the managed server.
stop:
    @bash scripts/server.sh stop

# Save and restart the managed server.
restart: stop start

# Request a save of the running game.
save:
    @bash scripts/server.sh save

# Show or follow the server log.
logs:
    @bash scripts/server.sh logs

logs-follow:
    @bash scripts/server.sh logs-follow

# Serve the GraphQL schema over HTTP for gqlcli TS and the agent harness.
graphql:
    #!/usr/bin/env bash
    set -euo pipefail
    exec env FACTORIO_RCON_PASSWORD="$(cat .factorio/password)" go run . serve --listen 127.0.0.1:8080

# Run the Pi Durable agent harness: agents status | ask ... | foreman ... | auto <role> ...
agents *args:
    @cd play/harness && node --experimental-strip-types src/main.ts {{args}}

# Start the agent web console in the background (idempotent; refuses a second writer).
agents-up:
    #!/usr/bin/env bash
    set -euo pipefail
    url=http://127.0.0.1:8787
    if curl -sf -m 2 "$url/api/state" >/dev/null 2>&1; then
        echo "agents already running: $url"
        exit 0
    fi
    cd play/harness
    mkdir -p agents
    nohup node --experimental-strip-types src/main.ts ui >> agents/ui.log 2>&1 &
    echo $! > agents/ui.pid
    for _ in $(seq 1 120); do
        if curl -sf -m 2 "$url/api/state" >/dev/null 2>&1; then
            echo "agents started: $url"
            echo "pid $(cat agents/ui.pid) — log play/harness/agents/ui.log — stop with: just agents-down"
            exit 0
        fi
        if ! kill -0 "$(cat agents/ui.pid)" 2>/dev/null; then
            echo "agents exited during start; last lines:" >&2
            tail -n 5 agents/ui.log >&2
            rm -f agents/ui.pid
            exit 1
        fi
        sleep 0.5
    done
    echo "agents did not answer on $url within 60s; check play/harness/agents/ui.log" >&2
    exit 1

# Stop the background agent web console.
agents-down:
    #!/usr/bin/env bash
    set -euo pipefail
    pidfile=play/harness/agents/ui.pid
    [[ -f $pidfile ]] || { echo "agents not started by just (no pid file)"; exit 0; }
    pid=$(cat "$pidfile")
    if kill "$pid" 2>/dev/null; then
        for _ in $(seq 1 40); do kill -0 "$pid" 2>/dev/null || break; sleep 0.25; done
        echo "stopped agents (pid $pid)"
    else
        echo "agents (pid $pid) was not running"
    fi
    rm -f "$pidfile"

# Show the agent console log.
agents-logs:
    @tail -n 40 play/harness/agents/ui.log
