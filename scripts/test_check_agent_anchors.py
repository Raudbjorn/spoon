"""Tracked temporary repositories exercise the Python checker and Go helper."""

import json
import os
from pathlib import Path
import runpy
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

check_agents = runpy.run_path(str(Path(__file__).with_name("check-agent-anchors.py")))["check_agents"]


class AgentAnchorTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        subprocess.run(["git", "init", "-q", str(self.root)], check=True)
        self.write("CLAUDE.md", "Fixture\n")
        self.write("docs/guide.md", "Fixture\n")
        self.write("go.mod", "module fixture\n\ngo 1.26\n")
        self.write("internal/example/source.go", "package example\n")
        self.write("internal/example/source_test.go", 'package example\nimport "testing"\nfunc TestAnchor(t *testing.T) {}\n')
        self.body = "`CLAUDE.md` `docs/guide.md` `internal/example/source_test.go:TestAnchor`"
        self.agent()
        self.track()

    def write(self, path, text):
        target = self.root / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(text, encoding="utf-8")

    def agent(self, filename="alpha", **changes):
        fields = dict(name=filename, description="Fixture agent", developer_instructions=self.body)
        fields.update(changes)
        self.write(f".codex/agents/{filename}.toml", "\n".join(f"{key} = {json.dumps(value)}" for key, value in fields.items()))

    def track(self):
        subprocess.run(["git", "-C", str(self.root), "add", "-A"], check=True)

    def errors(self):
        return "\n".join(check_agents(self.root))

    def test_valid_qualified_wrapped_alias_fuzz_and_benchmark(self):
        self.write("internal/example/other_test.go", 'package example\nimport check "testing"\nfunc FuzzInput(f *check.F) {}\nfunc BenchmarkInput(b *check.B) {}\n')
        self.agent(developer_instructions=self.body + " `\n./internal/example/other_test.go:FuzzInput\n` internal/example/other_test.go:BenchmarkInput")
        self.track()
        self.assertEqual(check_agents(self.root), [])

    def test_exact_prefixes_dot_import_and_test_main(self):
        self.write("internal/example/prefix_test.go", 'package example\nimport . "testing"\nfunc Test(t *T) {}\nfunc Fuzz(f *F) {}\nfunc Benchmark(b *B) {}\nfunc TestMain(m *M) {}\n')
        self.agent(developer_instructions=" ".join(f"`internal/example/prefix_test.go:{name}`" for name in ("Test", "Fuzz", "Benchmark", "TestMain")))
        self.track()
        self.assertEqual(check_agents(self.root), [])

    def test_bare_executable_names_fail_but_prose_does_not(self):
        self.agent(developer_instructions="Tests and Testing with Testify; model/corpus identity. Test the result.")
        self.assertEqual(check_agents(self.root), [])
        for name in ("TestAnchor", "FuzzInput", "BenchmarkInput"):
            with self.subTest(name=name):
                self.agent(developer_instructions=name)
                self.assertIn(f"bare executable anchor {name}", self.errors())
        self.agent(developer_instructions="`Test`")
        self.assertIn("bare executable anchor Test;", self.errors())

    def test_exact_file_and_package_binding(self):
        self.write("internal/elsewhere/other_test.go", 'package elsewhere\nimport "testing"\nfunc TestOther(t *testing.T) {}\n')
        self.agent(developer_instructions="`internal/elsewhere/other_test.go:TestAnchor`")
        self.track()
        self.assertIn("missing executable anchor: internal/elsewhere/other_test.go:TestAnchor", self.errors())
        self.write("internal/example/wrong_test.go", 'package wrong\nimport "testing"\nfunc TestWrong(t *testing.T) {}\n')
        self.agent(developer_instructions="`internal/example/wrong_test.go:TestWrong`")
        self.track()
        self.assertIn("package", self.errors())

    def test_invalid_executable_declarations(self):
        source = '''package example
import "testing"
import fake "other/testing"
type suite struct{}
func TestNoArgs() {}
func TestResult(t *testing.T) int { return 0 }
func TestValue(t testing.T) {}
func TestGeneric[T any](t *testing.T) {}
func (s suite) TestMethod(t *testing.T) {}
func TestFake(t *fake.T) {}
func Testlower(t *testing.T) {}
func FuzzWrong(t *testing.T) {}
func BenchmarkWrong(t *testing.T) {}
'''
        self.write("internal/example/invalid_test.go", source)
        names = ("TestNoArgs", "TestResult", "TestValue", "TestGeneric", "TestMethod", "TestFake", "Testlower", "FuzzWrong", "BenchmarkWrong")
        self.agent(developer_instructions=" ".join(f"`internal/example/invalid_test.go:{name}`" for name in names))
        self.track()
        errors = self.errors()
        for name in names:
            with self.subTest(name=name):
                # Methods are not top-level entrypoints, regardless of their signature.
                kind = "missing" if name == "TestMethod" else "invalid"
                self.assertIn(f"{kind} executable anchor: internal/example/invalid_test.go:{name}", errors)

    def test_method_does_not_overwrite_a_top_level_anchor(self):
        self.write("internal/example/source_test.go", 'package example\nimport "testing"\nfunc TestAnchor(t *testing.T) {}\ntype suite struct{}\nfunc (s suite) TestAnchor(t *testing.T) {}\n')
        self.assertEqual(check_agents(self.root), [])

    def test_file_anchors_cannot_resolve_to_directories(self):
        self.write("internal/example/directory_test.go/note.txt", "fixture")
        self.agent(developer_instructions="`internal/example/directory_test.go:TestAnchor`")
        self.track()
        self.assertIn("not a tracked regular file: internal/example/directory_test.go", self.errors())
        (self.root / "go.mod").unlink()
        self.write("go.mod/note.txt", "fixture")
        self.agent(developer_instructions="`go.mod`")
        self.track()
        self.assertIn("not a tracked regular file: go.mod", self.errors())

    def test_inactive_build_tags_do_not_satisfy_anchors(self):
        self.write("internal/example/inactive_test.go", '//go:build agent_anchor_unset_tag\n\npackage example\nimport "testing"\nfunc TestInactive(t *testing.T) {}\n')
        self.agent(developer_instructions="`internal/example/inactive_test.go:TestInactive`")
        self.track()
        self.assertIn("inactive for default build: internal/example/inactive_test.go", self.errors())

    def test_only_tracked_inventory_is_considered(self):
        self.write("untracked/reference_test.go", 'package reference\nimport "testing"\nfunc TestForeign(t *testing.T) {}\n')
        self.write("untracked/broken_test.go", "not Go syntax")
        self.write(".codex/agents/untracked.toml", "name = [")
        self.assertEqual(check_agents(self.root), [])
        self.agent(developer_instructions="`untracked/reference_test.go:TestForeign`")
        self.assertIn("not a tracked path: untracked/reference_test.go", self.errors())

    def test_excluded_directories_and_filenames(self):
        paths = (".do-not-commit/reference_test.go", "_reference/ref_test.go", "vendor/ref_test.go", "internal/example/testdata/ref_test.go", "internal/example/.scratch_test.go", "internal/example/_scratch_test.go", "internal/example/ordinary.go")
        for path in paths:
            self.write(path, 'package example\nimport "testing"\nfunc TestHidden(t *testing.T) {}\n')
        self.agent(developer_instructions=" ".join(f"`{path}:TestHidden`" for path in paths))
        self.track()
        errors = self.errors()
        for path in paths:
            with self.subTest(path=path):
                self.assertIn(f"excluded test source: {path}", errors)

    def test_paths_in_code_markdown_and_plain_prose(self):
        self.write("newroot/extensionless", "data")
        self.write("ROOTFILE.txt", "data")
        self.agent(developer_instructions="`\nnewroot/extensionless\n` [guide](docs/guide.md) ROOTFILE.txt model/corpus [TestRemote](https://example.test/internal/foo_test.go:TestOther) `https://example.test/FuzzRemote`")
        self.track()
        self.assertEqual(check_agents(self.root), [])
        self.agent(developer_instructions="`unknown/extensionless` [missing](elsewhere/missing) docs/missing.md `docs/\nwrapped.md` bare.go")
        errors = self.errors()
        for path in ("unknown/extensionless", "elsewhere/missing", "docs/missing.md", "docs/wrapped.md", "bare.go"):
            with self.subTest(path=path):
                self.assertIn(f"not a tracked path: {path}", errors)
        self.agent(developer_instructions="`/docs/guide.md`")
        self.assertIn("path must stay repository-relative: /docs/guide.md", self.errors())

    def test_root_directories_and_arbitrary_file_extensions_are_checked(self):
        paths = ("vendor/", "missing-root/", "./missing", "go.sum", "diagram.csv", "experiments/result")
        self.agent(developer_instructions=" ".join(f"`{path}`" for path in paths))
        errors = self.errors()
        for path in paths:
            with self.subTest(path=path):
                self.assertIn(f"not a tracked path: {path.removeprefix('./').rstrip('/')}", errors)
        for path in ("vendor/source.txt", "missing-root/source.txt", "missing", "go.sum", "diagram.csv", "experiments/result"):
            self.write(path, "fixture\n")
        self.track()
        self.assertEqual(check_agents(self.root), [])

    def test_unknown_keys_nested_tables_and_invalid_metadata(self):
        self.agent(model="override")
        self.assertIn("unexpected keys: model", self.errors())
        self.agent()
        target = self.root / ".codex/agents/alpha.toml"
        target.write_text(target.read_text() + '\n[tools]\nnetwork = true\n')
        self.assertIn("unexpected keys: tools", self.errors())
        target.write_text("name = [")
        self.assertIn("invalid TOML", self.errors())
        for field, value in (("name", " "), ("description", 1), ("developer_instructions", "")):
            self.agent(**{field: value})
            self.assertIn(f"{field} must be a nonempty string", self.errors())
        self.agent()
        self.agent("beta", name="alpha")
        self.track()
        self.assertIn("duplicate name 'alpha'", self.errors())

    def test_source_errors_are_aggregated_without_tracebacks(self):
        source = self.root / "internal/example/source_test.go"
        source.write_bytes(b"\xff")
        self.agent("beta", developer_instructions="`docs/missing.md`")
        self.track()
        errors = self.errors()
        self.assertIn("internal/example/source_test.go: cannot decode UTF-8", errors)
        self.assertIn("not a tracked path: docs/missing.md", errors)
        self.write("internal/example/source_test.go", 'package example\nimport "testing"\nfunc TestAnchor(t *testing.T) {}\n')
        original = Path.read_text
        def denied(path, *args, **kwargs):
            if path == source:
                raise PermissionError("fixture permission denied")
            return original(path, *args, **kwargs)
        with patch.object(Path, "read_text", denied):
            self.assertIn("internal/example/source_test.go: cannot read", self.errors())
        source.unlink()
        source.symlink_to("missing_test.go")
        self.track()
        self.assertIn("internal/example/source_test.go: symlink", self.errors())

    @unittest.skipUnless(hasattr(os, "mkfifo"), "named pipes are unavailable on this platform")
    def test_non_regular_role_and_go_sibling_reads_do_not_block(self):
        checker = Path(__file__).with_name("check-agent-anchors.py")
        child = (
            "import runpy, sys; from pathlib import Path; "
            "check = runpy.run_path(sys.argv[1])['check_agents']; "
            "errors = check(Path(sys.argv[2])); "
            "print('\\n'.join(errors)); sys.exit(bool(errors))"
        )
        for path in (".codex/agents/alpha.toml", "internal/example/source.go"):
            with self.subTest(path=path):
                target = self.root / path
                original = target.read_bytes()
                target.unlink()
                os.mkfifo(target)
                try:
                    # A tracked regular file replaced by a FIFO must fail before open;
                    # an outer timeout bounds the regression even against the old code.
                    result = subprocess.run(
                        [sys.executable, "-c", child, str(checker), str(self.root)],
                        capture_output=True, text=True, timeout=2,
                    )
                    self.assertEqual(result.returncode, 1)
                    self.assertIn(f"{path}: cannot read: not a regular file", result.stdout)
                    self.assertNotIn("Traceback", result.stderr)
                finally:
                    target.unlink()
                    target.write_bytes(original)


if __name__ == "__main__":
    unittest.main()
