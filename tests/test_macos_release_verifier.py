import hashlib
import io
import json
from pathlib import Path
import struct
import tarfile

import pytest

from tools.verify_macos_release import CPU_TYPES, verify


def _bundle(path: Path, arch: str, version: int = 1074, binary_arch: str | None = None) -> str:
    filename = f"gptadmin-macos-{arch}-client.tar.gz"
    binary = b"\xcf\xfa\xed\xfe" + struct.pack("<I", CPU_TYPES[binary_arch or arch])
    with tarfile.open(path / filename, "w:gz") as archive:
        for name, data, mode in (
            ("./bin/shellmcp", binary, 0o755),
            ("./README.txt", f"GPTAdmin {version} for macos/{arch} (client)\n".encode(), 0o644),
        ):
            info = tarfile.TarInfo(name)
            info.size = len(data)
            info.mode = mode
            archive.addfile(info, io.BytesIO(data))
    return filename


def _release_tree(path: Path, *, wrong_arm_arch: bool = False, version: int = 1074) -> None:
    entries = []
    checksums = []
    for arch in ("x64", "arm64"):
        filename = _bundle(path, arch, version, binary_arch="x64" if wrong_arm_arch and arch == "arm64" else None)
        package = path / filename
        digest = hashlib.sha256(package.read_bytes()).hexdigest()
        entries.append({
            "platform": "macos", "arch": arch, "edition": "client", "file": filename,
            "sha256": digest, "size": package.stat().st_size,
        })
        checksums.append(f"{digest}  build/{filename}")
    (path / "gptadmin-checksums.txt").write_text("\n".join(checksums) + "\n")
    (path / "gptadmin-release-matrix.json").write_text(json.dumps({
        "schema": "gptadmin.release-matrix/v2", "build_version": version,
        "git_commit": "a" * 40, "artifacts": entries,
    }))


def test_verifies_both_macos_shellmcp_packages_and_release_provenance(tmp_path: Path) -> None:
    _release_tree(tmp_path)

    result = verify(tmp_path, minimum_build=1074)

    assert {item["file"] for item in result["verified"]} == {
        "gptadmin-macos-arm64-client.tar.gz", "gptadmin-macos-x64-client.tar.gz",
    }
    assert all(item["build_version"] == 1074 for item in result["verified"])


def test_rejects_release_below_minimum_build(tmp_path: Path) -> None:
    _release_tree(tmp_path, version=1073)

    with pytest.raises(ValueError, match="below required build"):
        verify(tmp_path, minimum_build=1074)


def test_rejects_macos_package_with_wrong_macho_architecture(tmp_path: Path) -> None:
    _release_tree(tmp_path, wrong_arm_arch=True)

    with pytest.raises(ValueError, match="wrong Mach-O architecture"):
        verify(tmp_path, minimum_build=1074)
