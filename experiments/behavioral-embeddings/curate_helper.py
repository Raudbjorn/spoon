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


def emit_same_candidates(records: list[dict], out: Path, top: int = 40) -> None:
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
        if score < 0.15:
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


def emit_different_candidates(records: list[dict], out: Path, top: int = 40) -> None:
    """Sample pairs with zero file overlap and distinct title themes.

    Picks one pair from each combination of (PR with most-frequent
    top-token-A) × (PR with most-frequent top-token-B). This biases toward
    pairs whose first-glance intent words are clearly different.
    """
    by_top_token: dict[str, list[dict]] = {}
    for r in records:
        tok = next(iter(sorted(title_tokens(title_of(r)))), None)
        if not tok:
            continue
        by_top_token.setdefault(tok, []).append(r)
    # Keep tokens with at least one representative; sort by token-rarity
    # so we pick from the long tail (more distinct themes).
    tokens = sorted(by_top_token.keys())

    lines = [
        "# Different-Intent Candidates\n",
        "Sampled pairs with no shared title tokens and zero file overlap. ",
        "These should be 'obviously different' starting points.\n",
        "| a | b | titles |",
        "| --- | --- | --- |",
    ]
    seen_pairs: set[tuple[str, str]] = set()
    out_lines: list[tuple[dict, dict]] = []
    for ta, tb in combinations(tokens, 2):
        ra = by_top_token[ta][0]
        rb = by_top_token[tb][0]
        if ra["id"] == rb["id"]:
            continue
        if jaccard(paths_of(ra), paths_of(rb)) > 0:
            continue
        key = tuple(sorted([ra["id"], rb["id"]]))
        if key in seen_pairs:
            continue
        seen_pairs.add(key)
        out_lines.append((ra, rb))
        if len(out_lines) >= top:
            break
    for a, b in out_lines:
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
