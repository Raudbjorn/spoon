#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
  printf 'usage: %s <design-system-path>\n' "$0" >&2
  exit 2
fi

source_checkout=$1
source_tokens=$source_checkout/src/lib/tokens/resolved
target_tokens=internal/tui/theme/tokens
files=(dark.tokens.json light.tokens.json amber.tokens.json)

for file in "${files[@]}"; do
  if [[ ! -f $source_tokens/$file ]]; then
    printf 'missing resolved token file: %s\n' "$source_tokens/$file" >&2
    exit 1
  fi
done

if ! upstream_commit=$(git -C "$source_checkout" rev-parse HEAD 2>/dev/null); then
  printf 'not a git checkout: %s\n' "$source_checkout" >&2
  exit 1
fi

mkdir -p "$target_tokens"
for file in "${files[@]}"; do
  cp "$source_tokens/$file" "$target_tokens/$file"
done

{
  printf 'source_checkout: %s\n' "$source_checkout"
  printf 'upstream_commit: %s\n' "$upstream_commit"
  printf 'sync_date: %s\n' "$(date -I)"
  for file in "${files[@]}"; do
    printf '%s: %s\n' "$file" "$(sha256sum "$target_tokens/$file" | cut -d' ' -f1)"
  done
} >"$target_tokens/UPSTREAM.txt"

go generate ./internal/tui/theme
