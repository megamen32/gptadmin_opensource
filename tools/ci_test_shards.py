#!/usr/bin/env python3
"""Partition the complete collected test inventory; every item belongs to one shard."""
from __future__ import annotations
import argparse
import hashlib
import json
from pathlib import Path
import re
import subprocess
import sys


def shard_for(name: str, count: int) -> int:
    if count < 1:
        raise ValueError("shard count must be positive")
    return int.from_bytes(hashlib.sha256(name.encode()).digest()[:8], "big") % count


def selected_names(names, index, count):
    if not 0 <= index < count:
        raise ValueError("shard index must be in [0, count)")
    return sorted(name for name in set(names) if shard_for(name, count) == index)


class PytestShard:
    def __init__(self, index, count):
        self.index, self.count = index, count

    def pytest_collection_modifyitems(self, config, items):
        # Keep a whole file together so module-scoped fixtures retain their normal
        # lifetime. The ordinary ignore/deselect rules still run in pytest.
        keep, other = [], []
        for item in items:
            (keep if shard_for(item.nodeid.split("::", 1)[0], self.count) == self.index else other).append(item)
        config.hook.pytest_deselected(items=other)
        items[:] = keep


def run_go(module: str, index: int, count: int) -> None:
    listed = subprocess.run(["go", "test", "-list", ".", "./..."], cwd=module,
                            check=True, text=True, stdout=subprocess.PIPE)
    # Go's inventory includes top-level tests, fuzz seed tests and examples.
    # Names are hashed without the package, so duplicate names in different
    # packages still have exactly one owning shard.
    names = [line for line in listed.stdout.splitlines()
             if re.fullmatch(r"(?:Test|Fuzz|Example)\w*", line)]
    selected = selected_names(names, index, count)
    pattern = "^(" + "|".join(selected) + ")$" if selected else "^$"
    print(json.dumps({"module": module, "shard": index, "shards": count,
                      "collected_names": len(set(names)), "selected_names": len(selected)}),
          flush=True)
    subprocess.run(["go", "test", "-count=1", "-run", pattern, "./..."],
                   cwd=module, check=True)


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("kind", choices=("go", "pytest"))
    parser.add_argument("--index", type=int, required=True)
    parser.add_argument("--count", type=int, required=True)
    parser.add_argument("--module", action="append", default=[])
    options, remaining = parser.parse_known_args(argv)
    if options.count < 1 or not 0 <= options.index < options.count:
        parser.error("require 0 <= index < count")
    if remaining[:1] == ["--"]:
        remaining = remaining[1:]
    if options.kind == "pytest":
        import pytest
        return int(pytest.main(remaining, plugins=[PytestShard(options.index, options.count)]))
    if remaining or not options.module:
        parser.error("Go shards require --module and accept no extra arguments")
    for module in options.module:
        run_go(module, options.index, options.count)
    return 0


if __name__ == "__main__":
    sys.exit(main())
