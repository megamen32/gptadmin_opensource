#!/usr/bin/env python3
"""Consume pinned GrepMesh releases; its own public repository owns compilation."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import posixpath
import shutil
import socket
import subprocess
import tarfile
import tempfile
import time
import urllib.request
import zipfile

ROOT = Path(__file__).resolve().parent.parent
LOCK = ROOT / "tools" / "grepmesh-release.json"


def verify_archive(path: Path, expected: str) -> None:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    if digest.hexdigest() != expected:
        raise ValueError(f"SHA-256 mismatch for GrepMesh archive: {path}")


def _safe_path(name: str) -> PurePosixPath:
    path = PurePosixPath(name)
    if path.is_absolute() or ".." in path.parts or "\\" in name:
        raise ValueError(f"unsafe archive path: {name}")
    return path


def _runtime_member(path: PurePosixPath) -> bool:
    return str(path) in {
        "grepmesh-mcp", "grepmesh-mcp.exe", "rg", "rg.exe",
        "onnxruntime-manifest.txt",
    } or (path.parts and path.parts[0] in {"lib", "licenses"})


def extract_runtime(archive: Path, destination: Path) -> None:
    """Copy runtime files only; flatten archive links without following disk links."""
    destination.mkdir(parents=True, exist_ok=True)
    if archive.suffix == ".zip":
        with zipfile.ZipFile(archive) as source:
            members = [(_safe_path(entry.filename), entry) for entry in source.infolist()]
            for relative, entry in members:
                if entry.is_dir() or not _runtime_member(relative):
                    continue
                target = destination / relative
                target.parent.mkdir(parents=True, exist_ok=True)
                with source.open(entry) as src, target.open("wb") as dst:
                    shutil.copyfileobj(src, dst)
                target.chmod(0o755 if len(relative.parts) == 1 else 0o644)
    else:
        with tarfile.open(archive) as source:
            members = [(_safe_path(entry.name), entry) for entry in source.getmembers()]
            for relative, entry in members:
                if entry.issym() or entry.islnk():
                    link = entry.linkname if entry.islnk() else str(relative.parent / entry.linkname)
                    _safe_path(posixpath.normpath(link))
                elif not entry.isfile() and not entry.isdir():
                    raise ValueError(f"unsafe archive member type: {entry.name}")
                if entry.isdir() or not _runtime_member(relative):
                    continue
                target = destination / relative
                target.parent.mkdir(parents=True, exist_ok=True)
                stream = source.extractfile(entry)
                if stream is None:
                    raise ValueError(f"archive runtime entry has no bytes: {entry.name}")
                with stream, target.open("wb") as dst:
                    shutil.copyfileobj(stream, dst)
                target.chmod((entry.mode & 0o555) | 0o600)
    binary = "grepmesh-mcp.exe" if archive.suffix == ".zip" else "grepmesh-mcp"
    rg = "rg.exe" if archive.suffix == ".zip" else "rg"
    for name in (binary, rg):
        if not (destination / name).is_file():
            raise ValueError(f"verified GrepMesh archive is missing {name}")


def download_release(lock: dict, platform: str, cache: Path) -> tuple[Path, dict]:
    try:
        asset = lock["platforms"][platform]
    except KeyError as error:
        raise ValueError(
            f"No pinned GrepMesh release for {platform}; publish that platform upstream first"
        ) from error
    archive = cache / lock["tag"] / asset["name"]
    archive.parent.mkdir(parents=True, exist_ok=True)
    if not archive.exists():
        url = f'https://github.com/{lock["repository"]}/releases/download/{lock["tag"]}/{asset["name"]}'
        request = urllib.request.Request(url, headers={"User-Agent": "GPTAdmin-release-builder"})
        with tempfile.NamedTemporaryFile(dir=archive.parent, delete=False) as temporary:
            temporary_path = Path(temporary.name)
            try:
                with urllib.request.urlopen(request, timeout=60) as response:
                    shutil.copyfileobj(response, temporary)
                temporary.flush()
                verify_archive(temporary_path, asset["sha256"])
                temporary_path.replace(archive)
            finally:
                temporary_path.unlink(missing_ok=True)
    verify_archive(archive, asset["sha256"])
    return archive, asset


def verify_native_runtime(runtime: Path, expected_version: str) -> None:
    """Run the real packaged MCP server and search a real local canary file."""
    def rpc(url: str, method: str, params: dict) -> dict:
        request = urllib.request.Request(
            url, json.dumps({"jsonrpc": "2.0", "id": 1, "method": method, "params": params}).encode(),
            {"Content-Type": "application/json"},
        )
        with urllib.request.urlopen(request, timeout=2) as response:
            return json.load(response)

    with tempfile.TemporaryDirectory(prefix="grepmesh-smoke-", dir=runtime.parent) as temp:
        root = Path(temp).resolve()
        canary = root / "files"
        canary.mkdir()
        (canary / "proof.txt").write_text("GPTAdmin pinned companion canary\n")
        with socket.socket() as reservation:
            reservation.bind(("127.0.0.1", 0))
            port = reservation.getsockname()[1]
        config = {
            "host_id": "gptadmin-build-smoke", "bind": f"127.0.0.1:{port}",
            "root": str(canary), "roots": {"canary": [str(canary)]}, "peers": [],
            "ocr": {"enabled": False}, "stt": {"enabled": False},
            "userio": {"enabled": False},
        }
        config_path = root / "config.json"
        config_path.write_text(json.dumps(config))
        environment = dict(os.environ, PATH=str(runtime.resolve()) + os.pathsep + os.environ.get("PATH", ""))
        with (root / "runtime.log").open("w+") as log:
            process = subprocess.Popen(
                [str(runtime.resolve() / "grepmesh-mcp"), "--config", str(config_path)],
                cwd=root, env=environment, stdout=log, stderr=log,
            )
            try:
                url = f"http://127.0.0.1:{port}/mcp"
                deadline = time.monotonic() + 12
                initialized = None
                while time.monotonic() < deadline and process.poll() is None:
                    try:
                        initialized = rpc(url, "initialize", {
                            "protocolVersion": "2025-06-18", "capabilities": {},
                            "clientInfo": {"name": "gptadmin-build-smoke", "version": "1"},
                        })
                        break
                    except (OSError, ValueError):
                        time.sleep(0.1)
                if initialized is None:
                    log.seek(0)
                    raise RuntimeError(f"pinned GrepMesh failed to start: {log.read()[-2000:]}")
                actual = initialized.get("result", {}).get("serverInfo", {})
                if actual.get("name") != "grepmesh" or actual.get("version") != expected_version:
                    raise RuntimeError(f"unexpected GrepMesh identity/version: {actual}")
                searched = rpc(url, "tools/call", {"name": "search_text", "arguments": {
                    "query": "GPTAdmin pinned companion canary", "hosts": "local",
                }})
                result = searched.get("result", {})
                if searched.get("error") or result.get("isError"):
                    raise RuntimeError(f"pinned GrepMesh search failed: {searched}")
                payload = json.loads(result["content"][0]["text"])
                if not payload.get("results"):
                    raise RuntimeError(f"pinned GrepMesh did not return the real canary: {payload}")
            finally:
                process.terminate()
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()
    print(f"GrepMesh {expected_version}: packaged MCP initialization and local search passed")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--platform", required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--cache", type=Path, default=ROOT / ".buildcache" / "grepmesh")
    parser.add_argument("--verify-native", action="store_true")
    args = parser.parse_args()
    lock = json.loads(LOCK.read_text())
    archive, asset = download_release(lock, args.platform, args.cache)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="grepmesh-stage-", dir=args.output.parent) as temp:
        stage = Path(temp) / "runtime"
        extract_runtime(archive, stage)
        (stage / "grepmesh-release.json").write_text(json.dumps({
            "repository": lock["repository"], "tag": lock["tag"],
            "source_commit": lock["source_commit"], "platform": args.platform,
            "archive": asset["name"], "archive_sha256": asset["sha256"],
        }, indent=2) + "\n")
        if args.verify_native:
            verify_native_runtime(stage, lock["version"])
        if args.output.exists():
            shutil.rmtree(args.output)
        shutil.move(str(stage), args.output)
    print(f'Bundled {lock["repository"]}@{lock["tag"]} ({args.platform}, SHA-256 verified)')
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
