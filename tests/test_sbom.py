import json
import subprocess
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
TOOL = ROOT / "tools" / "generate_sbom.py"


def test_sbom_generation_is_deterministic_and_covers_dependency_manifests(tmp_path: Path) -> None:
    (tmp_path / "pyproject.toml").write_text(
        '[project]\nname = "demo"\nversion = "1.0"\ndependencies = ["httpx>=1"]\n',
        encoding="utf-8",
    )
    (tmp_path / "go-hub").mkdir()
    (tmp_path / "go-hub" / "go.mod").write_text(
        "module example/hub\n\ngo 1.24\n\nrequire example.com/relay v1.2.3\n",
        encoding="utf-8",
    )
    (tmp_path / "admin-ui").mkdir()
    (tmp_path / "admin-ui" / "package-lock.json").write_text(
        json.dumps(
            {
                "packages": {
                    "": {"name": "demo-ui", "version": "1.0.0"},
                    "node_modules/react": {"version": "19.0.0"},
                }
            }
        ),
        encoding="utf-8",
    )
    output = tmp_path / "build" / "sbom.json"

    args = [
        sys.executable,
        str(TOOL),
        "--root",
        str(tmp_path),
        "--output",
        str(output),
        "--build-version",
        "7",
        "--build-ts",
        "2026-07-24T00:00:00Z",
        "--git-commit",
        "abc1234",
    ]
    first = subprocess.run(args, cwd=ROOT, capture_output=True, text=True, check=False)
    assert first.returncode == 0, first.stderr
    first_bytes = output.read_bytes()
    first_payload = json.loads(first_bytes)
    assert first_payload["spdxVersion"] == "SPDX-2.3"
    assert first_payload["gptadminBuild"]["build_version"] == 7
    names = {package["name"] for package in first_payload["packages"]}
    assert "httpx" in names
    assert "example.com/relay" in names
    assert "react" in names

    second = subprocess.run(args, cwd=ROOT, capture_output=True, text=True, check=False)
    assert second.returncode == 0, second.stderr
    assert output.read_bytes() == first_bytes
