#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
  printf 'usage: %s <design-system-path>\n' "$0" >&2
  exit 2
fi

source_tokens=$1/src/lib/tokens/resolved
target_tokens=internal/tui/theme/tokens
files=(dark.tokens.json light.tokens.json amber.tokens.json)

for file in "${files[@]}"; do
  if ! cmp -s "$source_tokens/$file" "$target_tokens/$file"; then
    printf 'vendored token drift: %s\n' "$file" >&2
    exit 1
  fi
done

(
  cd internal/tui/theme
  go run gen.go -check
)
