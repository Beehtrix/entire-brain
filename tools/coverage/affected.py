#!/usr/bin/env python3
"""Select the Go packages a change set can affect, for PR test selection.

Reads the changed file list (from git or --changed-files), maps every changed
.go file to its package, expands through the reverse import graph (production,
test, and external-test imports), and prints a JSON decision:

  {"mode": "full"}                     — fall back to the complete suite
  {"mode": "none"}                     — no Go package is affected
  {"mode": "selected", "packages": []} — run exactly these packages

The selection FAILS SAFE: any changed file that is not a .go file inside a
listed package directory — workflow edits, go.mod/go.sum, tools, embedded
assets, testdata — selects the full suite, because those inputs can change Go
behavior without changing a .go file. Selection must only ever narrow a run it
can prove is equivalent for the affected packages.
"""
import argparse
import json
import pathlib
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parents[2]


def changed_files(base, head, cwd=ROOT):
    # --no-renames keeps a rename visible as an add plus a delete, so a .go
    # file moved between packages marks BOTH packages as changed. With rename
    # detection, --name-only lists only the destination and the source package
    # would silently keep its stale selection.
    output = subprocess.check_output(
        ["git", "diff", "--name-only", "--no-renames", f"{base}", f"{head}"],
        cwd=cwd,
        text=True,
    )
    return [line for line in output.splitlines() if line.strip()]


def load_packages(go):
    """Return {import_path: {dir, deps_on}} with reverse edges unexpanded."""
    output = subprocess.check_output(
        [go, "list", "-json=ImportPath,Dir,Imports,TestImports,XTestImports", "./..."],
        cwd=ROOT,
        text=True,
    )
    packages = {}
    decoder = json.JSONDecoder()
    index = 0
    while index < len(output):
        while index < len(output) and output[index] in " \r\n\t":
            index += 1
        if index >= len(output):
            break
        package, end = decoder.raw_decode(output, index)
        index = end
        packages[package["ImportPath"]] = {
            "dir": pathlib.Path(package["Dir"]).resolve(),
            "imports": set(
                dependency
                for key in ("Imports", "TestImports", "XTestImports")
                for dependency in package.get(key) or []
            ),
        }
    return packages


def decide(files, packages):
    if not files:
        return {"mode": "none"}
    by_dir = {info["dir"]: path for path, info in packages.items()}
    changed = set()
    for name in files:
        if not name.endswith(".go"):
            return {"mode": "full", "reason": f"non-Go change: {name}"}
        directory = (ROOT / name).parent.resolve()
        package = by_dir.get(directory)
        if package is None:
            # A .go file outside every listed package (deleted directory, new
            # package the listing has but the mapping missed, ignored tree).
            return {"mode": "full", "reason": f"unmapped Go file: {name}"}
        changed.add(package)
    # Reverse-import closure: every package whose compile or test inputs
    # include a changed package must re-run.
    affected = set(changed)
    grew = True
    while grew:
        grew = False
        for path, info in packages.items():
            if path in affected:
                continue
            if info["imports"] & affected:
                affected.add(path)
                grew = True
    return {"mode": "selected", "packages": sorted(affected)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base", help="base commit for git diff")
    parser.add_argument("--head", default="HEAD", help="head commit for git diff")
    parser.add_argument(
        "--changed-files",
        help="newline-separated file list to use instead of git diff (tests)",
    )
    parser.add_argument("--go-command", default="go")
    args = parser.parse_args()
    if args.changed_files:
        files = [
            line
            for line in pathlib.Path(args.changed_files).read_text().splitlines()
            if line.strip()
        ]
    elif args.base:
        files = changed_files(args.base, args.head)
    else:
        parser.error("one of --base or --changed-files is required")
    decision = decide(files, load_packages(args.go_command))
    json.dump(decision, sys.stdout)
    print()


if __name__ == "__main__":
    main()
