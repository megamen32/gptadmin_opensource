"""Local release-shard integrity and command wiring; runtime gates remain in CI."""

from __future__ import annotations

import hashlib
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import zipfile

import pytest

ROOT = Path(__file__).resolve().parents[1]
PAIRS = {
    "linux_amd64": ("ubuntu", "x64"), "linux_arm64": ("ubuntu", "arm64"),
    "darwin_amd64": ("macos", "x64"), "darwin_arm64": ("macos", "arm64"),
    "windows_amd64": ("windows", "x64"), "windows_arm64": ("windows", "arm64"),
    "android_amd64": ("android", "x64"), "android_arm64": ("android", "arm64"),
}


def _write_json(path, value):
    path.write_text(json.dumps(value, indent=2) + "\n")


def _read_json(path):
    return json.loads(path.read_text())


def _git(repo, *args):
    return subprocess.run(["git", "-C", str(repo), *args], check=True,
                          capture_output=True, text=True).stdout.strip()


@pytest.fixture
def release_tree(tmp_path):
    repo = tmp_path / "repo"
    (repo / "tools").mkdir(parents=True)
    for name in ("merge_release_shards.py", "generate_sbom.py", "verify_release_manifest.py"):
        shutil.copy2(ROOT / "tools" / name, repo / "tools" / name)
    (repo / "VERSION").write_text("129\n")
    (repo / "public").mkdir()
    (repo / "public/gptadmin-win.zip").write_bytes(b"old generated release")
    (repo / "public/unrelated.txt").write_text("source payload")
    _git(repo, "init")
    _git(repo, "add", ".")
    _git(repo, "-c", "user.name=Release Test", "-c", "user.email=release@example.invalid",
         "commit", "-m", "release fixture")
    source = _git(repo, "rev-parse", "HEAD")
    _git(repo, "tag", "v129")
    shards = repo / ".tmp/shards"
    identity = {"build_version": 129, "git_commit": source, "build_ts": "2026-11-26T00:00:00Z"}
    for pair, (platform, arch) in PAIRS.items():
        directory = shards / pair
        directory.mkdir(parents=True)
        entries = []
        for edition in ("client", "full"):
            extension = "zip" if platform == "windows" else "tar.gz"
            name = f"gptadmin-{platform}-{arch}-{edition}.{extension}"
            path = directory / name
            data = f"archive-integrity fixture {pair} {edition}\n".encode()
            if extension == "zip":
                with zipfile.ZipFile(path, "w") as archive:
                    archive.writestr("README.txt", data)
            else:
                with tarfile.open(path, "w:gz") as archive:
                    member = tarfile.TarInfo("README.txt")
                    member.size = len(data)
                    archive.addfile(member, io.BytesIO(data))
            entries.append({"platform": platform, "arch": arch, "edition": edition,
                            "file": name, "sha256": hashlib.sha256(path.read_bytes()).hexdigest(),
                            "size": path.stat().st_size})
        _write_json(directory / "gptadmin-release-matrix.json", {
            "schema": "gptadmin.release-matrix/v2", "build_version": 129,
            "git_commit": source, "build_platform": pair, "artifacts": entries,
        })
        (directory / "gptadmin-checksums.txt").write_text(
            "".join(f'{entry["sha256"]}  build/{entry["file"]}\n' for entry in entries))
        sbom = directory / "gptadmin-sbom.spdx.json"
        _write_json(sbom, {"spdxVersion": "SPDX-2.3", "gptadminBuild": identity})
        _write_json(directory / "manifest.json", {
            "schema": "gptadmin.release-manifest/v1", **identity,
            "sbom": {"path": "build/gptadmin-sbom.spdx.json", "size": sbom.stat().st_size,
                     "sha256": hashlib.sha256(sbom.read_bytes()).hexdigest()},
            "artifacts": [
                {"path": f'build/{entry["file"]}', "size": entry["size"], "sha256": entry["sha256"],
                 "platform": platform, "arch": arch, "artifact_type": f'{entry["edition"]}-bundle'}
                for entry in entries
            ],
        })
    return repo, shards, source


def _merge(tree, *extra):
    repo, shards, source = tree
    return subprocess.run(
        [sys.executable, "tools/merge_release_shards.py", "--shards-dir", str(shards),
         "--output", "build", "--source", source, "--version", "129", *extra],
        cwd=repo, capture_output=True, text=True, timeout=10,
    )


def test_merge_requires_all_eight_and_generates_standard_verified_metadata(release_tree):
    repo, _, source = release_tree
    result = _merge(release_tree)
    assert result.returncode == 0, result.stdout + result.stderr
    matrix = _read_json(repo / "build/gptadmin-release-matrix.json")
    assert matrix["build_version"] == 129 and matrix["git_commit"] == source
    assert "build_platform" not in matrix
    assert len(matrix["artifacts"]) == 16
    assert {(entry["platform"], entry["arch"], entry["edition"]) for entry in matrix["artifacts"]} == {
        (platform, arch, edition) for platform, arch in PAIRS.values() for edition in ("client", "full")}
    assert len(list((repo / "build").iterdir())) == 20
    assert not (repo / "public/gptadmin-win.zip").exists()
    assert (repo / "public/unrelated.txt").read_text() == "source payload"
    manifest = _read_json(repo / "build/manifest.json")
    sbom = _read_json(repo / "build/gptadmin-sbom.spdx.json")
    assert manifest["git_commit"] == sbom["gptadminBuild"]["git_commit"] == source
    assert manifest["build_version"] == sbom["gptadminBuild"]["build_version"] == 129
    verified = subprocess.run(
        [sys.executable, "tools/verify_release_manifest.py", "verify", "--root", ".",
         "--manifest", "build/manifest.json"], cwd=repo, capture_output=True, text=True)
    assert verified.returncode == 0, verified.stderr


@pytest.mark.parametrize("fault", ["missing", "extra", "duplicate", "file_instead_of_directory"])
def test_merge_rejects_incomplete_or_duplicate_platforms(release_tree, fault):
    _, shards, _ = release_tree
    if fault in ("missing", "duplicate"):
        shutil.rmtree(shards / "linux_arm64")
    if fault in ("extra", "duplicate"):
        shutil.copytree(shards / "linux_amd64", shards / "another")
    if fault == "file_instead_of_directory":
        shutil.rmtree(shards / "linux_arm64")
        (shards / "linux_arm64").write_text("not a shard")
    result = _merge(release_tree)
    assert result.returncode == 2
    assert not (release_tree[0] / "build").exists()


@pytest.mark.parametrize("fault", [
    "source", "version", "platform", "wrong_pair", "duplicate_artifact", "traversal",
    "edition", "sha256", "size", "boolean_size", "bytes", "missing_file", "extra_file",
    "nested_directory", "symlink", "checksums", "manifest", "sbom",
])
def test_merge_rejects_corrupt_or_unbound_shards_before_copying(release_tree, fault):
    repo, shards, _ = release_tree
    directory = shards / "linux_amd64"
    matrix_path = directory / "gptadmin-release-matrix.json"
    matrix = _read_json(matrix_path)
    entry = matrix["artifacts"][0]
    artifact = directory / entry["file"]
    if fault == "source":
        matrix["git_commit"] = "0" * 40
    elif fault == "version":
        matrix["build_version"] += 1
    elif fault == "platform":
        matrix["build_platform"] = "freebsd_amd64"
    elif fault == "wrong_pair":
        matrix["build_platform"] = "linux_arm64"
    elif fault == "duplicate_artifact":
        matrix["artifacts"][1] = dict(entry)
    elif fault == "traversal":
        entry["file"] = "../" + entry["file"]
    elif fault == "edition":
        entry["edition"] = "full"
    elif fault == "sha256":
        entry["sha256"] = "0" * 64
    elif fault == "size":
        entry["size"] += 1
    elif fault == "boolean_size":
        entry["size"] = True
    elif fault == "bytes":
        data = artifact.read_bytes()
        artifact.write_bytes(bytes([data[0] ^ 1]) + data[1:])
    elif fault == "missing_file":
        artifact.unlink()
    elif fault == "extra_file":
        (directory / "stale.tar.gz").write_bytes(b"extra")
    elif fault == "nested_directory":
        (directory / "nested").mkdir()
    elif fault == "symlink":
        target = repo / "outside.tar.gz"
        shutil.move(artifact, target)
        artifact.symlink_to(target)
    elif fault == "checksums":
        (directory / "gptadmin-checksums.txt").write_text("unrelated checksums\n")
    elif fault == "manifest":
        manifest = _read_json(directory / "manifest.json")
        manifest["git_commit"] = "0" * 40
        _write_json(directory / "manifest.json", manifest)
    elif fault == "sbom":
        sbom = directory / "gptadmin-sbom.spdx.json"
        value = _read_json(sbom)
        value["gptadminBuild"]["git_commit"] = "0" * 40
        _write_json(sbom, value)
        manifest = _read_json(directory / "manifest.json")
        manifest["sbom"].update(size=sbom.stat().st_size, sha256=hashlib.sha256(sbom.read_bytes()).hexdigest())
        _write_json(directory / "manifest.json", manifest)
    _write_json(matrix_path, matrix)
    result = _merge(release_tree)
    assert result.returncode == 2, result.stdout
    assert not (repo / "build").exists()
    assert (repo / "public/gptadmin-win.zip").read_bytes() == b"old generated release"


@pytest.mark.parametrize("fault", ["source", "version", "tag", "stale_output", "outside_output"])
def test_merge_binds_checkout_and_rejects_existing_output(release_tree, fault):
    repo, _, _ = release_tree
    extra = []
    if fault == "source":
        extra = ["--source", "0" * 40]
    elif fault == "version":
        extra = ["--version", "130"]
    elif fault == "tag":
        _git(repo, "tag", "-d", "v129")
    elif fault == "stale_output":
        (repo / "build").mkdir()
        (repo / "build/stale.zip").write_bytes(b"keep me")
    elif fault == "outside_output":
        extra = ["--output", str(repo.parent / "outside")]
    result = _merge(release_tree, *extra)
    assert result.returncode == 2
    if fault == "stale_output":
        assert list((repo / "build").iterdir()) == [repo / "build/stale.zip"]
        assert (repo / "build/stale.zip").read_bytes() == b"keep me"
    else:
        assert not (repo / "build").exists()


@pytest.mark.parametrize("arguments", [
    ["release-platform"], ["release-platform", "--platform", "linux_mips"],
    ["release-platform", "--platform", "linux_amd64", "release"],
    ["release-platform", "--platform", "linux_amd64"],
])
def test_release_platform_rejects_ambiguous_or_untagged_invocation(tmp_path, arguments):
    (tmp_path / "tools").mkdir()
    shutil.copy2(ROOT / "tools/build.sh", tmp_path / "tools/build.sh")
    (tmp_path / "VERSION").write_text("129\n")
    result = subprocess.run(
        ["bash", "tools/build.sh", *arguments], cwd=tmp_path,
        env=dict(os.environ, TAGGED_RELEASE="0"), capture_output=True, text=True, timeout=5)
    assert result.returncode == 2
    assert (tmp_path / "VERSION").read_text() == "129\n"
    assert not (tmp_path / "build").exists()


@pytest.mark.parametrize("pair", [*PAIRS, "matrix"])
def test_go_command_wiring_keeps_one_pair_or_the_complete_matrix(tmp_path, pair):
    """Only validate shell argument wiring with a recorder, not compiled artifacts."""
    source = (ROOT / "tools/build.sh").read_text()
    def function(name):
        return name + "() {\n" + source.split(name + "() {\n", 1)[1].split("\n}\n", 1)[0] + "\n}\n"
    for directory in ("go-hub", "go-shellmcp", "bin"):
        (tmp_path / directory).mkdir()
    capture = tmp_path / "arguments.jsonl"
    recorder = tmp_path / "bin/go"
    recorder.write_text(f"#!{sys.executable}\n" + """import json, os, pathlib, sys
with open(os.environ["CAPTURE"], "a") as stream:
    stream.write(json.dumps({"os": os.environ["GOOS"], "arch": os.environ["GOARCH"], "args": sys.argv[1:]}) + "\\n")
destination = pathlib.Path(sys.argv[sys.argv.index("-o") + 1])
destination.parent.mkdir(parents=True, exist_ok=True)
destination.write_text("argument-wiring fixture only")
destination.chmod(0o755)
""")
    recorder.chmod(0o755)
    compiler = tmp_path / "bin/android-clang"
    compiler.write_text("#!/bin/sh\nexit 71\n")
    compiler.chmod(0o755)
    script = """set -euo pipefail
ART_DIR=build
BUILD_VERSION=129
GIT_COMMIT=test-source
GPTADMIN_BUILD_PLATFORMS=default
BUILT_HUB_PLATFORMS=" "
BUILT_SHELLMCP_PLATFORMS=" "
GO_HUB_LDFLAGS=(-trimpath -buildvcs=false -ldflags hub-version)
GO_SHELLMCP_LDFLAGS=(-trimpath -buildvcs=false -ldflags shell-version)
step(){ :; }
"""
    script += function("build_android_arm64_client")
    script += function("build_hub_cross_platforms") + function("build_go_shellmcp_cross_platforms")
    mode = "matrix" if pair == "matrix" else "release-platform"
    script += f"build_hub_cross_platforms {mode}\nbuild_go_shellmcp_cross_platforms {mode}\n"
    env = dict(os.environ, PATH=str(tmp_path / "bin") + os.pathsep + os.environ["PATH"],
               CAPTURE=str(capture), RELEASE_BUILD_PAIR="" if pair == "matrix" else pair,
               ANDROID_X86_64_CC=str(compiler), ANDROID_ARM64_CC=str(compiler))
    result = subprocess.run(["bash", "-c", script], cwd=tmp_path, env=env,
                            capture_output=True, text=True, timeout=10)
    assert result.returncode == 0, result.stdout + result.stderr
    calls = [json.loads(line) for line in capture.read_text().splitlines()]
    expected = list(PAIRS) if pair == "matrix" else [pair]
    commands = {
        "./cmd/gptadmin-hub": set(expected),
        "./cmd/shellmcp-go": set(expected),
        "./cmd/gptadmin-node": {item for item in expected if item.startswith("windows_")},
    }
    assert len(calls) == sum(len(items) for items in commands.values())
    for command, platforms in commands.items():
        selected = [call for call in calls if call["args"][-1] == command]
        assert {f'{call["os"]}_{call["arch"]}' for call in selected} == platforms
    assert all(call["args"][:3] == ["build", "-trimpath", "-buildvcs=false"] for call in calls)
