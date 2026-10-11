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

    def parse(self, commands, tool_results=()):
        events = [{"type": "assistant", "message": {"content": [
            {"type": "tool_use", "name": "Bash", "input": {"command": command}}
        ]}} for command in commands]
        events.extend({"type": "user", "message": {"content": [
            {"type": "tool_result", "content": body}
        ]}} for body in tool_results)
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
        self.assert_excluded_from_grading(summary)

    def assert_excluded_from_grading(self, summary):
        (self.run / "summary.json").write_text(json.dumps(summary))
        result = subprocess.run([sys.executable, "-B", str(HERE / "grade.py"),
                                 "global-activation", "--json"], env=self.env,
                                capture_output=True, text=True, check=True)
        rows = json.loads(result.stdout)
        self.assertEqual(len(rows), 1)
        self.assertTrue(rows[0]["contaminated"], rows)
        self.assertFalse(rows[0]["completed"], rows)
        self.assertIsNone(rows[0]["recovered"], rows)

    def test_mixed_commit_metadata_is_excluded_from_grading(self):
        env = dict(self.env, GIT_AUTHOR_NAME="Test", GIT_AUTHOR_EMAIL="test@invalid",
                   GIT_COMMITTER_NAME="Test", GIT_COMMITTER_EMAIL="test@invalid")
        def git(*args):
            return subprocess.run(["git", "-c", "core.hooksPath=/dev/null", *args],
                                  cwd=self.clone, env=env, capture_output=True,
                                  text=True, check=True).stdout
        git("init", "-q")
        (self.clone / "source.txt").write_text("ordinary source change\n")
        harness = self.clone / "benchmarks" / "memory-ladder"
        harness.mkdir(parents=True)
        (harness / "answer.txt").write_text("hidden answer\n")
        git("add", ".")
        git("commit", "-qm", "Memory-Ladder: change harness and ordinary source")
        for args in (("show", "HEAD"), ("log", "--format=%B"), ("shortlog", "-s", "HEAD")):
            # shortlog -s contains only author/count metadata and should stay clean.
            with self.subTest(args=args):
                result = subprocess.run(["sh", str(HERE / "bin" / "git"), *args],
                                        cwd=self.clone, env=env, capture_output=True,
                                        text=True, check=True)
                self.assertNotIn("hidden answer", result.stdout)
                summary = self.parse(["git " + " ".join(args)], [result.stdout])
                if args[0] == "shortlog":
                    self.assertFalse(summary["contaminated"], summary)
                else:
                    self.assertTrue(summary["contaminated"], summary)
                    self.assert_excluded_from_grading(summary)

    def test_refused_harness_probe_stays_clean(self):
        summary = self.parse(["git show HEAD"], [
            "git: memory-ladder is not available in this environment"])
        self.assertFalse(summary["contaminated"], summary)

    def test_refusal_does_not_hide_metadata_in_same_result(self):
        summary = self.parse(["git log"], [
            "git: that path is not available in this environment\n"
            "Memory-Ladder: change harness and ordinary source"])
        self.assertTrue(summary["contaminated"], summary)


if __name__ == "__main__":
    unittest.main()
