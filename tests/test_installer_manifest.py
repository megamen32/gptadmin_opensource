import hashlib
from pathlib import Path

import pytest
from fastapi.testclient import TestClient

import server_for_installer

# This test validates a BUILT artifact (build/gptadmin-shellmcp.tar.gz). CI runs
# the test step before the build step, so the tarball is absent there; skip
# rather than fail. Run it locally after `tools/build.sh`.
_TARBALL = server_for_installer.BUILD_DIR / "gptadmin-shellmcp.tar.gz"


def test_manifest_includes_hashes_for_shellmcp_tarball():
    if not _TARBALL.exists():
        pytest.skip(f"{_TARBALL} not built — run tools/build.sh")
    client = TestClient(server_for_installer.app)
    manifest = client.get("/manifest.json")
    assert manifest.status_code == 200
    artifacts = manifest.json()["artifacts"]

    name = "gptadmin-shellmcp.tar.gz"
    assert name in artifacts, "manifest missing gptadmin-shellmcp.tar.gz"
    item = artifacts[name]
    artifact = client.get(item["url"])
    assert artifact.status_code == 200
    assert item["size"] == len(artifact.content)
    assert item["sha256"] == hashlib.sha256(artifact.content).hexdigest()
