"""Local boundary tests for consuming the separately released GrepMesh runtime."""
from __future__ import annotations

import hashlib
import io
import tarfile
from pathlib import Path

import pytest

from tools.fetch_grepmesh import extract_runtime, verify_archive


def test_corrupt_cached_release_is_rejected(tmp_path):
    archive = tmp_path / "release.tar.gz"
    archive.write_bytes(b"changed release bytes")
    with pytest.raises(ValueError, match="SHA-256 mismatch"):
        verify_archive(archive, hashlib.sha256(b"approved bytes").hexdigest())


def test_runtime_extraction_rejects_path_escape(tmp_path):
    archive = tmp_path / "release.tar.gz"
    with tarfile.open(archive, "w:gz") as output:
        entry = tarfile.TarInfo("../escaped")
        entry.size = 1
        output.addfile(entry, io.BytesIO(b"x"))
    with pytest.raises(ValueError, match="unsafe archive path"):
        extract_runtime(archive, tmp_path / "out")
    assert not (tmp_path / "escaped").exists()


def test_runtime_extraction_preserves_companion_files(tmp_path):
    archive = tmp_path / "release.tar.gz"
    with tarfile.open(archive, "w:gz") as output:
        for name, data in (("grepmesh-mcp", b"binary"), ("rg", b"ripgrep"),
                           ("lib/libonnxruntime.dylib", b"runtime"),
                           ("licenses/NOTICE", b"license"),
                           ("config.example.json", b"{}")):
            entry = tarfile.TarInfo(name)
            entry.size = len(data)
            entry.mode = 0o755 if name in ("grepmesh-mcp", "rg") else 0o644
            output.addfile(entry, io.BytesIO(data))
    output = tmp_path / "out"
    extract_runtime(archive, output)
    assert (output / "rg").read_bytes() == b"ripgrep"
    assert (output / "lib/libonnxruntime.dylib").read_bytes() == b"runtime"
    assert (output / "licenses/NOTICE").read_bytes() == b"license"
    assert not (output / "config.example.json").exists()


def test_installer_keeps_companions_and_legacy_binary(monkeypatch, tmp_path):
    import cli

    package = tmp_path / "package"
    package.mkdir()
    binary = package / "grepmesh-mcp"
    binary.write_bytes(b"native binary")
    (package / "rg").write_bytes(b"bundled ripgrep")
    (package / "lib").mkdir()
    (package / "lib/libonnxruntime.dylib").write_bytes(b"native library")
    destination = tmp_path / "installed"
    monkeypatch.setattr(cli, "BIN_DIR", destination)
    monkeypatch.setattr(cli, "_grepmesh_binary_candidates", lambda _p: [binary])
    monkeypatch.setattr(cli, "_binary_looks_native", lambda _p: True)
    monkeypatch.setattr(cli, "_macos_unquarantine_and_codesign", lambda _p: None)
    assert cli._install_grepmesh_binary_from_pkg(package)
    assert (destination / "rg").read_bytes() == b"bundled ripgrep"
    assert (destination / "lib/libonnxruntime.dylib").read_bytes() == b"native library"
    (package / "rg").unlink()
    assert cli._install_grepmesh_binary_from_pkg(package)
    assert (destination / "grepmesh-mcp").read_bytes() == b"native binary"


@pytest.mark.parametrize("target,tagged", [("all", "0"), ("hub", "1")])
def test_native_scope_cannot_shrink_a_release(tmp_path, target, tagged):
    import os
    import subprocess

    source = Path(__file__).resolve().parents[1] / "tools/build.sh"
    (tmp_path / "tools").mkdir()
    script = tmp_path / "tools/build.sh"
    script.write_bytes(source.read_bytes())
    (tmp_path / "VERSION").write_text("42\n")
    result = subprocess.run(["bash", str(script), target], cwd=tmp_path,
                            env=dict(os.environ, GPTADMIN_BUILD_PLATFORMS="native",
                                     TAGGED_RELEASE=tagged), capture_output=True)
    assert result.returncode == 2
    assert (tmp_path / "VERSION").read_text() == "42\n"
    assert not (tmp_path / "build").exists()


def test_release_target_requires_tagged_inputs(tmp_path):
    import os
    import subprocess

    source = Path(__file__).resolve().parents[1] / "tools/build.sh"
    (tmp_path / "tools").mkdir()
    script = tmp_path / "tools/build.sh"
    script.write_bytes(source.read_bytes())
    (tmp_path / "VERSION").write_text("42\n")
    result = subprocess.run(["bash", str(script), "release"], cwd=tmp_path,
                            env=dict(os.environ, TAGGED_RELEASE="0",
                                     GPTADMIN_BUILD_PLATFORMS="default"), capture_output=True)
    assert result.returncode == 2
    assert (tmp_path / "VERSION").read_text() == "42\n"
    assert not (tmp_path / "build").exists()


def test_gptadmin_companion_keeps_text_only_default():
    import cli

    assert cli._grepmesh_default_config({})["ocr"] == {"enabled": False}


@pytest.mark.parametrize("explicit_ocr", [None, {"enabled": True}])
def test_managed_companion_upgrade_preserves_ocr_choice(monkeypatch, tmp_path, explicit_ocr):
    import cli
    import json

    config = tmp_path / "grepmesh.json"
    original = {"host_id": "retained-host", "root": "/retained/root"}
    if explicit_ocr is not None:
        original["ocr"] = explicit_ocr
    config.write_text(json.dumps(original))
    binaries = tmp_path / "bin"
    binaries.mkdir()
    (binaries / "grepmesh-mcp").write_bytes(b"existing executable")
    monkeypatch.setattr(cli, "GREPMESH_CONFIG_FILE", config)
    monkeypatch.setattr(cli, "BIN_DIR", binaries)
    monkeypatch.setattr(cli, "IS_USER_INSTALL", True)
    monkeypatch.setattr(cli, "_mcp_config", lambda: {
        "mcpServers": {"GrepMesh": {"gptadmin_builtin": True, "enabled": True}}
    })
    assert cli._configure_builtin_grepmesh({}, True)
    expected = dict(original, ocr=explicit_ocr if explicit_ocr is not None else {"enabled": False})
    assert json.loads(config.read_text()) == expected
