import json
import os
import sys
from pathlib import Path
from types import SimpleNamespace

from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey

REPO = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(REPO))
os.environ.setdefault("GPTADMIN_AUDIT_LOG", "/tmp/gptadmin-test-audit.log")

import hub_proxy  # noqa: E402
from gptadmin_security import NonceCache, fingerprint_public_key_b64, public_key_to_b64, sign_request  # noqa: E402


def _pub(priv):
    return public_key_to_b64(priv.public_key())


def test_reconcile_uses_approved_identity_over_stale_state():
    hub_proxy.approved_servers.clear()
    hub_proxy.approved_servers["win"] = {
        "server_id": "new-id",
        "public_key": "new-public",
        "fingerprint": "SHA256:new",
        "base_url": "http://new:25900",
        "backend": "local",
    }
    stale = {
        "name": "win",
        "server_id": "old-id",
        "public_key": "old-public",
        "fingerprint": "SHA256:old",
        "base_url": "http://old:25900",
        "backend": "old",
        "mode": "polling",
    }

    reconciled = hub_proxy._reconcile_approved_server_record("win", stale)

    assert reconciled["server_id"] == "new-id"
    assert reconciled["public_key"] == "new-public"
    assert reconciled["fingerprint"] == "SHA256:new"
    assert reconciled["base_url"] == "http://new:25900"
    assert reconciled["backend"] == "local"
    assert reconciled["mode"] == "polling"


def test_heartbeat_signature_accepts_rotated_key_for_pending_flow():
    old_priv = Ed25519PrivateKey.generate()
    new_priv = Ed25519PrivateKey.generate()
    old_pub = _pub(old_priv)
    new_pub = _pub(new_priv)
    server_id = "rotated-id"
    name = "BeyondInfinity"

    hub_proxy.approved_servers.clear()
    hub_proxy.approved_servers[name] = {
        "server_id": "old-id",
        "public_key": old_pub,
        "fingerprint": fingerprint_public_key_b64(old_pub),
        "base_url": "http://old:25900",
        "backend": "local",
    }
    hub_proxy.SIGNATURE_NONCES = NonceCache(ttl_s=300)

    beat = hub_proxy.Beat(
        name=name,
        base_url="http://203.0.113.10:25900",
        rootd_token="srv_secret",
        time=1,
        mode="polling",
        server_id=server_id,
        public_key=new_pub,
        fingerprint=fingerprint_public_key_b64(new_pub),
    )
    body = beat.model_dump_json().encode("utf-8")
    signed = sign_request(new_priv, "POST", "/heartbeat", body, timestamp=int(hub_proxy.time.time()), nonce="unit-test-nonce")
    request = SimpleNamespace(
        method="POST",
        url=SimpleNamespace(path="/heartbeat"),
        headers={
            "X-GPTAdmin-Server": name,
            "X-GPTAdmin-Server-ID": server_id,
            "X-GPTAdmin-Timestamp": signed["timestamp"],
            "X-GPTAdmin-Nonce": signed["nonce"],
            "X-GPTAdmin-Signature": signed["signature"],
        },
    )

    hub_proxy._verify_heartbeat_signature(request, beat, body)
