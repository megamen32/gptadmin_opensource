from __future__ import annotations

import base64
import hashlib
import json
import os
import time
import uuid
from pathlib import Path
from typing import Any, Dict, Optional

from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey, Ed25519PublicKey
from cryptography.exceptions import InvalidSignature


def b64e(data: bytes) -> str:
    return base64.urlsafe_b64encode(data).decode("ascii").rstrip("=")


def b64d(data: str) -> bytes:
    pad = "=" * (-len(data) % 4)
    return base64.urlsafe_b64decode((data + pad).encode("ascii"))


def now_ts() -> int:
    return int(time.time())


def random_nonce() -> str:
    return b64e(os.urandom(18))


def public_key_to_b64(pub: Ed25519PublicKey) -> str:
    return b64e(pub.public_bytes(encoding=serialization.Encoding.Raw, format=serialization.PublicFormat.Raw))


def public_key_from_b64(data: str) -> Ed25519PublicKey:
    return Ed25519PublicKey.from_public_bytes(b64d(data))


def private_key_to_pem(priv: Ed25519PrivateKey) -> bytes:
    return priv.private_bytes(
        encoding=serialization.Encoding.PEM,
        format=serialization.PrivateFormat.PKCS8,
        encryption_algorithm=serialization.NoEncryption(),
    )


def private_key_from_pem(data: bytes) -> Ed25519PrivateKey:
    return serialization.load_pem_private_key(data, password=None)


def fingerprint_public_key_b64(public_key_b64: str) -> str:
    return "SHA256:" + b64e(hashlib.sha256(b64d(public_key_b64)).digest())


def load_or_create_ed25519_private_key(path: str | Path, mode: int = 0o600) -> Ed25519PrivateKey:
    p = Path(path)
    if p.exists():
        return private_key_from_pem(p.read_bytes())
    p.parent.mkdir(parents=True, exist_ok=True)
    priv = Ed25519PrivateKey.generate()
    p.write_bytes(private_key_to_pem(priv))
    os.chmod(p, mode)
    return priv


def write_public_key(path: str | Path, public_key_b64: str, mode: int = 0o644) -> None:
    p = Path(path)
    p.parent.mkdir(parents=True, exist_ok=True)
    p.write_text(public_key_b64 + "\n")
    os.chmod(p, mode)


def load_public_key_b64(path: str | Path) -> str:
    return Path(path).read_text().strip()


def load_or_create_identity(config_dir: str | Path, name: Optional[str] = None, prefix: str = "shellmcp") -> Dict[str, Any]:
    cfg = Path(config_dir)
    cfg.mkdir(parents=True, exist_ok=True)
    key_file = cfg / f"{prefix}_ed25519"
    pub_file = cfg / f"{prefix}_ed25519.pub"
    ident_file = cfg / f"{prefix}_identity.json"
    priv = load_or_create_ed25519_private_key(key_file)
    pub_b64 = public_key_to_b64(priv.public_key())
    write_public_key(pub_file, pub_b64)
    if ident_file.exists():
        try:
            ident = json.loads(ident_file.read_text())
        except Exception:
            ident = {}
    else:
        ident = {}
    changed = False
    if not ident.get("server_id"):
        ident["server_id"] = str(uuid.uuid4())
        changed = True
    if name and ident.get("name") != name:
        ident["name"] = name
        changed = True
    elif not ident.get("name") and name:
        ident["name"] = name
        changed = True
    if ident.get("public_key") != pub_b64:
        ident["public_key"] = pub_b64
        changed = True
    fp = fingerprint_public_key_b64(pub_b64)
    if ident.get("fingerprint") != fp:
        ident["fingerprint"] = fp
        changed = True
    if not ident.get("created_at"):
        ident["created_at"] = now_ts()
        changed = True
    if changed or not ident_file.exists():
        ident_file.write_text(json.dumps(ident, ensure_ascii=False, indent=2, sort_keys=True) + "\n")
        os.chmod(ident_file, 0o600)
    return {"identity": ident, "private_key": priv, "public_key_b64": pub_b64, "fingerprint": fp}


def canonical_request(method: str, path: str, timestamp: str | int, nonce: str, body: bytes) -> bytes:
    body_hash = hashlib.sha256(body or b"").hexdigest()
    return f"{method.upper()}\n{path}\n{timestamp}\n{nonce}\n{body_hash}".encode("utf-8")


def sign_request(private_key: Ed25519PrivateKey, method: str, path: str, body: bytes, timestamp: Optional[int] = None, nonce: Optional[str] = None) -> Dict[str, str]:
    ts = str(timestamp or now_ts())
    nn = nonce or random_nonce()
    sig = private_key.sign(canonical_request(method, path, ts, nn, body))
    return {"timestamp": ts, "nonce": nn, "signature": b64e(sig)}


def verify_signature(public_key_b64: str, method: str, path: str, timestamp: str | int, nonce: str, body: bytes, signature_b64: str, max_skew_s: int = 300) -> None:
    ts_int = int(timestamp)
    if abs(now_ts() - ts_int) > max_skew_s:
        raise ValueError("signature timestamp outside allowed skew")
    pub = public_key_from_b64(public_key_b64)
    try:
        pub.verify(b64d(signature_b64), canonical_request(method, path, str(timestamp), nonce, body))
    except InvalidSignature:
        raise ValueError("invalid signature")


class NonceCache:
    def __init__(self, ttl_s: int = 300):
        self.ttl_s = ttl_s
        self._seen: Dict[str, float] = {}

    def check_and_store(self, scope: str, nonce: str) -> None:
        now = time.time()
        cutoff = now - self.ttl_s
        for k, t in list(self._seen.items()):
            if t < cutoff:
                self._seen.pop(k, None)
        key = f"{scope}:{nonce}"
        if key in self._seen:
            raise ValueError("replayed nonce")
        self._seen[key] = now
