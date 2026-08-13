#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
  printf 'usage: %s <design-system-path>\n' "$0" >&2
  exit 2
fi

source_checkout=$(realpath "$1")
if ! checkout_root=$(git -C "$source_checkout" rev-parse --show-toplevel 2>/dev/null); then
  printf 'not a git checkout: %s\n' "$source_checkout" >&2
  exit 1
fi
checkout_root=$(realpath "$checkout_root")
if [[ $source_checkout != "$checkout_root" ]]; then
  printf 'design-system path must be checkout root: %s\n' "$checkout_root" >&2
  exit 1
fi

source_tokens=src/lib/tokens/resolved
target_tokens=internal/tui/theme/tokens
provenance=$target_tokens/UPSTREAM.txt
files=(dark.tokens.json light.tokens.json amber.tokens.json)

declare -A recorded
while IFS=': ' read -r key value; do
  [[ -n ${key:-} ]] && recorded[$key]=$value
done <"$provenance"

if [[ ${recorded[source_checkout]:-} != "$checkout_root" ]]; then
  printf 'provenance source checkout mismatch: %s\n' "${recorded[source_checkout]:-(missing)}" >&2
  exit 1
fi
if [[ -z ${recorded[upstream_commit]:-} ]]; then
  printf 'provenance upstream commit missing\n' >&2
  exit 1
fi

current_commit=$(git -C "$checkout_root" rev-parse HEAD)
if [[ ${recorded[upstream_commit]} != "$current_commit" ]]; then
  printf 'provenance commit mismatch: recorded %s, supplied checkout HEAD %s\n' "${recorded[upstream_commit]}" "$current_commit" >&2
  exit 1
fi
if ! git -C "$checkout_root" cat-file -e "${recorded[upstream_commit]}^{commit}" 2>/dev/null; then
  printf 'provenance commit unavailable in supplied checkout: %s\n' "${recorded[upstream_commit]}" >&2
  exit 1
fi

for file in "${files[@]}"; do
  expected=${recorded[$file]:-}
  if [[ ! $expected =~ ^[0-9a-f]{64}$ ]]; then
    printf 'provenance hash missing or invalid: %s\n' "$file" >&2
    exit 1
  fi
  if ! git -C "$checkout_root" cat-file -e "${recorded[upstream_commit]}:$source_tokens/$file" 2>/dev/null; then
    printf 'missing resolved token blob at recorded commit: %s\n' "$file" >&2
    exit 1
  fi
  if ! git -C "$checkout_root" show "${recorded[upstream_commit]}:$source_tokens/$file" | sha256sum -c <(printf '%s  -\n' "$expected") >/dev/null; then
    printf 'recorded upstream token hash mismatch: %s\n' "$file" >&2
    exit 1
  fi
  if ! sha256sum -c <(printf '%s  %s\n' "$expected" "$target_tokens/$file") >/dev/null; then
    printf 'vendored token hash mismatch: %s\n' "$file" >&2
    exit 1
  fi
done

(
  cd internal/tui/theme
  go run gen.go -check
)
