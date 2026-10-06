#!/usr/bin/env bash
# Classify each enriched fork in a spoon export as linear / not linear.
# Definition: linear upstream sync pattern = the fork's ahead-by commits form
# a single linear chain with no merge commits (every ahead commit has <= 1 parent).
# Short-circuits ahead=0 forks to true (no ahead commits => trivially linear).
#
# Output is a TSV of full_name<TAB>classification, one row per probed fork.
# This is a probe, not an enrichment pass: it never rewrites the export JSON.

set -euo pipefail
usage="usage: enrich-linear.sh <export.json> <out.tsv> [parent]"
EXP="${1:?$usage}"
OUT="${2:?$usage}"
# The upstream to compare against. The export names it; the third argument
# overrides it. jq's -r on the object prints its full_name.
PARENT="${3:-$(jq -r '.parent | if type == "object" then .full_name else . end' "$EXP")}"

# An unpaginated compare embeds at most 250 commits, so a fork with more ahead
# commits than that has an incomplete .commits array and an omitted merge would
# read as linear. Those are reported as unknown rather than guessed.
TRUNCATED_UNLESS_TOTAL_MATCHES='
    (.total_commits // (.commits | length)) as $total
    | (.commits // []) as $cs
    | if ($total > ($cs | length)) then "unknown"
      elif .status == "behind" or .status == "identical" then "true"
      elif ($cs | length) == 0 then "true"
      else (($cs | map(.parents | length > 1) | any | not) | tostring)
      end'

# Read ahead-0 forks as fast-path: linear=true
# Read ahead>0 forks and probe each with compare API in serial.
echo "Reading export..."
mapfile -t FORKS < <(jq -r '.forks[] | select(.enriched == true and .divergence != null and (.divergence.ahead // 0) > 0) | .full_name' "$EXP")
TOTAL=${#FORKS[@]}
echo "Forks to probe: $TOTAL (against $PARENT)"

# Probe each, write a TSV of full_name<TAB>linear_bool
: > "$OUT"
i=0
for fork in "${FORKS[@]}"; do
  i=$((i+1))
  owner="${fork%/*}"
  repo="${fork#*/}"
  # get default branch
  branch=$(gh api "repos/${owner}/${repo}" --jq '.default_branch' 2>/dev/null || echo "")
  if [[ -z "$branch" ]]; then
    printf '%s\t%s\n' "$fork" "unknown" >> "$OUT"
    continue
  fi
  # compare default-branch vs upstream main; check ahead commits for merge parents
  out=$(gh api "repos/${PARENT}/compare/main...${owner}:${repo}:${branch}" --jq "$TRUNCATED_UNLESS_TOTAL_MATCHES" 2>/dev/null || echo "unknown")
  printf '%s\t%s\n' "$fork" "$out" >> "$OUT"
  if (( i % 20 == 0 )); then
    echo "[$i/$TOTAL] last: $fork -> $out"
  fi
done
echo "Done probing. Wrote $OUT"
