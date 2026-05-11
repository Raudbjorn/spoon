"""Curation helper: surface candidate PR pairs from features.json.

Run: .venv/bin/python curate_helper.py

Writes (all gitignored):
- candidate_browser.md — all 200 PRs in a quick-scan table
- candidates_same.md   — pairs ranked by title-keyword overlap + file Jaccard
- candidates_different.md — sampled pairs with zero file overlap

These are starting points for hand-curation of judgments.json, NOT ground
truth. Verify each pair by reading the PRs on GitHub before labeling.
"""
import json
import re
from collections import Counter
from itertools import combinations
from pathlib import Path

STOPWORDS = {
    "a", "add", "added", "an", "and", "are", "as", "at", "be", "by",
    "fix", "fixed", "fixes", "for", "from", "in", "of", "on", "or",
    "the", "to", "update", "updated", "updates", "use", "with",
    "feat", "test", "tests", "chore", "docs", "ci", "build", "refactor",
}

WORD_RE = re.compile(r"[A-Za-z][A-Za-z0-9_-]+")


def load_records(path: str) -> list[dict]:
    with open(path) as f:
        return json.load(f)


def first_line(s: str) -> str:
    if not s:
        return ""
    return s.split("\n", 1)[0].strip()


def title_of(rec: dict) -> str:
    return first_line(rec.get("features", {}).get("Commits", ""))


def paths_of(rec: dict) -> set[str]:
    raw = rec.get("features", {}).get("Paths", "")
    return {p.strip() for p in raw.split("\n") if p.strip()}


def title_tokens(title: str) -> set[str]:
    return {
        w.lower() for w in WORD_RE.findall(title) if w.lower() not in STOPWORDS
    }


def jaccard(a: set, b: set) -> float:
    if not a and not b:
        return 0.0
    return len(a & b) / len(a | b)


def emit_browser(records: list[dict], out: Path) -> None:
    """Quick-scan table of all PRs."""
    lines = ["# PR Browser\n", "| id | title | files | first paths | url |", "| --- | --- | --- | --- | --- |"]
    for r in records:
        paths = sorted(paths_of(r))
        first5 = ", ".join(p.split("/")[-1] for p in paths[:5])
        lines.append(
            f"| `{r['id']}` | {title_of(r)[:80]} | {len(paths)} | {first5[:80]} | {r['url']} |"
        )
    out.write_text("\n".join(lines) + "\n")


def emit_same_candidates(records: list[dict], out: Path, top: int = 100) -> None:
    """Rank pairs by a combined title-token + path Jaccard score."""
    scored = []
    for a, b in combinations(records, 2):
        ta = title_tokens(title_of(a))
        tb = title_tokens(title_of(b))
        title_j = jaccard(ta, tb)
        path_j = jaccard(paths_of(a), paths_of(b))
        if title_j == 0 and path_j == 0:
            continue
        score = 0.6 * title_j + 0.4 * path_j
        if score < 0.10:
            continue
        scored.append((score, title_j, path_j, a, b))
    scored.sort(key=lambda t: (-t[0], -t[1], -t[2], t[3]["id"], t[4]["id"]))

    lines = [
        "# Same-Intent Candidates\n",
        "Ranked by `0.6 * title-token-jaccard + 0.4 * file-path-jaccard`. ",
        "Score > 0.5 is strong, > 0.3 plausible, lower needs human review.\n",
        "| score | t_j | p_j | a | b | titles |",
        "| --- | --- | --- | --- | --- | --- |",
    ]
    for s, tj, pj, a, b in scored[:top]:
        lines.append(
            f"| {s:.2f} | {tj:.2f} | {pj:.2f} | `{a['id']}` | `{b['id']}` | "
            f"{title_of(a)[:50]} ⇄ {title_of(b)[:50]} |"
        )
    out.write_text("\n".join(lines) + "\n")


def emit_different_candidates(
    records: list[dict], out: Path, top: int = 60, max_per_pr: int = 2
) -> None:
    """Sample pairs with zero file overlap and distinct title themes.

    Buckets PRs by their first sorted title token, then walks token pairs,
    each time picking the LEAST-USED PR from each bucket. Caps per-PR
    appearances at `max_per_pr` so no single PR dominates the candidate list
    (previous bug: pr-4699 was the rep for "a2a" and got paired with every
    other bucket's first rep, accounting for half the diff-candidate rows).
    """
    by_top_token: dict[str, list[dict]] = {}
    for r in records:
        toks = sorted(title_tokens(title_of(r)))
        if not toks:
            continue
        by_top_token.setdefault(toks[0], []).append(r)
    tokens = sorted(by_top_token.keys())

    lines = [
        "# Different-Intent Candidates\n",
        "Sampled pairs with no shared title tokens and zero file overlap. ",
        f"Each PR appears in at most {max_per_pr} rows. "
        "These should be 'obviously different' starting points.\n",
        "| a | b | titles |",
        "| --- | --- | --- |",
    ]
    seen_pairs: set[tuple[str, str]] = set()
    pr_count: dict[str, int] = {}
    out_pairs: list[tuple[dict, dict]] = []

    def least_used(bucket: list[dict]) -> dict | None:
        # Return the bucket entry with the lowest pr_count; None if all are
        # already at the cap.
        ranked = sorted(bucket, key=lambda r: pr_count.get(r["id"], 0))
        for r in ranked:
            if pr_count.get(r["id"], 0) < max_per_pr:
                return r
        return None

    for ta, tb in combinations(tokens, 2):
        ra = least_used(by_top_token[ta])
        rb = least_used(by_top_token[tb])
        if ra is None or rb is None:
            continue
        if ra["id"] == rb["id"]:
            continue
        if jaccard(paths_of(ra), paths_of(rb)) > 0:
            continue
        key = tuple(sorted([ra["id"], rb["id"]]))
        if key in seen_pairs:
            continue
        seen_pairs.add(key)
        pr_count[ra["id"]] = pr_count.get(ra["id"], 0) + 1
        pr_count[rb["id"]] = pr_count.get(rb["id"], 0) + 1
        out_pairs.append((ra, rb))
        if len(out_pairs) >= top:
            break

    for a, b in out_pairs:
        lines.append(
            f"| `{a['id']}` | `{b['id']}` | "
            f"{title_of(a)[:50]} ⇄ {title_of(b)[:50]} |"
        )
    out.write_text("\n".join(lines) + "\n")


def emit_summary(records: list[dict]) -> None:
    titles = [title_of(r) for r in records]
    file_counts = [len(paths_of(r)) for r in records]
    print(f"loaded {len(records)} PRs")
    print(f"file counts: min={min(file_counts)} max={max(file_counts)} "
          f"mean={sum(file_counts)/len(file_counts):.1f}")
    print("top 10 most common first-words in titles:")
    first_word = Counter()
    for t in titles:
        toks = title_tokens(t)
        if toks:
            first_word[sorted(toks)[0]] += 1
    for w, n in first_word.most_common(10):
        print(f"  {n:3d}  {w}")


def main() -> None:
    here = Path(__file__).parent
    records = load_records(str(here / "features.json"))
    emit_browser(records, here / "candidate_browser.md")
    emit_same_candidates(records, here / "candidates_same.md")
    emit_different_candidates(records, here / "candidates_different.md")
    emit_summary(records)
    print("wrote: candidate_browser.md candidates_same.md candidates_different.md")


if __name__ == "__main__":
    main()
