from __future__ import annotations

import json

from scripts import gptadmin_failover_reclaim_push as reclaim


class _Response:
    status = 200

    def __enter__(self) -> "_Response":
        return self

    def __exit__(self, *_: object) -> None:
        return None

    def read(self) -> bytes:
        return b'{"accepted":true}'


def test_reclaim_post_authenticates_to_ctl_endpoint(monkeypatch) -> None:
    seen: dict[str, str] = {}

    def fake_urlopen(request, timeout: float):
        seen.update({key.lower(): value for key, value in request.header_items()})
        assert json.loads(request.data.decode()) == {"action": "demote"}
        assert timeout == 3.0
        return _Response()

    monkeypatch.setattr(reclaim.urllib.request, "urlopen", fake_urlopen)

    status, body = reclaim.post_json(
        "http://127.0.0.1:19080/admin/api/failover/reclaim/accept",
        {"action": "demote"},
        3.0,
        authorization="test-ctl",
    )

    assert status == 200
    assert body == '{"accepted":true}'
    assert seen["authorization"] == "Bearer test-ctl"
