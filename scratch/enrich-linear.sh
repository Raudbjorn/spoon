#!/usr/bin/env bash
# Probe a Spoon export and write full_name<TAB>true|false|unknown.
# Uses Spoon's saved OAuth credentials or GH_TOKEN/GITHUB_TOKEN; requires Go.
set -euo pipefail
usage="usage: enrich-linear.sh <export.json> <out.tsv> [parent]"
input="${1:?$usage}"
output="${2:?$usage}"
[[ "$input" = /* ]] || input="$PWD/$input"
[[ "$output" = /* ]] || output="$PWD/$output"
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
exec go -C "$script_dir/.." run ./scratch/enrich-linear "$input" "$output" "${@:3}"
