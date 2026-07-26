"""Behavioral regressions for the immutable tag-build contract."""

from __future__ import annotations

import json
import os
import shutil
import socket
import subprocess
import tarfile
import time
import urllib.request
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[1]


def _minimal_build_repo(tmp_path: Path, *, include_shellmcp: bool = False) -> Path:
    """Create a minimal real build-script checkout for release identity tests."""

    repo = tmp_path / "release-build"
    (repo / "tools").mkdir(parents=True)
    shutil.copy2(REPO_ROOT / "tools" / "build.sh", repo / "tools" / "build.sh")
    shutil.copy2(REPO_ROOT / "tools" / "generate_sbom.py", repo / "tools" / "generate_sbom.py")
    shutil.copy2(REPO_ROOT / "tools" / "verify_release_manifest.py", repo / "tools" / "verify_release_manifest.py")
    shutil.copy2(REPO_ROOT / "cli.py", repo / "cli.py")
    if include_shellmcp:
        shutil.copytree(REPO_ROOT / "go-hub", repo / "go-hub")
        shutil.copytree(REPO_ROOT / "go-shellmcp", repo / "go-shellmcp")
    (repo / "VERSION").write_text("129\n", encoding="utf-8")
    subprocess.run(["git", "init"], cwd=repo, check=True, capture_output=True, text=True)
    subprocess.run(["git", "config", "user.email", "release-test@example.invalid"], cwd=repo, check=True)
    subprocess.run(["git", "config", "user.name", "Release Test"], cwd=repo, check=True)
    subprocess.run(["git", "add", "."], cwd=repo, check=True)
    subprocess.run(["git", "commit", "-m", "release fixture"], cwd=repo, check=True, capture_output=True, text=True)
    subprocess.run(["git", "tag", "v129"], cwd=repo, check=True)
    return repo


def _run_tagged_build(repo: Path, tag: str, *, target: str = "cli", release_commit: str | None = None) -> subprocess.CompletedProcess[str]:
    """Run the real CLI build path with the proposed immutable release inputs."""

    if release_commit is None:
        release_commit = subprocess.run(
            ["git", "rev-parse", "HEAD"], cwd=repo, check=True, capture_output=True, text=True
        ).stdout.strip()
    environment = os.environ | {
        "TAGGED_RELEASE": "1",
        "RELEASE_TAG": tag,
        "RELEASE_COMMIT": release_commit,
        "SKIP_TESTS": "1",
    }
    return subprocess.run(
        ["bash", "tools/build.sh", target],
        cwd=repo,
        env=environment,
        text=True,
        capture_output=True,
        check=False,
    )


def _run_developer_build(repo: Path) -> subprocess.CompletedProcess[str]:
    """Run the established non-tag build path for characterization coverage."""

    return subprocess.run(
        ["bash", "tools/build.sh", "cli"],
        cwd=repo,
        env=os.environ | {"SKIP_TESTS": "1"},
        text=True,
        capture_output=True,
        check=False,
    )


def test_tagged_build_keeps_version_and_generates_matching_metadata(tmp_path: Path) -> None:
    """A v129 tag must package v129 without rewriting the tracked version."""

    repo = _minimal_build_repo(tmp_path)
    completed = _run_tagged_build(repo, "v129")

    assert completed.returncode == 0, completed.stderr
    assert (repo / "VERSION").read_text(encoding="utf-8") == "129\n"
    build_info = (repo / "client" / "gptadmin_build_info.py").read_text(encoding="utf-8")
    assert "BUILD_VERSION = 129" in build_info
    commit = subprocess.run(["git", "rev-parse", "HEAD"], cwd=repo, check=True, capture_output=True, text=True).stdout.strip()
    assert len(commit) == 40
    assert f'GIT_COMMIT = "{commit}"' in build_info
    assert (repo / "gptadmin_build_info.py").read_text(encoding="utf-8") == build_info
    manifest = json.loads((repo / "build" / "manifest.json").read_text(encoding="utf-8"))
    assert manifest["build_version"] == 129
    assert manifest["git_commit"] == commit
    sbom = json.loads((repo / "build" / "gptadmin-sbom.spdx.json").read_text(encoding="utf-8"))
    assert sbom["gptadminBuild"]["build_version"] == 129
    assert sbom["gptadminBuild"]["git_commit"] == commit


def test_tagged_build_rejects_tag_version_mismatch(tmp_path: Path) -> None:
    """A mismatched tag must fail before building an incorrectly labelled artifact."""

    completed = _run_tagged_build(_minimal_build_repo(tmp_path), "v130")

    assert completed.returncode != 0
    assert "RELEASE_TAG must equal v129" in completed.stdout + completed.stderr


def test_tagged_build_requires_existing_tag_at_head(tmp_path: Path) -> None:
    """A tag build must not emit provenance when its declared tag is absent."""

    repo = _minimal_build_repo(tmp_path)
    subprocess.run(["git", "tag", "-d", "v129"], cwd=repo, check=True, capture_output=True, text=True)
    completed = _run_tagged_build(repo, "v129")

    assert completed.returncode != 0
    assert "RELEASE_TAG v129 does not resolve to HEAD" in completed.stdout + completed.stderr


def test_tagged_build_rejects_branch_with_release_tag_name(tmp_path: Path) -> None:
    """A same-named branch cannot substitute for the required release tag ref."""

    repo = _minimal_build_repo(tmp_path)
    subprocess.run(["git", "tag", "-d", "v129"], cwd=repo, check=True, capture_output=True, text=True)
    subprocess.run(["git", "branch", "v129"], cwd=repo, check=True)
    completed = _run_tagged_build(repo, "v129")

    assert completed.returncode != 0
    assert "missing refs/tags/v129" in completed.stdout + completed.stderr
    assert not (repo / "build" / "gptadmin-cli.tar.gz").exists()


def test_tagged_build_rejects_tag_that_is_not_head(tmp_path: Path) -> None:
    """A tag from an earlier commit cannot label artifacts from a newer HEAD."""

    repo = _minimal_build_repo(tmp_path)
    (repo / "release-note.txt").write_text("new head\n", encoding="utf-8")
    subprocess.run(["git", "add", "release-note.txt"], cwd=repo, check=True)
    subprocess.run(["git", "commit", "-m", "new head"], cwd=repo, check=True, capture_output=True, text=True)
    completed = _run_tagged_build(repo, "v129")

    assert completed.returncode != 0
    assert "RELEASE_TAG v129 does not resolve to HEAD" in completed.stdout + completed.stderr


def test_tagged_build_rejects_non_numeric_version(tmp_path: Path) -> None:
    """Tag builds reject malformed version files instead of normalizing them."""

    repo = _minimal_build_repo(tmp_path)
    (repo / "VERSION").write_text("129oops\n", encoding="utf-8")
    completed = _run_tagged_build(repo, "v129")

    assert completed.returncode != 0
    assert "VERSION must contain a plain integer" in completed.stdout + completed.stderr


def test_tagged_build_rejects_release_commit_mismatch(tmp_path: Path) -> None:
    """CI's immutable commit input must resolve to the tagged checkout commit."""

    repo = _minimal_build_repo(tmp_path)
    completed = _run_tagged_build(repo, "v129", release_commit="0" * 40)

    assert completed.returncode != 0
    assert "RELEASE_COMMIT must equal HEAD" in completed.stdout + completed.stderr


def test_tagged_build_requires_release_commit(tmp_path: Path) -> None:
    """Tagged builds fail closed when CI omits the immutable commit input."""

    completed = _run_tagged_build(_minimal_build_repo(tmp_path), "v129", release_commit="")

    assert completed.returncode != 0
    assert "RELEASE_COMMIT must be a full lowercase commit SHA" in completed.stdout + completed.stderr


def test_non_tagged_build_retains_single_version_bump_policy(tmp_path: Path) -> None:
    """Developer builds retain their established single-bump behavior."""

    repo = _minimal_build_repo(tmp_path)
    completed = _run_developer_build(repo)

    assert completed.returncode == 0, completed.stderr
    assert (repo / "VERSION").read_text(encoding="utf-8") == "130\n"
    manifest = json.loads((repo / "build" / "manifest.json").read_text(encoding="utf-8"))
    sbom = json.loads((repo / "build" / "gptadmin-sbom.spdx.json").read_text(encoding="utf-8"))
    short_commit = subprocess.run(
        ["git", "rev-parse", "--short", "HEAD"], cwd=repo, check=True, capture_output=True, text=True
    ).stdout.strip()
    assert manifest["build_version"] == 130
    assert manifest["git_commit"] == short_commit
    assert sbom["gptadminBuild"]["build_version"] == 130
    assert sbom["gptadminBuild"]["git_commit"] == short_commit


def test_tagged_build_rejects_and_removes_preexisting_release_archives(tmp_path: Path) -> None:
    """A tagged build cannot mix stale archives into current release provenance."""

    repo = _minimal_build_repo(tmp_path)
    stale_archive = repo / "build" / "gptadmin-v128.tar.gz"
    stale_archive.parent.mkdir()
    stale_archive.write_bytes(b"stale v128 archive")
    completed = _run_tagged_build(repo, "v129")

    assert completed.returncode != 0
    assert "pre-existing release archives" in completed.stdout + completed.stderr
    assert not list((repo / "build").rglob("gptadmin*.tar.gz"))
    assert not (repo / "build" / "manifest.json").exists()


def test_tagged_build_removes_stale_public_archive_before_manifest(tmp_path: Path) -> None:
    """Tracked generated public archives must be rebuilt before tagged provenance includes them."""

    repo = _minimal_build_repo(tmp_path)
    stale_archive = repo / "public" / "gptadmin-win.zip"
    stale_archive.parent.mkdir()
    stale_archive.write_bytes(b"stale public v128 archive")
    completed = _run_tagged_build(repo, "v129")

    assert completed.returncode == 0, completed.stderr
    assert not stale_archive.exists()
    manifest = json.loads((repo / "build" / "manifest.json").read_text(encoding="utf-8"))
    assert {artifact["path"] for artifact in manifest["artifacts"]} == {"build/gptadmin-cli.tar.gz"}


def test_tagged_shellmcp_artifact_reports_manifest_and_sbom_identity(tmp_path: Path) -> None:
    """The packaged binary must report the same version and commit as release metadata."""

    repo = _minimal_build_repo(tmp_path, include_shellmcp=True)
    completed = _run_tagged_build(repo, "v129", target="shellmcp")
    assert completed.returncode == 0, completed.stderr

    extracted = tmp_path / "artifact"
    with tarfile.open(repo / "build" / "gptadmin-shellmcp.tar.gz", "r:gz") as archive:
        archive.extractall(extracted, filter="data")
    binary = extracted / "go-shellmcp" / "linux_amd64" / "shellmcp-go"
    manifest = json.loads((repo / "build" / "manifest.json").read_text(encoding="utf-8"))
    sbom = json.loads((repo / "build" / "gptadmin-sbom.spdx.json").read_text(encoding="utf-8"))
    artifact_metadata = json.loads((repo / "build" / "gptadmin-shellmcp.json").read_text(encoding="utf-8"))

    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        port = listener.getsockname()[1]
    environment = os.environ | {
        "SHELL_HOST": "127.0.0.1",
        "SHELL_PORT": str(port),
        "SHELLMCP_MODE": "webhook",
        "SHELLMCP_QUEUE": "0",
        "SHELLMCP_HEARTBEAT": "0",
        "SHELLMCP_TOKEN": "release-identity-test",
    }
    process = subprocess.Popen([str(binary)], env=environment, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    try:
        for _ in range(100):
            try:
                with urllib.request.urlopen(f"http://127.0.0.1:{port}/version", timeout=0.2) as response:
                    version = json.load(response)
                break
            except OSError:
                time.sleep(0.05)
        else:
            stdout, stderr = process.communicate(timeout=1)
            raise AssertionError(f"packaged ShellMCP did not start: stdout={stdout!r} stderr={stderr!r}")
    finally:
        process.terminate()
        process.wait(timeout=5)

    expected = {"build_version": manifest["build_version"], "git_commit": manifest["git_commit"]}
    assert {key: sbom["gptadminBuild"][key] for key in expected} == expected
    assert {key: artifact_metadata[key] for key in expected} == expected
    assert {key: version[key] for key in expected} == expected
