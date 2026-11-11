#!/usr/bin/env python3
"""Validate tracked agent definitions and anchors; Python 3.11+, Git and local Go.

Reference syntax is deliberately small: inline backticks (including wrapped spans),
inline Markdown destinations, and bare file-like tokens or tracked-root paths.
Ambiguous prose such as model/corpus is not a path unless explicitly marked.
"""

import json
import os
from pathlib import Path, PurePosixPath
import re
import stat
import subprocess
import sys
import tomllib

FIELDS = {"name", "description", "developer_instructions"}
FILE_NAME = re.compile(r".+\.[a-z][a-z0-9]*$")
EXECUTABLE = re.compile(r"\b(?:Test|Fuzz|Benchmark)\w*\b")
QUALIFIED = re.compile(r"(?P<path>[\w./+-]+\.go):(?P<name>(?:Test|Fuzz|Benchmark)\w*)\b")
TOKENS = re.compile(r"(?:\./|/)?[\w.+-]+(?:/[\w.+-]+)*/?(?:\.\.\.)?")
HELPER = Path(__file__).with_name("check-agent-go-anchors.go")


def excluded(path: str) -> bool:
    return any(part.startswith((".", "_")) or part in {"vendor", "testdata"} for part in PurePosixPath(path).parts)


def safe_read(root: Path, path: str, errors: list[str]) -> str | None:
    target = root / path
    try:
        for parent in (target, *target.parents):
            if parent == root:
                break
            if parent.is_symlink():
                errors.append(f"{path}: symlink sources are not allowed ({parent.relative_to(root)})")
                return None
        if not stat.S_ISREG(target.stat().st_mode):
            raise OSError("not a regular file")
        return target.read_text(encoding="utf-8")
    except UnicodeError as error:
        errors.append(f"{path}: cannot decode UTF-8: {error}")
    except OSError as error:
        errors.append(f"{path}: cannot read: {error}")
    return None


def references(text: str, tracked: set[str]) -> tuple[set[str], set[tuple[str, str]], set[str]]:
    # shortcut: inline references only; use a Markdown parser if richer link syntax is needed.
    # External destinations and their labels are not local source anchors.
    text = re.sub(r"\[[^\]]*\]\(<?[A-Za-z][\w+.-]*:[^)]*\)", "", text)
    text = re.sub(r"\b[A-Za-z][\w+.-]*://[^\s`)>]+", "", text)
    # Only join wrapped path components, not arbitrary whitespace in commands.
    text = re.sub(r"(?<=[/:])[^\S\n]*\n\s*", "", text)
    qualified = {(m["path"].removeprefix("./"), m["name"]) for m in QUALIFIED.finditer(text)}
    remaining = QUALIFIED.sub("", text)
    bare = set()
    for match in EXECUTABLE.finditer(remaining):
        name = match[0]
        prefix = next(prefix for prefix in ("Test", "Fuzz", "Benchmark") if name.startswith(prefix))
        suffix = name[len(prefix):]
        if suffix and not suffix[0].islower():
            bare.add(name)
    # An unqualified exact prefix is only an anchor when explicitly marked;
    # ordinary prose can begin "Test the cancellation path".
    bare.update(code.strip() for code in re.findall(r"`+([^`]+)`+", remaining)
                if code.strip() in {"Test", "Fuzz", "Benchmark"})
    roots = {path.split("/", 1)[0] for path in tracked}
    paths = {path for path, _ in qualified}
    def add_tokens(value: str, explicit: bool) -> None:
        for match in TOKENS.finditer(value):
            original = match[0]
            path = original.removeprefix("./").removesuffix("/...").rstrip("/")
            if not path or path in {".", "..", "..."}:
                continue
            if ((explicit and ("/" in original or original.startswith("."))) or
                original.startswith(("./", "/")) or original.endswith("/") or
                path in tracked or (explicit and path in roots) or
                FILE_NAME.fullmatch(PurePosixPath(path).name) or
                ("/" in path and path.split("/", 1)[0] in roots)):
                paths.add(path)
    add_tokens(remaining, False)
    for code in re.findall(r"`+([^`]+)`+", remaining):
        add_tokens(code, True)
    for match in re.finditer(r"\[[^\]]*\]\((?:<([^>]+)>|([^\s)]+))\)", remaining):
        path = match[1] or match[2]
        if not re.match(r"[A-Za-z][\w+.-]*:", path) and not path.startswith("#"):
            paths.add(path.split("#", 1)[0].removeprefix("./").rstrip("/"))
    return paths, qualified, bare


def check_agents(root: Path) -> list[str]:
    root = root.resolve()
    errors = []
    try:
        inventory = subprocess.run(["git", "-C", str(root), "ls-files", "-z"], capture_output=True, check=True, timeout=10)
        tracked = set(inventory.stdout.decode("utf-8").rstrip("\0").split("\0")) - {""}
    except (OSError, UnicodeError, subprocess.SubprocessError) as error:
        return [f"git tracked inventory: {error}"]
    agents = sorted(path for path in tracked if path.startswith(".codex/agents/") and path.endswith(".toml") and path.count("/") == 2)
    if not agents:
        return [".codex/agents: no tracked agent TOML files found"]
    names = set()
    requests: dict[tuple[str, str], set[str]] = {}
    for agent in agents:
        source = safe_read(root, agent, errors)
        if source is None:
            continue
        try:
            data = tomllib.loads(source)
        except tomllib.TOMLDecodeError as error:
            errors.append(f"{agent}: invalid TOML: {error}")
            continue
        if extra := set(data) - FIELDS:
            errors.append(f"{agent}: unexpected keys: {', '.join(sorted(extra))}")
        for field in sorted(FIELDS):
            if not isinstance(data.get(field), str) or not data[field].strip():
                errors.append(f"{agent}: {field} must be a nonempty string")
        name = data.get("name")
        if isinstance(name, str) and name.strip():
            if name != Path(agent).stem:
                errors.append(f"{agent}: name {name!r} must match filename {Path(agent).stem!r}")
            if name in names:
                errors.append(f"{agent}: duplicate name {name!r}")
            names.add(name)
        text = "\n".join(data[field] for field in FIELDS if isinstance(data.get(field), str))
        paths, qualified, bare = references(text, tracked)
        executable_paths = {path for path, _ in qualified}
        invalid = set()
        for path in sorted(paths):
            relative = PurePosixPath(path)
            if relative.is_absolute() or ".." in relative.parts:
                errors.append(f"{agent}: path must stay repository-relative: {path}")
                invalid.add(path)
            elif path not in tracked and not any(item.startswith(path + "/") for item in tracked):
                errors.append(f"{agent}: not a tracked path: {path}")
                invalid.add(path)
            elif path not in tracked and (path in executable_paths or relative.suffix in {".go", ".md", ".toml"} or path in {"go.mod", "go.sum"}):
                errors.append(f"{agent}: not a tracked regular file: {path}")
                invalid.add(path)
            else:
                try:
                    target = root / path
                    for parent in (target, *target.parents):
                        if parent == root:
                            break
                        if parent.is_symlink():
                            raise OSError(f"symlink reference: {parent.relative_to(root)}")
                    if not target.exists() or (path in tracked and not target.is_file()):
                        raise OSError("tracked path is missing or has changed kind")
                except OSError as error:
                    errors.append(f"{agent}: {path}: {error}")
                    invalid.add(path)
        for name in sorted(bare):
            errors.append(f"{agent}: bare executable anchor {name}; use repo/path/file_test.go:{name}")
        for path, name in sorted(qualified):
            if path in invalid:
                continue
            if excluded(path) or not path.endswith("_test.go"):
                errors.append(f"{agent}: excluded test source: {path}")
            else:
                requests.setdefault((path, name), set()).add(agent)
    if requests:
        directories = {str(PurePosixPath(path).parent) for path, _ in requests}
        sources = {}
        for path in sorted(tracked):
            if path.endswith(".go") and not excluded(path) and str(PurePosixPath(path).parent) in directories:
                source = safe_read(root, path, errors)
                if source is not None:
                    sources[path] = source
        environment = dict(os.environ, GOTOOLCHAIN="local", GOPROXY="off", GOSUMDB="off", GOWORK="off", GOENV="off", GOFLAGS="")
        try:
            output = subprocess.run(["go", "run", str(HELPER)], cwd=HELPER.parent, env=environment,
                                    input=json.dumps({"root": str(root), "sources": sources}),
                                    text=True, capture_output=True, timeout=60, check=True)
            results = json.loads(output.stdout)
            for (path, name), owners in sorted(requests.items()):
                if path not in sources:
                    continue  # The precise read/decode error was already recorded.
                result = results.get(path, {})
                problem = result.get("error")
                if problem:
                    diagnostic = f"{problem}: {path}"
                elif name not in result.get("functions", {}):
                    diagnostic = f"missing executable anchor: {path}:{name}"
                elif reason := result["functions"][name]:
                    diagnostic = f"invalid executable anchor: {path}:{name}: {reason}"
                else:
                    continue
                errors.extend(f"{owner}: {diagnostic}" for owner in sorted(owners))
        except subprocess.CalledProcessError as error:
            errors.append(f"Go anchor helper failed: {error.stderr.strip()}")
        except (OSError, ValueError, subprocess.SubprocessError) as error:
            errors.append(f"Go anchor helper failed: {error}")
    return errors


if __name__ == "__main__":
    problems = check_agents(Path(__file__).resolve().parents[1])
    if problems:
        print("\n".join(problems), file=sys.stderr)
        sys.exit(1)
    print("Validated tracked agent definitions and their path/executable anchors.")
