#!/usr/bin/env bash
# Enrich spoon-export-stablyai-orca.json with a linear_history field per fork.
# Definition: linear upstream sync pattern = the fork's ahead-by commits form
# a single linear chain with no merge commits (every ahead commit has <= 1 parent).
# Short-circuits ahead=0 forks to true (no ahead commits => trivially linear).

set -euo pipefail
EXP="${1:?usage: enrich-linear.sh <export.json> <out.json>}"
OUT="${2:?usage: enrich-linear.sh <export.json> <out.json>}"

# Read ahead-0 forks as fast-path: linear=true
# Read ahead>0 forks and probe each with compare API in serial.
echo "Reading export..."
jq -r '.forks[] | select(.enriched == true and .divergence != null and (.divergence.ahead // 0) > 0) | .full_name' "$EXP" > /tmp/enrich-probe.txt
TOTAL=$(wc -l < /tmp/enrich-probe.txt)
echo "Forks to probe: $TOTAL"

# Probe each, write a TSV of full_name<TAB>linear_bool
> /tmp/enrich-result.tsv
i=0
while IFS= read -r fork; do
  i=$((i+1))
  owner="${fork%/*}"
  repo="${fork#*/}"
  # get default branch
  branch=$(gh api "repos/${owner}/${repo}" --jq '.default_branch' 2>/dev/null || echo "")
  if [[ -z "$branch" ]]; then
    printf '%s\t%s\n' "$fork" "unknown" >> /tmp/enrich-result.tsv
    continue
  fi
  # compare default-branch vs upstream main; check ahead commits for merge parents
  out=$(gh api "repos/stablyai/orca/compare/main...${owner}:${repo}:${branch}" --jq '
    if .status == "behind" or .status == "identical" then
      "true"
    else
      (.commits // []) as $cs
      | if ($cs | length) == 0 then
          "true"
        else
          ($cs | map(.parents | length > 1) | any | not) | tostring
        end
    end
  ' 2>/dev/null || echo "unknown")
  printf '%s\t%s\n' "$fork" "$out" >> /tmp/enrich-result.tsv
  if (( i % 20 == 0 )); then
    echo "[$i/$TOTAL] last: $fork -> $out"
  fi
done < /tmp/enrich-probe.txt
echo "Done probing."
