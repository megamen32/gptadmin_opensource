#!/usr/bin/env python3
"""Invoke the real offline handoff implementation with an exact binary pin.

The previous descriptor-only phase commands have intentionally been removed.
This wrapper does not claim that a file is an admission fence. The service
manager must stop and fence the real unit; the Go implementation additionally
holds its actual auth runtime lock and performs canonical state/CAS operations.
"""

import argparse
import hashlib
import os
import pathlib
import stat
import sys


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=pathlib.Path)
    parser.add_argument("--binary-sha256", required=True)
    parser.add_argument("arguments", nargs=argparse.REMAINDER)
    options = parser.parse_args()
    binary = options.binary
    info = binary.lstat()
    if not binary.is_absolute() or not stat.S_ISREG(info.st_mode):
        parser.error("binary must be an absolute regular file")
    if info.st_mode & (stat.S_IWGRP | stat.S_IWOTH):
        parser.error("binary must not be group/world writable")
    actual = hashlib.sha256(binary.read_bytes()).hexdigest()
    if actual != options.binary_sha256:
        parser.error("binary digest differs from the reviewed build")
    arguments = options.arguments
    if arguments and arguments[0] == "--":
        arguments = arguments[1:]
    if not arguments:
        parser.error("real authority-handoff action is required after --")
    os.execve(str(binary), [str(binary), *arguments], os.environ)


if __name__ == "__main__":
    try:
        main()
    except OSError as error:
        print(f"authority handoff launcher: {error}", file=sys.stderr)
        raise SystemExit(1)
