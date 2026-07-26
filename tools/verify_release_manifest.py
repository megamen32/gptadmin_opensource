#!/usr/bin/env python3
"""Generate and verify the machine-readable GPTAdmin release manifest."""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import sys
from pathlib import Path
from typing import Any


SCHEMA = "gptadmin.release-manifest/v1"
ARCHIVE_RE = re.compile(r"^(?:gptadmin\.tar\.gz|gptadmin-[^/]+(?:\.tar\.gz|\.zip))$")
PLATFORM_RE = re.compile(r"^gptadmin-(?P<platform>linux|darwin|android)-(?P<arch>[^.]+)\.tar\.gz$")


def sha256_file(path: Path) -> str:
    """Return the SHA-256 digest of *path* without loading it all in memory."""

    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def artifact_identity(path: Path) -> tuple[str, str, str]:
    """Derive platform, architecture and artifact kind from an archive name."""

    match = PLATFORM_RE.match(path.name)
    if match:
        return match.group("platform"), match.group("arch"), "platform-archive"
    if path.name == "gptadmin-win.zip":
        return "windows", "amd64", "platform-archive"
    return "multi", "multi", "release-archive"


def archive_paths(root: Path, manifest: Path) -> list[Path]:
    """Find release archives under build/ and public/, excluding the manifest."""

    paths: list[Path] = []
    for directory in (root / "build", root / "public"):
        if not directory.is_dir():
            continue
        for path in directory.rglob("*"):
            if not path.is_file() or not ARCHIVE_RE.match(path.name):
                continue
            if path.resolve() == manifest.resolve():
                continue
            paths.append(path)
    return sorted(paths, key=lambda item: item.relative_to(root).as_posix())


def manifest_for(root: Path, manifest: Path, *, build_version: int, build_ts: str, git_commit: str) -> dict[str, Any]:
    """Build the canonical manifest payload for all release archives."""

    artifacts = []
    for path in archive_paths(root, manifest):
        platform, arch, artifact_type = artifact_identity(path)
        artifacts.append(
            {
                "path": path.relative_to(root).as_posix(),
                "size": path.stat().st_size,
                "sha256": sha256_file(path),
                "platform": platform,
                "arch": arch,
                "artifact_type": artifact_type,
            }
        )
    if not artifacts:
        raise ValueError("no release archives found under build/ or public/")
    sbom = root / "build" / "gptadmin-sbom.spdx.json"
    if not sbom.is_file():
        raise ValueError("release SBOM is missing: build/gptadmin-sbom.spdx.json")
    return {
        "schema": SCHEMA,
        "build_version": build_version,
        "build_ts": build_ts,
        "git_commit": git_commit,
        "sbom": {"path": sbom.relative_to(root).as_posix(), "size": sbom.stat().st_size, "sha256": sha256_file(sbom)},
        "artifacts": artifacts,
    }


def load_manifest(path: Path) -> dict[str, Any]:
    """Load a JSON manifest and require the top-level object contract."""

    try:
        payload = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise ValueError(f"cannot read manifest: {exc}") from exc
    if not isinstance(payload, dict):
        raise ValueError("manifest must be a JSON object")
    return payload


def verify_manifest(root: Path, manifest: Path) -> None:
    """Verify schema, provenance fields, paths, sizes and digests."""

    payload = load_manifest(manifest)
    if payload.get("schema") != SCHEMA:
        raise ValueError("unsupported release manifest schema")
    for field in ("build_version", "build_ts", "git_commit"):
        if field not in payload or payload[field] in (None, ""):
            raise ValueError(f"manifest field is missing: {field}")
    sbom = payload.get("sbom")
    if not isinstance(sbom, dict):
        raise ValueError("manifest SBOM record is missing")
    sbom_path = sbom.get("path")
    if not isinstance(sbom_path, str) or Path(sbom_path).is_absolute() or ".." in Path(sbom_path).parts:
        raise ValueError("manifest SBOM path is unsafe")
    sbom_file = root / sbom_path
    if not sbom_file.is_file():
        raise ValueError(f"SBOM is missing: {sbom_path}")
    if sbom.get("size") != sbom_file.stat().st_size or sbom.get("sha256") != sha256_file(sbom_file):
        raise ValueError("SBOM digest or size mismatch")
    artifacts = payload.get("artifacts")
    if not isinstance(artifacts, list) or not artifacts:
        raise ValueError("manifest artifacts must be a non-empty list")

    version_file = root / "VERSION"
    if version_file.exists():
        expected_version = version_file.read_text(encoding="utf-8").strip()
        if str(payload["build_version"]) != expected_version:
            raise ValueError("manifest build_version does not match VERSION")

    seen: set[str] = set()
    for item in artifacts:
        if not isinstance(item, dict):
            raise ValueError("manifest artifact entry must be an object")
        relative = item.get("path")
        if not isinstance(relative, str) or not relative or Path(relative).is_absolute() or ".." in Path(relative).parts:
            raise ValueError(f"unsafe artifact path: {relative!r}")
        if relative in seen:
            raise ValueError(f"duplicate artifact path: {relative}")
        seen.add(relative)
        artifact = root / relative
        if not artifact.is_file():
            raise ValueError(f"artifact is missing: {relative}")
        if item.get("size") != artifact.stat().st_size:
            raise ValueError(f"size mismatch: {relative}")
        actual_sha = sha256_file(artifact)
        if item.get("sha256") != actual_sha:
            raise ValueError(f"sha256 mismatch: {relative}")
        for field in ("platform", "arch", "artifact_type"):
            if not item.get(field):
                raise ValueError(f"artifact field is missing: {field}: {relative}")

    discovered = {path.relative_to(root).as_posix() for path in archive_paths(root, manifest)}
    missing = sorted(discovered - seen)
    if missing:
        raise ValueError(f"artifact is not listed in manifest: {missing[0]}")


def write_manifest(path: Path, payload: dict[str, Any]) -> None:
    """Atomically write a UTF-8 JSON manifest."""

    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(f".{path.name}.tmp")
    temporary.write_text(json.dumps(payload, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    temporary.replace(path)


def parse_args() -> argparse.Namespace:
    """Parse the small generate/verify command line interface."""

    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="command", required=True)
    for command in ("generate", "verify"):
        subparser = subparsers.add_parser(command)
        subparser.add_argument("--root", type=Path, required=True)
        subparser.add_argument("--manifest", type=Path, required=True)
        if command == "generate":
            subparser.add_argument("--build-version", type=int, required=True)
            subparser.add_argument("--build-ts", required=True)
            subparser.add_argument("--git-commit", required=True)
    return parser.parse_args()


def main() -> int:
    """Run the requested manifest operation and return a shell exit code."""

    args = parse_args()
    root = args.root.resolve()
    manifest = args.manifest.resolve()
    try:
        if args.command == "generate":
            write_manifest(
                manifest,
                manifest_for(
                    root,
                    manifest,
                    build_version=args.build_version,
                    build_ts=args.build_ts,
                    git_commit=args.git_commit,
                ),
            )
            print(f"generated release manifest: {manifest}")
        else:
            verify_manifest(root, manifest)
            print(f"verified release manifest: {manifest}")
    except (OSError, ValueError) as exc:
        print(f"release manifest error: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
