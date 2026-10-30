#!/usr/bin/env bash
set -euo pipefail

state=${FACTORIO_STATE_DIR:-.factorio}
action=${1:-}
shift || true

fail() { printf 'error: %s\n' "$*" >&2; exit 1; }

# Refuse to control a reused PID or a server not started by this workflow.
managed_pid() {
    [[ -f "$state/pid" && -f "$state/save" && -f "$state/binary" && -f "$state/rcon-port" ]] || return 1
    local pid cmd save binary port
    pid=$(cat "$state/pid")
    [[ "$pid" =~ ^[0-9]+$ ]] || return 1
    cmd=$(ps -p "$pid" -o command= 2>/dev/null) || return 1
    save=$(cat "$state/save")
    binary=$(cat "$state/binary")
    port=$(cat "$state/rcon-port")
    [[ "$cmd" == *"$binary"* && "$cmd" == *"--start-server $save"* && "$cmd" == *"--rcon-bind 127.0.0.1:$port"* ]] || return 1
    printf '%s\n' "$pid"
}

run_cli() {
    managed_pid >/dev/null || fail 'managed server is not running; run just start'
    [[ -s "$state/password" ]] || fail 'RCON password file missing'
    local port
    port=$(cat "$state/rcon-port")
    FACTORIO_RCON_PASSWORD=$(cat "$state/password") go run . --addr "127.0.0.1:$port" "$@"
}

case "$action" in
start|stop)
    umask 077
    mkdir -p "$state"
    mkdir "$state/lock" 2>/dev/null || fail "another lifecycle operation is running (or remove stale $state/lock)"
    trap 'rmdir "$state/lock"' EXIT
    if [[ "$action" == start ]]; then
        if managed_pid >/dev/null; then fail 'managed server is already running'; fi
        if [[ -f "$state/pid" ]] && kill -0 "$(cat "$state/pid")" 2>/dev/null; then
            fail 'stored PID belongs to an unrecognized process; refusing to start'
        fi
        binary=${FACTORIO_BIN:-/Applications/factorio.app/Contents/MacOS/factorio}
        save=${FACTORIO_SAVE:-$HOME/Library/Application Support/factorio/saves/ai-player.zip}
        port=${FACTORIO_RCON_PORT:-27015}
        game_port=${FACTORIO_GAME_PORT:-34197}
        [[ -x "$binary" ]] || fail "Factorio executable not found: $binary"
        [[ -f "$save" && "$save" == /* ]] || fail "save must be an existing absolute path: $save"
        [[ "$binary" == /* ]] || fail 'Factorio executable must be an absolute path'
        [[ "$port" =~ ^[0-9]+$ && "$game_port" =~ ^[0-9]+$ ]] || fail 'ports must be numbers'
        if [[ ! -s "$state/password" ]]; then openssl rand -hex 24 > "$state/password"; fi
        printf '%s\n' "$binary" > "$state/binary"
        printf '%s\n' "$save" > "$state/save"
        printf '%s\n' "$port" > "$state/rcon-port"
        : > "$state/server.log"
        set -- --start-server "$save" --bind "127.0.0.1:$game_port" --rcon-bind "127.0.0.1:$port" --rcon-password "$(cat "$state/password")" --server-settings "$PWD/server-settings.json"
        if [[ -n "${FACTORIO_MOD_DIR:-}" ]]; then set -- --mod-directory "$FACTORIO_MOD_DIR" "$@"; fi
        nohup "$binary" "$@" >> "$state/server.log" 2>&1 < /dev/null &
        pid=$!
        printf '%s\n' "$pid" > "$state/pid"
        for ((i=0; i<120; i++)); do
            if grep -q 'Starting RCON interface' "$state/server.log"; then
                managed_pid >/dev/null || fail 'server exited during startup; inspect just logs'
                printf 'Factorio server started (PID %s, save %s, game 127.0.0.1:%s, RCON 127.0.0.1:%s).\n' "$pid" "$save" "$game_port" "$port"
                exit 0
            fi
            if ! kill -0 "$pid" 2>/dev/null; then fail 'server failed to start; inspect just logs'; fi
            sleep 1
        done
        fail 'server startup timed out; inspect just logs'
    fi
    pid=$(managed_pid) || fail 'managed server is not running or PID does not match'
    run_cli mutation --format json --query 'mutation { shutdown { response } }'
    for ((i=0; i<120; i++)); do
        if ! managed_pid >/dev/null; then
            if ! grep -q 'Goodbye' "$state/server.log" || ! grep -q 'Saving map as ' "$state/server.log" || ! grep -q 'Saving progress: 100.000000%' "$state/server.log"; then
                fail 'server exited without confirmed save and clean shutdown; inspect just logs'
            fi
            rm -f "$state/pid"
            printf 'Factorio server saved and stopped.\n'
            exit 0
        fi
        sleep 1
    done
    fail 'server did not stop within 120 seconds; inspect just logs'
    ;;
save)
    run_cli mutation --format json --query 'mutation { save { response } }'
    ;;
logs)
    [[ -f "$state/server.log" ]] || fail 'no server log; run just start'
    cat "$state/server.log"
    ;;
logs-follow)
    [[ -f "$state/server.log" ]] || fail 'no server log; run just start'
    tail -f "$state/server.log"
    ;;
*) fail 'usage: server.sh {start|stop|save|logs|logs-follow}' ;;
esac
