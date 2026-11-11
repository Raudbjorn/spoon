#!/usr/bin/env python3
"""Check native agent metadata and source anchors using Python 3.11+ only."""

import os
from pathlib import Path
import re
import sys
import tomllib


TEST_NAME = re.compile(r"\bTest[A-Za-z0-9_]+\b")
GO_NON_CODE = re.compile(
    r'//[^\n]*|/\*[\s\S]*?\*/|`[^`]*`|"(?:\\.|[^"\\])*"|\'(?:\\.|[^\'\\])*\''
)
PATH_ROOTS = ("internal", "cmd", "docs", "packaging", ".codex", "vendor", "experiments", "scripts")
ROOT_FILES = {"go.mod", "go.sum"}


def go_test_names(root: Path) -> set[str]:
    # shortcut: lexical declarations ignore signatures/build tags; use go/parser if those become requirements.
    names = set()
    for directory, children, files in os.walk(root):
        children[:] = [
            name for name in children
            if name not in {"vendor", "testdata"} and not name.startswith((".", "_"))
        ]
        for filename in files:
            if filename.startswith((".", "_")) or not filename.endswith("_test.go"):
                continue
            source = (Path(directory) / filename).read_text(encoding="utf-8")
            source = GO_NON_CODE.sub(lambda m: " " + "\n" * m.group().count("\n"), source)
            depth = 0
            for token in re.finditer(r"\bfunc\s+(Test[A-Za-z0-9_]+)\s*\(|[{}]", source):
                if token[1] and depth == 0:
                    names.add(token[1])
                elif token[0] == "{":
                    depth += 1
                elif token[0] == "}":
                    depth -= 1
    return names


def path_anchors(text: str) -> set[str]:
    paths = set()
    for code in re.findall(r"`([^`\n]+)`", text):
        for token in code.split():
            token = token.rstrip(",;")
            path = token.removeprefix("./").removesuffix("/...")
            if path in ROOT_FILES or path.endswith((".go", ".md", ".toml")) or any(
                path == prefix or path.startswith(prefix + "/") for prefix in PATH_ROOTS
            ):
                paths.add(path)
    return paths


def check_agents(root: Path) -> list[str]:
    root = root.resolve()
    errors = []
    agents = sorted((root / ".codex/agents").glob("*.toml"))
    if not agents:
        return [".codex/agents: no agent TOML files found"]
    tests = go_test_names(root)
    names = set()
    for agent in agents:
        label = agent.relative_to(root)
        try:
            data = tomllib.loads(agent.read_text(encoding="utf-8"))
        except (OSError, UnicodeError, tomllib.TOMLDecodeError) as error:
            errors.append(f"{label}: invalid TOML: {error}")
            continue
        for field in ("name", "description", "developer_instructions"):
            if not isinstance(data.get(field), str) or not data[field].strip():
                errors.append(f"{label}: {field} must be a nonempty string")
        name = data.get("name")
        if isinstance(name, str) and name.strip():
            if name != agent.stem:
                errors.append(f"{label}: name {name!r} must match filename {agent.stem!r}")
            if name in names:
                errors.append(f"{label}: duplicate name {name!r}")
            names.add(name)
        text = "\n".join(value for value in data.values() if isinstance(value, str))
        for anchor in sorted(path_anchors(text)):
            target = root / anchor
            if Path(anchor).is_absolute() or not target.resolve().is_relative_to(root):
                errors.append(f"{label}: path must stay repository-relative: {anchor}")
            elif not (
                target.is_file()
                if target.suffix in {".go", ".md", ".toml"} or anchor in ROOT_FILES
                else target.exists()
            ):
                errors.append(f"{label}: missing path: {anchor}")
        for name in sorted(set(TEST_NAME.findall(text)) - tests):
            errors.append(f"{label}: missing top-level test: {name}")
    return errors


if __name__ == "__main__":
    repository = Path(__file__).resolve().parents[1]
    problems = check_agents(repository)
    if problems:
        print("\n".join(problems), file=sys.stderr)
        sys.exit(1)
    count = len(list((repository / ".codex/agents").glob("*.toml")))
    print(f"Validated {count} agent definitions and their path/test anchors.")
