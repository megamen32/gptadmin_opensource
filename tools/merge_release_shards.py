#!/usr/bin/env python3
"""Validate eight same-source release shards and assemble the standard release."""

from __future__ import annotations

import argparse
from datetime import datetime, timezone
import json
from pathlib import Path
import re
import shutil
import subprocess
import sys
from typing import Any

from generate_sbom import build_sbom
from verify_release_manifest import (
    ARCHIVE_RE, SCHEMA as MANIFEST_SCHEMA, manifest_for, sha256_file,
    verify_manifest, write_manifest,
)

MATRIX_SCHEMA = "gptadmin.release-matrix/v2"
PLATFORMS = {
    f"{goos}_{goarch}": (platform, arch)
    for goos, platform in (("linux", "ubuntu"), ("darwin", "macos"),
                          ("windows", "windows"), ("android", "android"))
    for goarch, arch in (("amd64", "x64"), ("arm64", "arm64"))
}
METADATA = {
    "gptadmin-release-matrix.json", "gptadmin-checksums.txt",
    "manifest.json", "gptadmin-sbom.spdx.json",
}


def read_object(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise ValueError(f"cannot read {path}: {exc}") from exc
    if not isinstance(value, dict):
        raise ValueError(f"{path.name} must be an object")
    return value


def require_identity(value: dict[str, Any], source: str, version: int, label: str) -> None:
    if type(value.get("build_version")) is not int or value["build_version"] != version:
        raise ValueError(f"{label}: build_version does not match VERSION")
    if value.get("git_commit") != source:
        raise ValueError(f"{label}: git_commit does not match source")


def validate_source(root: Path, source: str, version: int) -> None:
    if not re.fullmatch(r"[0-9a-f]{40}", source):
        raise ValueError("source must be a full lowercase commit SHA")
    if (root / "VERSION").read_text(encoding="utf-8").strip() != str(version):
        raise ValueError("version does not match checkout VERSION")
    def revision(ref: str) -> str:
        return subprocess.run(
            ["git", "-C", str(root), "rev-parse", "--verify", ref],
            check=True, capture_output=True, text=True,
        ).stdout.strip()
    if revision("HEAD") != source:
        raise ValueError("source does not match checkout HEAD")
    if revision(f"refs/tags/v{version}^{{commit}}") != source:
        raise ValueError("release tag does not match checkout HEAD")


def manifest_entries(entries: list[dict[str, Any]]) -> list[dict[str, Any]]:
    return [
        {"path": f'build/{item["file"]}', "size": item["size"], "sha256": item["sha256"],
         "platform": item["platform"], "arch": item["arch"],
         "artifact_type": f'{item["edition"]}-bundle'}
        for item in entries
    ]


def validate_shard(directory: Path, source: str, version: int) -> tuple[str, list[dict[str, Any]]]:
    files = list(directory.iterdir())
    if any(path.is_symlink() or not path.is_file() for path in files):
        raise ValueError(f"{directory.name}: shard must contain only regular files")
    matrix = read_object(directory / "gptadmin-release-matrix.json")
    if matrix.get("schema") != MATRIX_SCHEMA:
        raise ValueError(f"{directory.name}: unsupported matrix schema")
    require_identity(matrix, source, version, directory.name)
    pair = matrix.get("build_platform")
    if not isinstance(pair, str) or pair not in PLATFORMS:
        raise ValueError(f"{directory.name}: unknown build_platform")
    platform, arch = PLATFORMS[pair]
    extension = "zip" if platform == "windows" else "tar.gz"
    expected = {f"gptadmin-{platform}-{arch}-{edition}.{extension}" for edition in ("full", "client")}
    if {path.name for path in files} != expected | METADATA:
        raise ValueError(f"{directory.name}: missing or extra shard files")
    artifacts = matrix.get("artifacts")
    if not isinstance(artifacts, list) or len(artifacts) != 2:
        raise ValueError(f"{directory.name}: partial matrix must contain exactly two artifacts")
    seen: set[str] = set()
    entries = []
    for artifact in artifacts:
        if not isinstance(artifact, dict):
            raise ValueError(f"{directory.name}: invalid artifact record")
        name = artifact.get("file")
        if not isinstance(name, str) or name not in expected or name in seen:
            raise ValueError(f"{directory.name}: unexpected or duplicate artifact name")
        seen.add(name)
        edition = "client" if "-client." in name else "full"
        if (artifact.get("platform"), artifact.get("arch"), artifact.get("edition")) != (platform, arch, edition):
            raise ValueError(f"{directory.name}: artifact does not match its platform pair")
        path = directory / name
        size = artifact.get("size")
        digest = artifact.get("sha256")
        if type(size) is not int or size <= 0 or size != path.stat().st_size:
            raise ValueError(f"{name}: size mismatch")
        if not isinstance(digest, str) or not re.fullmatch(r"[0-9a-f]{64}", digest) or digest != sha256_file(path):
            raise ValueError(f"{name}: sha256 mismatch")
        entries.append({"platform": platform, "arch": arch, "edition": edition,
                        "file": name, "sha256": digest, "size": size})

    entries.sort(key=lambda item: item["file"])
    checksums = [f'{item["sha256"]}  build/{item["file"]}' for item in entries]
    if (directory / "gptadmin-checksums.txt").read_text(encoding="utf-8").splitlines() != checksums:
        raise ValueError(f"{directory.name}: checksums do not match partial matrix")
    manifest = read_object(directory / "manifest.json")
    if manifest.get("schema") != MANIFEST_SCHEMA:
        raise ValueError(f"{directory.name}: unsupported manifest schema")
    require_identity(manifest, source, version, "shard manifest")
    if manifest.get("artifacts") != manifest_entries(entries):
        raise ValueError(f"{directory.name}: manifest does not match partial matrix")
    sbom_path = directory / "gptadmin-sbom.spdx.json"
    expected_sbom = {"path": "build/gptadmin-sbom.spdx.json",
                     "size": sbom_path.stat().st_size, "sha256": sha256_file(sbom_path)}
    if manifest.get("sbom") != expected_sbom:
        raise ValueError(f"{directory.name}: SBOM hash or size mismatch")
    sbom_build = read_object(sbom_path).get("gptadminBuild")
    if not isinstance(sbom_build, dict):
        raise ValueError(f"{directory.name}: missing SBOM build identity")
    require_identity(sbom_build, source, version, "shard SBOM")
    if not manifest.get("build_ts") or sbom_build.get("build_ts") != manifest["build_ts"]:
        raise ValueError(f"{directory.name}: SBOM timestamp does not match manifest")
    return pair, entries


def merge_release_shards(root: Path, shards_dir: Path, output: Path, source: str, version: int) -> None:
    root = root.resolve()
    validate_source(root, source, version)
    if output.is_symlink() or output.resolve() != root / "build":
        raise ValueError("output must be this checkout's build directory")
    if output.exists() and (not output.is_dir() or any(output.iterdir())):
        raise ValueError("output must be empty; refusing stale release files")
    if shards_dir.is_symlink() or not shards_dir.is_dir():
        raise ValueError("shards-dir must be a regular directory")
    directories = sorted(shards_dir.iterdir())
    if len(directories) != len(PLATFORMS) or any(path.is_symlink() or not path.is_dir() for path in directories):
        raise ValueError("exactly eight distinct shard directories are required")
    pairs: set[str] = set()
    assets: list[tuple[Path, dict[str, Any]]] = []
    for directory in directories:
        pair, entries = validate_shard(directory, source, version)
        if pair in pairs:
            raise ValueError(f"duplicate platform shard: {pair}")
        pairs.add(pair)
        assets.extend((directory / item["file"], item) for item in entries)
    if pairs != set(PLATFORMS):
        raise ValueError("release platform matrix is incomplete")
    assets.sort(key=lambda entry: entry[1]["file"])

    # Match tagged build preflight: generated legacy public archives are not
    # source inputs and must never enter the final provenance manifest.
    for path in (root / "public").rglob("*"):
        if path.is_file() and ARCHIVE_RE.fullmatch(path.name):
            path.unlink()
    output.mkdir(parents=True, exist_ok=True)
    for path, item in assets:
        shutil.copy2(path, output / item["file"])
    matrix = {"schema": MATRIX_SCHEMA, "build_version": version, "git_commit": source,
              "artifacts": [item for _, item in assets]}
    write_manifest(output / "gptadmin-release-matrix.json", matrix)
    (output / "gptadmin-checksums.txt").write_text(
        "".join(f'{item["sha256"]}  build/{item["file"]}\n' for _, item in assets),
        encoding="utf-8",
    )
    build_ts = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    identity = {"build_version": version, "build_ts": build_ts, "git_commit": source}
    write_manifest(output / "gptadmin-sbom.spdx.json", build_sbom(root, **identity))
    manifest = output / "manifest.json"
    payload = manifest_for(root, manifest, **identity)
    if payload["artifacts"] != manifest_entries(matrix["artifacts"]):
        raise ValueError("copied release files do not match verified shard hashes")
    write_manifest(manifest, payload)
    verify_manifest(root, manifest)
    print(f"verified release matrix: {len(pairs)} platforms, {len(assets)} assets, source {source}")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--shards-dir", type=Path, required=True)
    parser.add_argument("--output", type=Path, default=Path("build"))
    parser.add_argument("--source", required=True)
    parser.add_argument("--version", type=int, required=True)
    args = parser.parse_args()
    try:
        merge_release_shards(Path(__file__).resolve().parents[1], args.shards_dir,
                             args.output, args.source, args.version)
    except (OSError, ValueError, subprocess.CalledProcessError) as exc:
        print(f"merge release shards: {exc}", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
