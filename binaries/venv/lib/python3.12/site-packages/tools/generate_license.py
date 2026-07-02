#!/usr/bin/env python3
import argparse
import json
import datetime
import base64
from pathlib import Path
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import padding


def load_private_key(path: str):
    with open(path, "rb") as f:
        return serialization.load_pem_private_key(f.read(), password=None)


def make_license(private_key, days: int, max_servers: int):
    expiry = None
    if days > 0:
        expiry = (datetime.date.today() + datetime.timedelta(days=days)).strftime("%Y-%m-%d")
    data = {"expiry": expiry, "max_servers": max_servers}
    message = json.dumps(data, sort_keys=True, separators=(",",":")).encode()
    signature = private_key.sign(
        message,
        padding.PKCS1v15(),
        hashes.SHA256()
    )
    return {"data": data, "signature": base64.b64encode(signature).decode()}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="Generate signed license file")
    parser.add_argument("--days", type=int, default=30, help="Days until expiry; 0 for no expiry")
    parser.add_argument("--max-servers", type=int, default=1, help="Number of allowed servers; 0 for unlimited")
    config_dir = Path(__file__).resolve().parents[1] / "config"
    config_dir.mkdir(parents=True, exist_ok=True)
    parser.add_argument("--key", default=str(config_dir / "private.pem"), help="Path to private key in PEM format")
    parser.add_argument("--out", default=str(config_dir / "license.json"), help="Output license file path")
    args = parser.parse_args()

    pk = load_private_key(args.key)
    lic = make_license(pk, args.days, args.max_servers)
    with open(args.out, "w") as f:
        json.dump(lic, f, indent=2)
    print(f"License written to {args.out}")
