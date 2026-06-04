#!/bin/bash
# Build goclaw plugins into a staging directory ready to copy into goclaw.
#
# Adapted from godoorkit's scripts/build-door.sh. The difference: a goclaw plugin is
# a binary PLUS a declarative plugin.yml the host reads before launching, so we stage
# both together in the per-plugin layout the host walks:
#
#   build/<name>/
#     <exec>        the built binary (named per the plugin.yml `exec:` field)
#     plugin.yml    copied verbatim from cmd/<name>/plugin.yml
#
# This script does NOT copy anything into goclaw; copy build/<name>/ into your
# goclaw plugins directory yourself.
#
# Usage:
#   scripts/build-plugin.sh [name ...]
#
#   With one or more names, build those plugins (e.g. `build-plugin.sh roll`).
#   With no name, build every plugin under cmd/ that has a plugin.yml.
#
# Env:
#   GOOS, GOARCH   cross-compile target (default: host platform)

set -euo pipefail

# Repo root = parent of this script's dir, so the script works from anywhere.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "$ROOT_DIR"

BUILD_DIR="${ROOT_DIR}/build"

# yaml_get FILE KEY -> prints the scalar value of a top-level `key: value` line,
# trimming quotes, inline comments, and surrounding whitespace. Good enough for the
# flat plugin.yml schema (no external yaml dependency).
yaml_get() {
    local file="$1" key="$2"
    sed -n "s/^${key}:[[:space:]]*//p" "$file" \
        | head -n1 \
        | sed -e 's/[[:space:]]*#.*$//' -e 's/^"//' -e 's/"$//' -e 's/[[:space:]]*$//'
}

# Discover plugins: a plugin is a cmd/<name>/ dir containing a plugin.yml.
discover_plugins() {
    local d
    for d in cmd/*/; do
        [ -f "${d}plugin.yml" ] && basename "$d"
    done
}

build_one() {
    local name="$1"
    local cmd_dir="cmd/${name}"
    local yml="${cmd_dir}/plugin.yml"

    if [ ! -d "$cmd_dir" ]; then
        echo "Error: no such plugin dir: ${cmd_dir}" >&2
        return 1
    fi
    if [ ! -f "$yml" ]; then
        echo "Error: ${cmd_dir} has no plugin.yml (not a registerable plugin)" >&2
        return 1
    fi

    # The binary name is the plugin.yml `exec:` field (defaults to the plugin name).
    local exec_name version yml_name yml_kind
    exec_name="$(yaml_get "$yml" exec)"
    exec_name="${exec_name:-$name}"
    version="$(yaml_get "$yml" version)"
    yml_name="$(yaml_get "$yml" name)"
    yml_kind="$(yaml_get "$yml" kind)"

    # Sanity: the manifest name should match the plugin dir (the host keys on name).
    if [ -n "$yml_name" ] && [ "$yml_name" != "$name" ]; then
        echo "Warning: ${yml} name '${yml_name}' != plugin dir '${name}'" >&2
    fi

    local out_dir="${BUILD_DIR}/${name}"
    mkdir -p "$out_dir"

    local target="host"
    [ -n "${GOOS:-}" ] || [ -n "${GOARCH:-}" ] && target="${GOOS:-host}/${GOARCH:-host}"

    echo "Building ${name} (${yml_kind:-tool} v${version:-?}) for ${target} -> build/${name}/${exec_name}"
    go build -trimpath -ldflags="-s -w" -o "${out_dir}/${exec_name}" "./${cmd_dir}"
    chmod +x "${out_dir}/${exec_name}" 2>/dev/null || true

    # Stage the manifest beside the binary, the per-plugin shape the host walks.
    cp "$yml" "${out_dir}/plugin.yml"

    echo "  staged: ${out_dir}/ (${exec_name} + plugin.yml)"
}

main() {
    local -a names=()
    if [ "$#" -gt 0 ]; then
        names=("$@")
    else
        # Bash 3.2 (macOS default) has no `mapfile`, so collect names in a loop.
        local p
        while IFS= read -r p; do
            [ -n "$p" ] && names+=("$p")
        done < <(discover_plugins)
        if [ "${#names[@]}" -eq 0 ]; then
            echo "No plugins found (no cmd/*/plugin.yml). Nothing to build." >&2
            exit 1
        fi
    fi

    echo "Staging plugins into build/ (copy build/<name>/ into goclaw yourself)"
    echo
    local n
    for n in "${names[@]}"; do
        build_one "$n"
    done
    echo
    echo "Done. Staged: ${names[*]}"
}

main "$@"
