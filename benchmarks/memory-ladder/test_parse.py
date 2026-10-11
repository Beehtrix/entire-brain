"""Exercise escape auditing through the parser and grading CLI contracts."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


HERE = Path(__file__).resolve().parent


class EscapeAuditTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.out = self.root / "runs"
        self.run = self.out / "global-activation" / "c1-r1"
        self.clone = self.run / "repo"
        self.clone.mkdir(parents=True)
        self.env = dict(os.environ, LADDER_OUT=str(self.out),
                        LADDER_OUT_ROOT=str(self.out), LADDER_SRC=str(self.root / "source"),
                        GOCACHE=str(self.root / "go-cache"), GOMODCACHE=str(self.root / "go-mod"))

    def parse(self, commands):
        events = [{"type": "assistant", "message": {"content": [
            {"type": "tool_use", "name": "Bash", "input": {"command": command}}
        ]}} for command in commands]
        events.append({"type": "result", "subtype": "success", "is_error": False,
                       "result": "A completed answer", "usage": {}})
        (self.run / "stream.jsonl").write_text("\n".join(json.dumps(e) for e in events))
        result = subprocess.run([sys.executable, "-B", str(HERE / "parse.py"), str(self.run)],
                                env=self.env, capture_output=True, text=True, check=True)
        return json.loads(result.stdout)

    def test_parent_directory_commands_contaminate_run(self):
        for command in ("cd ..", "cd '../..'", "cd ../..", "cd ..; cat summary.json",
                        "cd ./..", "cd ./../..", "cd docs/../..",
                        "cd ../../..", "cd ../../../.."):
            with self.subTest(command=command):
                summary = self.parse([command, "cat summary.json"])
                self.assertTrue(summary["contaminated"], summary)
                self.assertTrue(summary["escape_paths"], summary)

    def test_clone_and_system_paths_remain_allowed(self):
        summary = self.parse(["cat ./README.md", "cat ..notes", "echo ...", "cd docs/..",
                              f"cat {self.clone}/README.md", "/usr/bin/git status"])
        self.assertFalse(summary["contaminated"], summary)
        self.assertEqual(summary["escape_paths"], [])

    def test_sibling_run_absolute_path_contaminates(self):
        summary = self.parse([f"cat {self.out}/global-activation/c2-r1/summary.json"])
        self.assertTrue(summary["contaminated"], summary)

    def test_output_root_directory_contaminates(self):
        summary = self.parse([f"cd {self.out}", "cat global-activation/c2-r1/summary.json"])
        self.assertTrue(summary["contaminated"], summary)

    def test_parent_escape_is_excluded_from_grading(self):
        summary = self.parse(["cd ..", "cat summary.json"])
        (self.run / "summary.json").write_text(json.dumps(summary))
        result = subprocess.run([sys.executable, "-B", str(HERE / "grade.py"),
                                 "global-activation", "--json"], env=self.env,
                                capture_output=True, text=True, check=True)
        rows = json.loads(result.stdout)
        self.assertEqual(len(rows), 1)
        self.assertTrue(rows[0]["contaminated"], rows)
        self.assertFalse(rows[0]["completed"], rows)
        self.assertIsNone(rows[0]["recovered"], rows)


if __name__ == "__main__":
    unittest.main()
