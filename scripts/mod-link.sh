#!/usr/bin/env bash
# Point every mod directory at this checkout under the unversioned name.
#
# Factorio accepts a directory named "<name>" or "<name>_<version>". The
# unversioned form is used here so a version bump can never invalidate the
# symlink again; any "<name>_<old version>" symlink into this repository is
# removed, because two copies of one mod make Factorio refuse to load.
#
# Only symlinks that point into this repository are touched: a real installation
# or a link to somebody else's checkout is reported and left alone, and a second
# visible copy of the same mod is called out.
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
mods_dir="${1:-${FACTORIO_MOD_DIR:-$HOME/Library/Application Support/factorio/mods}}"
mkdir -p "$mods_dir"

shopt -s nullglob
changed=0
for source in "$repo_root"/mods/*/; do
    source="${source%/}"
    info="$source/info.json"
    [[ -f "$info" ]] || continue
    name="$(sed -n 's/.*"name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$info" | head -1)"
    version="$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$info" | head -1)"
    if [[ -z "$name" || -z "$version" ]]; then
        echo "mod-link: cannot read name/version from $info" >&2
        exit 1
    fi
    desired="$mods_dir/${name}"
    for stale in "$mods_dir/${name}" "$mods_dir/${name}"_*; do
        [[ -e "$stale" || -L "$stale" ]] || continue
        [[ "$stale" == "$desired" ]] && continue
        if [[ ! -L "$stale" ]]; then
            if [[ -d "$stale" ]]; then
                echo "mod-link: WARNING $stale is a directory, not a symlink; ${name} is then visible twice in $mods_dir and Factorio will refuse to load it" >&2
            fi
            continue
        fi
        linked="$(readlink "$stale")"
        if [[ "$linked" == "$source" || "$linked" == "$source/" ]]; then
            rm "$stale"
            echo "mod-link: removed stale $stale"
            changed=1
        else
            echo "mod-link: WARNING $stale -> $linked is another copy of ${name}; only ${desired} should exist in $mods_dir" >&2
        fi
    done
    if [[ -L "$desired" ]]; then
        if [[ "$(readlink "$desired")" != "$source" ]]; then
            ln -sfn "$source" "$desired"
            echo "mod-link: repointed $desired"
            changed=1
        fi
    elif [[ -e "$desired" ]]; then
        echo "mod-link: keeping existing $desired (not a symlink)" >&2
    else
        ln -s "$source" "$desired"
        echo "mod-link: linked $desired"
        changed=1
    fi
done
if [[ "$changed" == 0 ]]; then
    echo "mod-link: $mods_dir already matches info.json"
fi
