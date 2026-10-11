"""Exercise the documented CLI surfaces for all five benchmark conditions."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


SHIM = Path(__file__).resolve().parent / "bin" / "entire"


class EntireShimTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        root = Path(self.temp.name)
        self.real = root / "entire"
        self.brain = root / "brain"
        for binary in (self.real, self.brain):
            binary.write_text("#!/usr/bin/env python3\nimport json, sys\n"
                              "print(json.dumps(sys.argv[1:]))\n")
            binary.chmod(0o755)

    def invoke(self, condition, *args, brain=False):
        env = dict(os.environ, LADDER_COND=str(condition), LADDER_REAL_ENTIRE=str(self.real))
        env.pop("LADDER_BRAIN_BIN", None)
        if brain:
            env["LADDER_BRAIN_BIN"] = str(self.brain)
        return subprocess.run(["sh", str(SHIM), *args], env=env,
                              capture_output=True, text=True)

    def test_unrelated_cli_surfaces_are_denied_in_every_condition(self):
        for condition in range(1, 6):
            for subcommand in ("trail", "api", "auth"):
                with self.subTest(condition=condition, subcommand=subcommand):
                    result = self.invoke(condition, subcommand, "--help")
                    self.assertNotEqual(result.returncode, 0, result)
                    self.assertEqual(result.stdout, "")

    def test_memory_surfaces_follow_the_ladder(self):
        for condition in range(1, 6):
            for subcommand, minimum in (("search", 2), ("graph", 3), ("brain", 4)):
                with self.subTest(condition=condition, subcommand=subcommand):
                    result = self.invoke(condition, subcommand, "query with spaces")
                    if condition >= minimum:
                        self.assertEqual(result.returncode, 0, result)
                        self.assertEqual(json.loads(result.stdout), [subcommand, "query with spaces"])
                    else:
                        self.assertNotEqual(result.returncode, 0, result)
                        self.assertEqual(result.stdout, "")

    def test_brain_override_remains_scoped_to_brain_in_conditions_four_and_five(self):
        for condition in (4, 5):
            result = self.invoke(condition, "brain", "recall", "query with spaces", brain=True)
            self.assertEqual(result.returncode, 0, result)
            self.assertEqual(json.loads(result.stdout), ["recall", "query with spaces"])
            result = self.invoke(condition, "search", "query with spaces", brain=True)
            self.assertEqual(json.loads(result.stdout), ["search", "query with spaces"])

    def test_hooks_are_swallowed_in_every_condition(self):
        for condition in range(1, 6):
            result = self.invoke(condition, "hooks", "claude-code", "session-end")
            self.assertEqual(result.returncode, 0, result)
            self.assertEqual(result.stdout, "")


if __name__ == "__main__":
    unittest.main()
