#!/usr/bin/env python3
"""Verify the ShellMCP binaries and provenance in both macOS release bundles."""

from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import re
import struct
import tarfile


CPU_TYPES = {"x64": 0x01000007, "arm64": 0x0100000C}


def macho_arch(data: bytes) -> int:
    """Return the CPU type from a thin 32/64-bit Mach-O header."""
    if len(data) < 8:
        raise ValueError("ShellMCP binary is too short to be Mach-O")
    magic = data[:4]
    byte_order = {
        b"\xcf\xfa\xed\xfe": "<",  # MH_MAGIC_64
        b"\xfe\xed\xfa\xcf": ">",
        b"\xce\xfa\xed\xfe": "<",  # MH_MAGIC
        b"\xfe\xed\xfa\xce": ">",
    }.get(magic)
    if byte_order is None:
        raise ValueError("ShellMCP binary is not a thin Mach-O executable")
    return struct.unpack(byte_order + "I", data[4:8])[0]


def verify(build_dir: Path, minimum_build: int) -> dict:
    matrix = json.loads((build_dir / "gptadmin-release-matrix.json").read_text())
    if matrix.get("schema") != "gptadmin.release-matrix/v2":
        raise ValueError("unsupported release matrix schema")
    version = matrix.get("build_version")
    commit = matrix.get("git_commit")
    if type(version) is not int or version < minimum_build:
        raise ValueError(f"release build {version!r} is below required build {minimum_build}")
    if not isinstance(commit, str) or not re.fullmatch(r"[0-9a-f]{40}", commit):
        raise ValueError("release matrix has no full source commit")

    entries = {item.get("file"): item for item in matrix.get("artifacts", [])}
    checksums = {}
    for line in (build_dir / "gptadmin-checksums.txt").read_text().splitlines():
        parts = line.split()
        if len(parts) == 2:
            name = parts[1].lstrip("*")
            if name.startswith("build/"):
                name = name[len("build/"):]
            if "/" in name or name in checksums:
                raise ValueError("unsafe or duplicate checksum entry")
            checksums[name] = parts[0].lower()

    verified = []
    for arch, cpu_type in CPU_TYPES.items():
        filename = f"gptadmin-macos-{arch}-client.tar.gz"
        path = build_dir / filename
        entry = entries.get(filename)
        if not path.is_file() or not entry:
            raise ValueError(f"missing macOS {arch} client package or release-matrix entry")
        digest = hashlib.sha256(path.read_bytes()).hexdigest()
        if checksums.get(filename) != digest:
            raise ValueError(f"checksum mismatch for {filename}")
        if entry.get("sha256") != digest or entry.get("size") != path.stat().st_size:
            raise ValueError(f"release matrix digest or size mismatch for {filename}")
        if (entry.get("platform"), entry.get("arch"), entry.get("edition")) != ("macos", arch, "client"):
            raise ValueError(f"release matrix target mismatch for {filename}")

        with tarfile.open(path, "r:gz") as archive:
            members = [member for member in archive.getmembers() if member.name in ("bin/shellmcp", "./bin/shellmcp")]
            if len(members) != 1 or not members[0].isfile() or not (members[0].mode & 0o111):
                raise ValueError(f"{filename} must contain one executable bin/shellmcp")
            stream = archive.extractfile(members[0])
            if stream is None or macho_arch(stream.read(8)) != cpu_type:
                raise ValueError(f"bin/shellmcp in {filename} has the wrong Mach-O architecture")
            readmes = [m for m in archive.getmembers() if m.name in ("README.txt", "./README.txt")]
            if len(readmes) != 1:
                raise ValueError(f"{filename} must contain one README.txt")
            readme = archive.extractfile(readmes[0])
            expected = f"GPTAdmin {version} for macos/{arch} (client)\n"
            if readme is None or readme.read().decode("utf-8") != expected:
                raise ValueError(f"build metadata in {filename} does not match release matrix")
        verified.append({"file": filename, "build_version": version, "git_commit": commit, "sha256": digest})
    return {"verified": verified}


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--build-dir", type=Path, required=True)
    parser.add_argument("--minimum-build", type=int, default=1074)
    args = parser.parse_args()
    print(json.dumps(verify(args.build_dir, args.minimum_build), indent=2))


if __name__ == "__main__":
    main()
