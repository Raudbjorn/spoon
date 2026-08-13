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
if ! upstream_commit=$(git -C "$checkout_root" rev-parse HEAD 2>/dev/null); then
  printf 'cannot resolve design-system HEAD: %s\n' "$checkout_root" >&2
  exit 1
fi

source_tokens=src/lib/tokens/resolved
target_tokens=internal/tui/theme/tokens
files=(dark.tokens.json light.tokens.json amber.tokens.json)

for file in "${files[@]}"; do
  if ! git -C "$checkout_root" cat-file -e "$upstream_commit:$source_tokens/$file" 2>/dev/null; then
    printf 'missing resolved token blob at %s: %s\n' "$upstream_commit" "$source_tokens/$file" >&2
    exit 1
  fi
done

mkdir -p "$target_tokens"
for file in "${files[@]}"; do
  git -C "$checkout_root" show "$upstream_commit:$source_tokens/$file" >"$target_tokens/$file"
done

{
  printf 'source_checkout: %s\n' "$checkout_root"
  printf 'upstream_commit: %s\n' "$upstream_commit"
  printf 'sync_date: %s\n' "$(date -I)"
  for file in "${files[@]}"; do
    read -r hash _ < <(sha256sum "$target_tokens/$file")
    printf '%s: %s\n' "$file" "$hash"
  done
} >"$target_tokens/UPSTREAM.txt"

go generate ./internal/tui/theme
