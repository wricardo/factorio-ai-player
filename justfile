# List available workflows.
default:
    @just --list

# Run the default Go test suite (no Factorio server required).
test:
    go test ./...


# Regenerate GraphQL models, execution code, and resolver adapters with pinned gqlgen.
generate:
    @bash scripts/generate.sh

# Verify formatting, generated files, vet, build, and tests without rewriting this checkout.
check:
    @bash scripts/check.sh

# Run the opt-in suite against a disposable real Factorio server.
test-integration:
    go test -tags integration -run TestIntegration -v ./...

# Start the real save as a local RCON-enabled server.
start:
    @bash scripts/server.sh start

# Create a new regular freeplay save with the repository map settings (stop the server first).
new-game name:
    #!/usr/bin/env bash
    set -euo pipefail
    if [[ -f .factorio/pid ]] && kill -0 "$(cat .factorio/pid)" 2>/dev/null; then
        echo 'error: managed server is running; run just stop first' >&2
        exit 1
    fi
    bash scripts/world-settings-check.sh
    saves="$HOME/Library/Application Support/factorio/saves"
    save="$saves/{{name}}.zip"
    if [[ -e "$save" ]]; then
        echo "error: $save already exists; refusing to overwrite" >&2
        exit 1
    fi
    mkdir -p "$saves"
    "${FACTORIO_BIN:-/Applications/factorio.app/Contents/MacOS/factorio}" \
        --create "$save" \
        --map-gen-settings "$PWD/map-gen-settings-regular.json" \
        --map-settings "$PWD/map-settings.json"
    [[ -f "$save" ]] || { echo 'error: save was not created' >&2; exit 1; }
    printf 'Created %s\nStart it with: FACTORIO_SAVE="%s" just start\n' "$save" "$save"

# Verify the map-gen/map-settings files keep new worlds free of enemies.
world-settings-check:
    @bash scripts/world-settings-check.sh

# Install the navigation mod without overwriting an existing installation.
navigation-install:
    #!/usr/bin/env bash
    set -euo pipefail
    mod_dir="${FACTORIO_MOD_DIR:-$HOME/Library/Application Support/factorio/mods}"
    version="$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' mods/factorio_ai_navigation/info.json | head -1)"
    if [[ -z "$version" ]]; then
        echo "Could not read the mod version from mods/factorio_ai_navigation/info.json." >&2
        exit 1
    fi
    target="$mod_dir/factorio_ai_navigation_$version"
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

# Serve the GraphQL schema over HTTP for gqlcli TS clients.
graphql:
    #!/usr/bin/env bash
    set -euo pipefail
    exec env FACTORIO_RCON_PASSWORD="$(cat .factorio/password)" go run . serve --listen 127.0.0.1:8080

# Stop your processes listening on TCP 127.0.0.1:8080.
graphql-stop:
    #!/usr/bin/env bash
    set -euo pipefail
    port=8080
    pids=$(lsof -nP -a -u "$(id -u)" -iTCP:$port -sTCP:LISTEN -t 2>/dev/null || true)
    if [[ -z "${pids//[[:space:]]/}" ]]; then
        echo "nothing listening on 127.0.0.1:$port"
        exit 0
    fi
    echo "stopping: $pids"
    for pid in $pids; do kill -TERM "$pid" 2>/dev/null || true; done
    for _ in $(seq 50); do
        lsof -nP -a -u "$(id -u)" -iTCP:$port -sTCP:LISTEN -t >/dev/null 2>&1 || {
            echo "127.0.0.1:$port is free"
            exit 0
        }
        sleep 0.1
    done
    for pid in $(lsof -nP -a -u "$(id -u)" -iTCP:$port -sTCP:LISTEN -t 2>/dev/null || true); do
        kill -KILL "$pid" 2>/dev/null || true
    done
    sleep 0.2
    if lsof -nP -a -u "$(id -u)" -iTCP:$port -sTCP:LISTEN -t >/dev/null 2>&1; then
        echo "error: 127.0.0.1:$port still in use" >&2
        exit 1
    fi
    echo "127.0.0.1:$port is free"

# Restart the GraphQL HTTP server: kill your :8080 listeners, start a fresh one, return.
graphql-restart: graphql-stop
    #!/usr/bin/env bash
    set -euo pipefail
    log=".factorio/graphql.log"
    mkdir -p .factorio
    : > "$log"
    nohup env FACTORIO_RCON_PASSWORD="$(cat .factorio/password)" \
        go run . serve --listen 127.0.0.1:8080 >>"$log" 2>&1 < /dev/null &
    pid=$!
    for _ in $(seq 120); do
        if curl -fsS -o /dev/null -X POST -H 'Content-Type: application/json' \
            -d '{"query":"{ __typename }"}' http://127.0.0.1:8080/query 2>/dev/null; then
            echo "GraphQL endpoint: http://127.0.0.1:8080/query (pid $pid, log $log)"
            exit 0
        fi
        kill -0 "$pid" 2>/dev/null || { echo "error: server exited during startup" >&2; tail -n 20 "$log" >&2; exit 1; }
        sleep 0.5
    done
    echo "error: server did not answer within 60s" >&2
    tail -n 20 "$log" >&2
    exit 1

# Start Memgraph (Bolt 127.0.0.1:7687) and Memgraph Lab (http://127.0.0.1:3000) for the factory graph.
graph-up:
    @bash scripts/graph-compose.sh up -d --wait
    @echo "Memgraph bolt://127.0.0.1:7687, Lab http://127.0.0.1:3000"

# Stop Memgraph and Lab, keeping the graph volume.
graph-down:
    @bash scripts/graph-compose.sh down

# Delete the Memgraph volume (every loaded snapshot) after stopping the stack.
graph-wipe:
    @bash scripts/graph-compose.sh down -v

# Read every entity of the player force into .factorio/graph/<snapshot> (read-only on the game).
graph-export *args:
    #!/usr/bin/env bash
    set -euo pipefail
    exec env FACTORIO_RCON_PASSWORD="$(cat .factorio/password)" go run . --addr "127.0.0.1:$(cat .factorio/rcon-port 2>/dev/null || echo 27015)" graph export {{args}}

# Load the latest exported snapshot (or --in DIR) into Memgraph, replacing its surfaces.
graph-load *args:
    go run . graph load {{args}}

# Export, then load: one full refresh of the factory graph.
graph-sync *args:
    #!/usr/bin/env bash
    set -euo pipefail
    exec env FACTORIO_RCON_PASSWORD="$(cat .factorio/password)" go run . --addr "127.0.0.1:$(cat .factorio/rcon-port 2>/dev/null || echo 27015)" graph sync {{args}}

# Reapply the Memgraph constraints and indexes from memgraph/schema.cypherl.
graph-schema:
    go run . graph schema

# Open an interactive Cypher shell (mgconsole) inside the Memgraph container.
graph-shell:
    @bash scripts/graph-compose.sh exec memgraph mgconsole
