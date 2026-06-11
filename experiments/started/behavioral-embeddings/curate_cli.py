"""Interactive curation CLI for judgments.json.

Walks the candidate lists in `candidates_same.md` (first) then
`candidates_different.md`, opens each pair's PRs in browser tabs, and
prompts for a same/different/skip/quit decision plus a one-line rationale.
Already-judged and explicitly-skipped pairs are filtered out automatically.

Run: .venv/bin/python curate_cli.py

State files:
- judgments.json    — written on every accepted decision (gitignored: NO,
                      this is the deliverable).
- .curate_skipped   — list of "skip" pairs, one per line (gitignored).
"""
import json
import re
import sys
import webbrowser
from pathlib import Path

HERE = Path(__file__).parent
FEATURES_PATH = HERE / "features.json"
JUDGMENTS_PATH = HERE / "judgments.json"
SKIPPED_PATH = HERE / ".curate_skipped"
SAME_PATH = HERE / "candidates_same.md"
DIFF_PATH = HERE / "candidates_different.md"

UPSTREAM_OWNER = "IBM"
UPSTREAM_REPO = "mcp-context-forge"

TARGET_PER_LABEL = 20

# Matches `pr-NNNN` inside backticks on a markdown table row. The first two
# such captures on a row are taken as (a, b).
PR_RE = re.compile(r"`(pr-\d+)`")


def load_features_titles() -> dict[str, str]:
    if not FEATURES_PATH.exists():
        die(f"missing {FEATURES_PATH}; run cmd/dump_features first")
    data = json.loads(FEATURES_PATH.read_text())
    out = {}
    for r in data:
        commits = r.get("features", {}).get("Commits", "")
        title = commits.split("\n", 1)[0].strip() if commits else "(no title)"
        out[r["id"]] = title
    return out


def load_judgments() -> list[dict]:
    if JUDGMENTS_PATH.exists():
        return json.loads(JUDGMENTS_PATH.read_text())
    return []


def save_judgments(j: list[dict]) -> None:
    JUDGMENTS_PATH.write_text(json.dumps(j, indent=2) + "\n")


def load_skipped() -> set[tuple[str, str]]:
    if not SKIPPED_PATH.exists():
        return set()
    out: set[tuple[str, str]] = set()
    for line in SKIPPED_PATH.read_text().splitlines():
        parts = line.strip().split()
        if len(parts) == 2:
            out.add(tuple(sorted(parts)))
    return out


def save_skipped(s: set[tuple[str, str]]) -> None:
    SKIPPED_PATH.write_text("\n".join(f"{a} {b}" for a, b in sorted(s)) + "\n")


def parse_candidates(path: Path) -> list[tuple[str, str]]:
    if not path.exists():
        return []
    pairs: list[tuple[str, str]] = []
    seen: set[tuple[str, str]] = set()
    for line in path.read_text().splitlines():
        ids = PR_RE.findall(line)
        if len(ids) < 2:
            continue
        key = tuple(sorted(ids[:2]))
        if key in seen:
            continue
        seen.add(key)
        pairs.append((ids[0], ids[1]))
    return pairs


def pair_key(a: str, b: str) -> tuple[str, str]:
    return tuple(sorted([a, b]))


def url_for(pr_id: str) -> str:
    n = pr_id.removeprefix("pr-")
    return f"https://github.com/{UPSTREAM_OWNER}/{UPSTREAM_REPO}/pull/{n}"


def die(msg: str) -> None:
    print(msg, file=sys.stderr)
    sys.exit(1)


def print_status(j: list[dict]) -> None:
    n_same = sum(1 for x in j if x["label"] == 1)
    n_diff = sum(1 for x in j if x["label"] == 0)
    bar_same = "█" * min(n_same, TARGET_PER_LABEL) + "·" * max(0, TARGET_PER_LABEL - n_same)
    bar_diff = "█" * min(n_diff, TARGET_PER_LABEL) + "·" * max(0, TARGET_PER_LABEL - n_diff)
    print()
    print(f"  same-intent  {n_same:2d}/{TARGET_PER_LABEL}  [{bar_same}]")
    print(f"  different    {n_diff:2d}/{TARGET_PER_LABEL}  [{bar_diff}]")


def prompt_choice(prompt: str, choices: str) -> str:
    valid = set(choices.lower())
    while True:
        try:
            ans = input(prompt).strip().lower()
        except EOFError:
            return "q"
        if ans in valid:
            return ans
        print(f"  pick one of {sorted(valid)}")


def check_quota(judgments: list[dict], pr_id: str, label: int) -> str | None:
    """Returns a warning string if the rule 'each PR in at most one same +
    one different pair' would be violated by adding pr_id at this label."""
    same_uses = sum(1 for j in judgments if j["label"] == 1 and pr_id in (j["a"], j["b"]))
    diff_uses = sum(1 for j in judgments if j["label"] == 0 and pr_id in (j["a"], j["b"]))
    if label == 1 and same_uses >= 1:
        return f"{pr_id} already in {same_uses} same-intent pair(s)"
    if label == 0 and diff_uses >= 1:
        return f"{pr_id} already in {diff_uses} different-intent pair(s)"
    return None


def main() -> None:
    titles = load_features_titles()
    judgments = load_judgments()
    skipped = load_skipped()
    same = parse_candidates(SAME_PATH)
    diff = parse_candidates(DIFF_PATH)

    if not same and not diff:
        die(f"no candidate files found; run curate_helper.py first")

    in_judg = {pair_key(j["a"], j["b"]) for j in judgments}

    def queue():
        for p in same:
            k = pair_key(*p)
            if k in in_judg or k in skipped:
                continue
            yield (*p, "same-candidate")
        for p in diff:
            k = pair_key(*p)
            if k in in_judg or k in skipped:
                continue
            yield (*p, "different-candidate")

    print_status(judgments)
    print("\n[y]es-same  [n]o-different  [s]kip-pair  [b]rowser-only  [q]uit")

    for a, b, source in queue():
        ta = titles.get(a, "(unknown PR — not in features.json)")
        tb = titles.get(b, "(unknown PR — not in features.json)")
        print()
        print(f"  ─── candidate ({source}) ───")
        print(f"  {a}: {ta}")
        print(f"  {b}: {tb}")
        print(f"  URLs: {url_for(a)}")
        print(f"        {url_for(b)}")

        try:
            webbrowser.open_new_tab(url_for(a))
            webbrowser.open_new_tab(url_for(b))
        except Exception as e:
            print(f"  (browser open failed: {e})")

        ans = prompt_choice("  Same intent? [y/n/s/b/q]: ", "ynsbq")

        if ans == "q":
            print("  quitting; saved state intact.")
            break
        if ans == "b":
            # User just wanted browser tabs; re-prompt without advancing the queue.
            # Simplest path: re-prompt y/n/s/q now.
            ans = prompt_choice("  (browser opened) Same intent? [y/n/s/q]: ", "ynsq")
            if ans == "q":
                break
        if ans == "s":
            skipped.add(pair_key(a, b))
            save_skipped(skipped)
            continue

        label = 1 if ans == "y" else 0

        # Quota warnings — non-fatal, user can still proceed.
        for pid in (a, b):
            w = check_quota(judgments, pid, label)
            if w:
                print(f"  ⚠️  {w}")

        rationale = input("  Rationale (one line): ").strip()
        if not rationale:
            rationale = "(see PRs)"

        judgments.append({"a": a, "b": b, "label": label, "rationale": rationale})
        save_judgments(judgments)
        in_judg.add(pair_key(a, b))
        print(f"  ✓ saved as label={label}")
        print_status(judgments)

        n_same = sum(1 for j in judgments if j["label"] == 1)
        n_diff = sum(1 for j in judgments if j["label"] == 0)
        if n_same >= TARGET_PER_LABEL and n_diff >= TARGET_PER_LABEL:
            print("\n  🎉 target reached — feel free to [q]uit.")

    print("\nDone. judgments.json saved.")


if __name__ == "__main__":
    main()
