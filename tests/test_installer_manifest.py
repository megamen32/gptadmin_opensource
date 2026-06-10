import hashlib

from fastapi.testclient import TestClient

from services import server_for_installer


def test_manifest_includes_hashes_for_python_rootd_artifacts():
    client = TestClient(server_for_installer.app)
    manifest = client.get("/manifest.json")
    assert manifest.status_code == 200
    artifacts = manifest.json()["artifacts"]

    for name in ("rootd.py", "rootd_pure.py"):
        item = artifacts[name]
        artifact = client.get(item["url"])
        assert artifact.status_code == 200
        assert item["size"] == len(artifact.content)
        assert item["sha256"] == hashlib.sha256(artifact.content).hexdigest()
