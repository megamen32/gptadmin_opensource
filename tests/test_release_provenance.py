import json
import subprocess
import sys
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[1]
VERIFIER = REPO_ROOT / "tools" / "verify_release_manifest.py"


def run_verifier(*args: str, cwd: Path) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        [sys.executable, str(VERIFIER), *args],
        cwd=cwd,
        text=True,
        capture_output=True,
        check=False,
    )


def test_release_manifest_records_and_verifies_every_archive(tmp_path: Path) -> None:
    build_dir = tmp_path / "build"
    public_dir = tmp_path / "public"
    build_dir.mkdir()
    public_dir.mkdir()
    (tmp_path / "VERSION").write_text("17\n", encoding="utf-8")
    (build_dir / "gptadmin-cli.tar.gz").write_bytes(b"cli artifact")
    (build_dir / "gptadmin-sbom.spdx.json").write_bytes(b'{"spdxVersion":"SPDX-2.3"}\n')
    (public_dir / "gptadmin-win.zip").write_bytes(b"windows artifact")
    manifest = build_dir / "manifest.json"

    generated = run_verifier(
        "generate",
        "--root",
        str(tmp_path),
        "--manifest",
        str(manifest),
        "--build-version",
        "17",
        "--build-ts",
        "2026-07-24T00:00:00Z",
        "--git-commit",
        "abc1234",
        cwd=REPO_ROOT,
    )
    assert generated.returncode == 0, generated.stderr

    payload = json.loads(manifest.read_text(encoding="utf-8"))
    assert payload["schema"] == "gptadmin.release-manifest/v1"
    assert payload["build_version"] == 17
    assert payload["git_commit"] == "abc1234"
    assert payload["sbom"]["path"] == "build/gptadmin-sbom.spdx.json"
    assert len(payload["sbom"]["sha256"]) == 64
    assert {item["path"] for item in payload["artifacts"]} == {
        "build/gptadmin-cli.tar.gz",
        "public/gptadmin-win.zip",
    }
    for item in payload["artifacts"]:
        assert item["size"] > 0
        assert len(item["sha256"]) == 64
        assert item["platform"]
        assert item["arch"]

    verified = run_verifier(
        "verify", "--root", str(tmp_path), "--manifest", str(manifest), cwd=REPO_ROOT
    )
    assert verified.returncode == 0, verified.stderr

    (public_dir / "gptadmin-win.zip").write_bytes(b"changed artifact")
    tampered = run_verifier(
        "verify", "--root", str(tmp_path), "--manifest", str(manifest), cwd=REPO_ROOT
    )
    assert tampered.returncode != 0
    assert "sha256 mismatch" in tampered.stderr


def test_release_manifest_rejects_an_unlisted_archive(tmp_path: Path) -> None:
    build_dir = tmp_path / "build"
    build_dir.mkdir()
    (tmp_path / "VERSION").write_text("3\n", encoding="utf-8")
    (build_dir / "gptadmin-cli.tar.gz").write_bytes(b"cli artifact")
    (build_dir / "gptadmin-sbom.spdx.json").write_bytes(b'{"spdxVersion":"SPDX-2.3"}\n')
    manifest = build_dir / "manifest.json"
    generated = run_verifier(
        "generate",
        "--root",
        str(tmp_path),
        "--manifest",
        str(manifest),
        "--build-version",
        "3",
        "--build-ts",
        "2026-07-24T00:00:00Z",
        "--git-commit",
        "abc1234",
        cwd=REPO_ROOT,
    )
    assert generated.returncode == 0, generated.stderr

    (build_dir / "gptadmin-hub.tar.gz").write_bytes(b"unlisted artifact")
    verified = run_verifier(
        "verify", "--root", str(tmp_path), "--manifest", str(manifest), cwd=REPO_ROOT
    )
    assert verified.returncode != 0
    assert "not listed" in verified.stderr
