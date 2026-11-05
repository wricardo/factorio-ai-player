#!/usr/bin/env bash
# Verify the repository world-creation settings keep a new save free of enemies.
#
# Enemy bases are disabled through autoplace (`enemy-base` frequency/size/
# richness 0), which is what actually prevents spawners from existing;
# peaceful_mode only stops attacks. Evolution and expansion are additionally
# disabled in the shared map settings. Run this before creating a world so an
# accidental edit cannot silently reintroduce biters. Only read: creation-time
# settings are never applied to an existing save.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
fail=0

check() {
    local file=$1
    [[ -f "$root/$file" ]] || { printf 'error: %s is missing\n' "$file" >&2; fail=1; return; }
    if ! grep -q '"enemy-base": {"frequency": 0, "size": 0, "richness": 0}' "$root/$file"; then
        printf 'error: %s must set "enemy-base": {"frequency": 0, "size": 0, "richness": 0}\n' "$file" >&2
        fail=1
    fi
    if ! grep -q '"peaceful_mode": true' "$root/$file"; then
        printf 'error: %s must set "peaceful_mode": true\n' "$file" >&2
        fail=1
    fi
}

check map-gen-settings-regular.json
check map-gen-settings-sandbox.json

# Both enemy blocks are objects whose first member is "enabled": false.
for block in enemy_evolution enemy_expansion; do
    if ! awk -v block="\"$block\":" '
        $0 ~ block { found = 1; next }
        found && /"enabled"/ { print; exit }
    ' "$root/map-settings.json" | grep -q '"enabled": false'; then
        printf 'error: map-settings.json must set %s "enabled": false\n' "$block" >&2
        fail=1
    fi
done

[[ $fail -eq 0 ]] || exit 1
printf 'world settings disable enemies (no enemy-base autoplace, peaceful, evolution and expansion off)\n'
