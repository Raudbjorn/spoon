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
# Resolve the parent's default branch rather than assuming `main`: a literal
# `main` against a parent that never had one 404s for every fork, and the
# suppressed error would land in the TSV as unknown across the board.
PARENT_BRANCH=$(gh api "repos/${PARENT}" --jq '.default_branch' 2>/dev/null || echo "")
if [[ -z "$PARENT_BRANCH" ]]; then
  echo "cannot resolve the default branch of $PARENT" >&2
  exit 1
fi

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
# ahead=0 forks are trivially linear and need no probe, but they still get a row:
# omitting them would make the TSV an incomplete classification that cannot tell
# a known-linear fork from an unprocessed one.
while IFS= read -r f; do
  printf '%s\t%s\n' "$f" "true"
done < <(jq -r '.forks[] | select(.enriched == true and .divergence != null and (.divergence.ahead // 0) == 0) | .full_name' "$EXP") > "$OUT"
mapfile -t FORKS < <(jq -r '.forks[] | select(.enriched == true and .divergence != null and (.divergence.ahead // 0) > 0) | .full_name' "$EXP")
TOTAL=${#FORKS[@]}
echo "Forks to probe: $TOTAL (against $PARENT@$PARENT_BRANCH), plus trivially-linear ahead=0 forks"

# Probe each, write a TSV of full_name<TAB>linear_bool
i=0
for fork in "${FORKS[@]}"; do
  i=$((i+1))
  owner="${fork%/*}"
  repo="${fork#*/}"
  # The exported divergence describes active_branch when the fork's work sits on
  # a non-default branch; comparing its default branch instead would classify an
  # unrelated, usually behind, branch. Fall back to the default only when absent.
  branch=$(jq -r --arg f "$fork" '.forks[] | select(.full_name == $f) | .divergence.active_branch // empty' "$EXP")
  if [[ -z "$branch" ]]; then
    branch=$(gh api "repos/${owner}/${repo}" --jq '.default_branch' 2>/dev/null || echo "")
  fi
  if [[ -z "$branch" ]]; then
    printf '%s\t%s\n' "$fork" "unknown" >> "$OUT"
    continue
  fi
  # compare default-branch vs upstream main; check ahead commits for merge parents
  # Cross-repository head ref in GitHub's documented USERNAME:BRANCH form.
  # gh api exits non-zero on an HTTP error but prints the error body to stdout,
  # so a bare `|| echo unknown` would splice the JSON error into the TSV row.
  # Only an exact true/false from the jq program is trusted; anything else is
  # unknown.
  if ! out=$(gh api "repos/${PARENT}/compare/${PARENT_BRANCH}...${owner}:${branch}" --jq "$TRUNCATED_UNLESS_TOTAL_MATCHES" 2>/dev/null); then
    out=unknown
  fi
  case "$out" in
    true | false | unknown) ;;
    *) out=unknown ;;
  esac
  printf '%s\t%s\n' "$fork" "$out" >> "$OUT"
  if (( i % 20 == 0 )); then
    echo "[$i/$TOTAL] last: $fork -> $out"
  fi
done
echo "Done probing. Wrote $OUT"
