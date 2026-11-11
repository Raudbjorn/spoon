"""Small isolated fixtures for check-agent-anchors.py; no Go tools required."""

import json
from pathlib import Path
import runpy
import tempfile
import unittest


check_agents = runpy.run_path(str(Path(__file__).with_name("check-agent-anchors.py")))[
    "check_agents"
]


class AgentAnchorTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        for directory in (".codex/agents", "internal/example", "docs", "cmd", "packaging"):
            (self.root / directory).mkdir(parents=True)
        for filename in ("CLAUDE.md", "docs/guide.md", "internal/example/source.go"):
            (self.root / filename).touch()
        (self.root / "internal/example/source_test.go").write_text(
            "package example\nfunc TestAnchor(t *testing.T) {}\n", encoding="utf-8"
        )
        self.body = (
            "`CLAUDE.md` `docs/guide.md` `internal/example/source.go` "
            "`cmd` `packaging` `.codex/agents` `go test ./internal/example/...` TestAnchor"
        )
        self.write_agent()

    def write_agent(self, filename="alpha", **changes):
        fields = dict(name=filename, description="Fixture agent", developer_instructions=self.body)
        fields.update(changes)
        (self.root / f".codex/agents/{filename}.toml").write_text(
            "\n".join(f"{key} = {json.dumps(value)}" for key, value in fields.items()),
            encoding="utf-8",
        )

    def test_valid_paths_and_exact_test(self):
        (self.root / "internal/example/spaced_test.go").write_text(
            "package example\nfunc/* separator */\nTestSpaced(t *testing.T) {}\n"
        )
        self.write_agent(developer_instructions=self.body + " TestSpaced")
        self.assertEqual(check_agents(self.root), [])

    def test_missing_and_bare_paths_are_not_resolved_by_basename(self):
        (self.root / "internal/example/bare.go").touch()
        self.write_agent(developer_instructions=self.body + " `bare.go` `docs/missing.md`")
        errors = "\n".join(check_agents(self.root))
        self.assertIn("missing path: bare.go", errors)
        self.assertIn("missing path: docs/missing.md", errors)

    def test_test_names_ignore_prefixes_comments_strings_methods_and_vendor(self):
        (self.root / "vendor").mkdir()
        (self.root / "vendor/ignored_test.go").write_text("func TestVendor() {}\n")
        (self.root / "internal/example/other_test.go").write_text(
            "package example\nfunc TestAbsentLonger() {}\n"
            "/*\nfunc TestComment() {}\n*/\n"
            "var fixture = `\nfunc TestString() {}\n`\n"
            "func (s suite) TestMethod() {}\n"
        )
        for name in ("TestAbsent", "TestComment", "TestString", "TestMethod", "TestVendor"):
            with self.subTest(name=name):
                self.write_agent(developer_instructions=name)
                self.assertIn(f"missing top-level test: {name}", "\n".join(check_agents(self.root)))

    def test_test_names_ignore_reference_clones_and_fixture_directories(self):
        directories = (
            ".do-not-commit/reference", ".hidden", "_reference",
            "internal/example/_scratch", "internal/example/testdata", "testdata",
        )
        for index, directory in enumerate(directories):
            with self.subTest(directory=directory):
                target = self.root / directory
                target.mkdir(parents=True)
                name = f"TestIgnored{index}"
                (target / "reference_test.go").write_text(f"package fixture\nfunc {name}() {{}}\n")
                self.write_agent(developer_instructions=name)
                # Reference/fixture trees are not runnable package-test anchors.
                self.assertIn(f"missing top-level test: {name}", "\n".join(check_agents(self.root)))

    def test_test_names_ignore_non_test_and_prefixed_filenames(self):
        for index, filename in enumerate((".scratch_test.go", "_scratch_test.go", "ordinary.go")):
            with self.subTest(filename=filename):
                name = f"TestIgnoredFilename{index}"
                (self.root / "internal/example" / filename).write_text(
                    f"package example\nfunc {name}() {{}}\n", encoding="utf-8"
                )
                self.write_agent(developer_instructions=self.body + f" {name}")
                # Go ignores prefixed filenames and does not run tests from ordinary source files.
                self.assertIn(f"missing top-level test: {name}", "\n".join(check_agents(self.root)))

    def test_module_vendor_experiment_and_script_paths_must_exist(self):
        files = ("go.mod", "go.sum", "scripts/check-agent-anchors.py")
        directories = ("vendor/", "experiments/started/behavioral-embeddings")
        self.write_agent(developer_instructions=" ".join(f"`{path}`" for path in files + directories))
        errors = "\n".join(check_agents(self.root))
        for path in files + directories:
            with self.subTest(path=path):
                # These explicit current/future prompt paths must not be ignored.
                self.assertIn(f"missing path: {path}", errors)
        for path in files:
            target = self.root / path
            target.parent.mkdir(parents=True, exist_ok=True)
            target.touch()
        for path in directories:
            (self.root / path).mkdir(parents=True)
        self.assertEqual(check_agents(self.root), [])

    def test_invalid_toml_fields_and_duplicate_name(self):
        agent = self.root / ".codex/agents/alpha.toml"
        agent.write_text("name = [")
        self.assertIn("invalid TOML", "\n".join(check_agents(self.root)))
        for field, value in (("name", " "), ("description", 1), ("developer_instructions", "")):
            with self.subTest(field=field):
                self.write_agent(**{field: value})
                self.assertIn(f"{field} must be a nonempty string", "\n".join(check_agents(self.root)))
        self.write_agent()
        self.write_agent("beta", name="alpha")
        errors = "\n".join(check_agents(self.root))
        self.assertIn("must match filename 'beta'", errors)
        self.assertIn("duplicate name 'alpha'", errors)


if __name__ == "__main__":
    unittest.main()
