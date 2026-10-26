import json
import pathlib
import subprocess
import sys
import tempfile
import unittest

TOOL = pathlib.Path(__file__).resolve().parent / "affected.py"
ROOT = pathlib.Path(__file__).resolve().parents[2]
MODULE = "github.com/entireio/entire-brain"


def run_tool(file_names):
    with tempfile.NamedTemporaryFile("w", suffix=".txt", delete=False) as handle:
        handle.write("\n".join(file_names) + "\n")
        listing = handle.name
    try:
        output = subprocess.check_output(
            [sys.executable, "-B", str(TOOL), "--changed-files", listing],
            cwd=ROOT,
            text=True,
        )
    finally:
        pathlib.Path(listing).unlink()
    return json.loads(output)


class AffectedSelectionTests(unittest.TestCase):
    def test_non_go_change_selects_the_full_suite(self):
        decision = run_tool(["tools/coverage/run.py"])
        self.assertEqual(decision["mode"], "full")

    def test_module_file_change_selects_the_full_suite(self):
        decision = run_tool(["go.mod"])
        self.assertEqual(decision["mode"], "full")

    def test_empty_change_set_selects_nothing(self):
        decision = run_tool([])
        self.assertEqual(decision["mode"], "none")

    def test_go_file_outside_every_package_selects_the_full_suite(self):
        decision = run_tool(["no/such/dir/x.go"])
        self.assertEqual(decision["mode"], "full")

    def test_leaf_package_change_includes_its_importers(self):
        decision = run_tool(["internal/cli/patterns/corpus_access.go"])
        self.assertEqual(decision["mode"], "selected")
        packages = set(decision["packages"])
        self.assertIn(MODULE + "/internal/cli/patterns", packages)
        # cli imports patterns, and the main module wires the cli root command,
        # so both must re-run when patterns changes.
        self.assertIn(MODULE + "/internal/cli", packages)

    def test_top_package_change_excludes_packages_it_does_not_reach(self):
        decision = run_tool(["internal/cli/root.go"])
        self.assertEqual(decision["mode"], "selected")
        packages = set(decision["packages"])
        self.assertIn(MODULE + "/internal/cli", packages)
        # Nothing in cli is imported by the leaf helper packages, so a cli-only
        # change must not re-run them.
        self.assertNotIn(MODULE + "/internal/cli/patterns", packages)
        self.assertNotIn(MODULE + "/internal/cli/internal/base", packages)

    def test_test_only_dependency_affects_its_test_importers(self):
        decision = run_tool(["internal/cli/internal/clitest/clitest.go"])
        self.assertEqual(decision["mode"], "selected")
        packages = set(decision["packages"])
        # clitest is imported only by _test.go files; test imports must still
        # count as affectedness or moved pins would silently stop re-running.
        self.assertIn(MODULE + "/internal/cli", packages)
        self.assertIn(MODULE + "/internal/cli/patterns", packages)


if __name__ == "__main__":
    unittest.main()
