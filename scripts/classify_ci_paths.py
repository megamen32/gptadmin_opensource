#!/usr/bin/env python3
"""Fail-closed classification of operator-only GitHub CI changes."""
from __future__ import annotations

import argparse
import json
import re
import subprocess

OPERATOR_PATHS = frozenset({
    "scripts/github_ci_autofix.py",
    "tests/test_github_ci_autofix.py",
    "deploy/systemd/gptadmin-ci-autofix.service",
    "deploy/systemd/gptadmin-ci-autofix.timer",
    "docs/GITHUB_CI_AUTOFIX.md",
    "scripts/classify_ci_paths.py",
    "tests/test_classify_ci_paths.py",
    ".github/workflows/operator-ci.yml",
    ".github/workflows/website-bump.yml",
})


def classify(paths):
    # Empty or undecodable changes must never exempt product validation.
    return bool(paths) and all(isinstance(path, str) and path in OPERATOR_PATHS for path in paths)


def changed_paths(base, head):
    if not all(re.fullmatch(r"[0-9a-fA-F]{40}", sha or "") and sha != "0" * 40 for sha in (base, head)):
        raise ValueError("Two valid nonzero commit SHAs are required")
    result = subprocess.run(["git", "diff", "--name-only", "--no-renames", "-z", base, head, "--"],
                            capture_output=True, timeout=30, check=True)
    if not result.stdout:
        return []
    if not result.stdout.endswith(b"\0"):
        raise ValueError("Malformed NUL-delimited Git diff")
    return [path.decode("utf-8", errors="strict") for path in result.stdout[:-1].split(b"\0")]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base", required=True)
    parser.add_argument("--head", required=True)
    args = parser.parse_args()
    try:
        paths = changed_paths(args.base, args.head)
        only = classify(paths)
        print(json.dumps({"operator_only": only, "product_required": not only, "paths": paths}))
        return 0
    except (ValueError, UnicodeError, subprocess.SubprocessError):
        print(json.dumps({"operator_only": False, "product_required": True,
                          "error": "Change classification failed; product exemption refused"}))
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
