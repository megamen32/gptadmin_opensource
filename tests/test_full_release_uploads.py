"""Local publisher orchestration tests; these never publish a GitHub release."""
from collections import Counter
import json
from pathlib import Path
import threading
import time

import pytest

from tools import publish_full_release as release


SOURCE = "a" * 40
PRIVATE = "b" * 40


def publisher_fixture(monkeypatch, tmp_path: Path, *, upload_hook=None, reject_recheck=False):
    assets = [tmp_path / f"asset-{i:02d}.bin" for i in range(18)]
    for index, path in enumerate(assets):
        path.write_bytes(f"verified package {index}\n".encode())
    state = {"calls": [], "attempts": Counter(), "remote": {}, "checks": 0}
    lock = threading.Lock()

    monkeypatch.setattr(release, "checked_assets", lambda *args: assets)
    monkeypatch.setattr(release, "latest_release_tag", lambda *args: "v1077")
    monkeypatch.setattr(release.node, "tag_commit", lambda *args, **kwargs: None)
    monkeypatch.setattr(release.node, "verify_tag", lambda *args: None)

    def verify(*_args):
        with lock:
            state["checks"] += 1
            count = state["checks"]
            uploaded = len(state["remote"])
        if count == 2:
            assert uploaded == len(assets), "publication recheck ran before uploads settled"
            if reject_recheck:
                raise ValueError("public main changed during upload")

    monkeypatch.setattr(release, "verify_ci_and_node", verify)

    def gh(*args):
        endpoint = args[1]
        method = args[args.index("--method") + 1] if "--method" in args else "GET"
        with lock:
            state["calls"].append((endpoint, method, args))
        if endpoint.endswith("/git/refs") and method == "POST":
            return b"{}"
        if endpoint.endswith("/releases?per_page=100") and method == "GET":
            return b""
        if endpoint.endswith("/releases") and method == "POST":
            assert "draft=true" in args and "make_latest=false" in args
            return json.dumps({"id": 27, "tag_name": "v1078", "draft": True}).encode()
        if endpoint.startswith("https://uploads.github.com/"):
            path = Path(args[args.index("--input") + 1])
            with lock:
                state["attempts"][path.name] += 1
            asset = {"name": path.name, "size": path.stat().st_size,
                     "digest": "sha256:" + release.node.digest(path)}
            if upload_hook:
                asset = upload_hook(path, asset)
            with lock:
                state["remote"][path.name] = asset
            return json.dumps(asset).encode()
        if endpoint.endswith("/releases/27") and method == "PATCH":
            assert "draft=false" in args and "make_latest=false" in args
            with lock:
                assert state["checks"] == 2
                remote = list(state["remote"].values())
            return json.dumps({"id": 27, "tag_name": "v1078", "draft": False,
                               "assets": remote}).encode()
        raise AssertionError((endpoint, method))

    monkeypatch.setattr(release.node, "gh", gh)
    return assets, state


def publish(tmp_path):
    release.publish(tmp_path, SOURCE, PRIVATE, 1078, 123, tmp_path)


def assert_draft_retained(state):
    assert not any(method == "PATCH" for _, method, _ in state["calls"])
    assert sum(endpoint.endswith("/git/refs") and method == "POST"
               for endpoint, method, _ in state["calls"]) == 1
    assert sum(endpoint.endswith("/releases") and method == "POST"
               for endpoint, method, _ in state["calls"]) == 1
    assert all(count == 1 for count in state["attempts"].values())


def test_uploads_overlap_with_at_most_four_in_flight(monkeypatch, tmp_path):
    barrier = threading.Barrier(4, timeout=3)
    lock = threading.Lock()
    active = started = peak = 0

    def upload_hook(_path, asset):
        nonlocal active, started, peak
        with lock:
            started += 1
            sequence = started
            active += 1
            peak = max(active, peak)
        try:
            if sequence <= 4:
                barrier.wait()
            # Keep the bounded calls overlapping long enough to observe their peak.
            time.sleep(.02)
            return asset
        finally:
            with lock:
                active -= 1

    assets, state = publisher_fixture(monkeypatch, tmp_path, upload_hook=upload_hook)
    publish(tmp_path)
    assert peak == 4
    assert state["attempts"] == Counter({path.name: 1 for path in assets})
    assert state["checks"] == 2
    assert sum(method == "PATCH" for _, method, _ in state["calls"]) == 1


def test_upload_failure_retains_draft_without_publication_or_retry(monkeypatch, tmp_path):
    def upload_hook(path, asset):
        if path.name == "asset-00.bin":
            raise RuntimeError("upload outcome unknown")
        return asset

    _, state = publisher_fixture(monkeypatch, tmp_path, upload_hook=upload_hook)
    with pytest.raises(RuntimeError, match="upload outcome unknown"):
        publish(tmp_path)
    assert_draft_retained(state)
    assert state["checks"] == 1
    assert state["attempts"]["asset-00.bin"] == 1


@pytest.mark.parametrize("field,value", [("name", "wrong-name.bin"),
                                        ("size", -1), ("digest", "sha256:" + "0" * 64)])
def test_invalid_upload_receipt_never_publishes(monkeypatch, tmp_path, field, value):
    def upload_hook(path, asset):
        if path.name == "asset-00.bin":
            asset[field] = value
        return asset

    _, state = publisher_fixture(monkeypatch, tmp_path, upload_hook=upload_hook)
    with pytest.raises(ValueError, match="uploaded asset"):
        publish(tmp_path)
    assert_draft_retained(state)


def test_changed_source_after_parallel_uploads_retains_draft(monkeypatch, tmp_path):
    assets, state = publisher_fixture(monkeypatch, tmp_path, reject_recheck=True)
    with pytest.raises(ValueError, match="public main changed"):
        publish(tmp_path)
    assert_draft_retained(state)
    assert state["checks"] == 2
    assert state["attempts"] == Counter({path.name: 1 for path in assets})
