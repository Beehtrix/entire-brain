"""Verify the git shim preserves argv while adding its exclusion pathspec."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


SHIM = Path(__file__).resolve().parent / "bin" / "git"


class GitShimTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.repo = Path(self.temp.name)
        fake_bin = self.repo / "bin"
        fake_bin.mkdir()
        git = fake_bin / "git"
        git.write_text("#!/usr/bin/env python3\nimport json, os, sys\n"
                       "if sys.argv[1:] == ['rev-parse', '--show-toplevel']:\n"
                       "    print(os.getcwd())\n"
                       "else:\n    print(json.dumps(sys.argv[1:]))\n")
        git.chmod(0o755)
        self.env = dict(os.environ, PATH=str(fake_bin) + os.pathsep + os.environ["PATH"])

    def invoke(self, args):
        result = subprocess.run(["sh", str(SHIM), "log", *args], cwd=self.repo,
                                env=self.env, capture_output=True, text=True, check=True)
        return json.loads(result.stdout)

    def test_many_arguments_preserve_options_and_space_in_path(self):
        options = [f"--option={i}" for i in range(12)]
        path = "a path with spaces.txt"
        (self.repo / path).write_text("test")
        self.assertEqual(self.invoke([*options, path]),
                         ["log", *options, "--", path, ":!benchmarks/memory-ladder"])

    def test_argument_contents_are_never_evaluated(self):
        args = [f"--option={i}" for i in range(10)]
        args.append("--format=$(touch sentinel)`touch sentinel`'\"\\")
        self.assertEqual(self.invoke(args), ["log", *args, "--", ":!benchmarks/memory-ladder"])
        self.assertFalse((self.repo / "sentinel").exists())


if __name__ == "__main__":
    unittest.main()
