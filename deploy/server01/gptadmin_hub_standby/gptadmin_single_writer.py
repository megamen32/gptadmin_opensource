#!/usr/bin/env python3
"""Fail closed unless a fenced, complete single-writer handoff was prepared."""
import hashlib
import hmac
import json
import os
from pathlib import Path
import stat
import sys
from urllib.parse import urlsplit

DOMAINS = frozenset({"auth", "membership", "registry", "virtual_mcp", "instruction_sets_named", "instruction_sets_default", "secret_ingress", "webhooks", "webhook_routes", "security", "webauthn", "tasks"})
CREDENTIALS = ("CTL_TOKEN", "OAUTH_CLIENT_SECRET", "MCP_RELAY_AGENT_TOKEN", "ADMIN_PASSWORD")

def private_file(path):
    fd = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
    info = os.fstat(fd)
    if not stat.S_ISREG(info.st_mode) or info.st_mode & 0o077:
        os.close(fd)
        raise ValueError("handoff file is not owner-only")
    return os.fdopen(fd, "rb")

def read_json(path):
    with private_file(path) as source:
        raw = source.read((10 << 20) + 1)
    if len(raw) > 10 << 20:
        raise ValueError("handoff state too large")
    data = json.loads(raw)
    if not isinstance(data, dict):
        raise ValueError("handoff state is not an object")
    return data

def validate(config, identity_path, origin, environ):
    config = Path(config)
    parts = urlsplit(origin)
    if (parts.scheme != "https" or not parts.hostname or any(c.isspace() for c in origin)
        or parts.username or parts.password or parts.query or parts.fragment or parts.path not in ("", "/")
        or (parts.port is not None and not 1 <= parts.port <= 65535)):
        raise ValueError("writer origin must be an HTTPS origin")
    identity = read_json(identity_path)
    node_id = identity.get("server_id")
    if not isinstance(node_id, str) or not node_id:
        raise ValueError("executor identity missing")
    receipt = read_json(config / "single-writer-handoff.json")
    if (receipt.get("schema") != "gptadmin.single-writer-handoff/v1"
        or receipt.get("complete") is not True or receipt.get("source_writer_fenced") is not True
        or receipt.get("writer_id") != node_id
        or not isinstance(receipt.get("source_writer_id"), str) or not receipt["source_writer_id"]
        or set(receipt.get("domains", [])) != DOMAINS):
        raise ValueError("complete fenced authority handoff receipt required")
    members = read_json(config / "membership/membership.json")
    if members.get("writer_id") != node_id or node_id not in members.get("nodes", {}):
        raise ValueError("membership writer mismatch")
    auth = read_json(config / "auth-continuity/state.json")
    if (type(auth.get("generation")) is not int or auth["generation"] < 1
        or auth.get("pending") is not False or type(auth.get("slot")) is not int or auth.get("slot") not in (0, 1)
        or auth.get("writer_id") not in (receipt["source_writer_id"], node_id)):
        raise ValueError("auth snapshot is incomplete or belongs to another writer")
    first_start = auth.get("writer_id") == receipt["source_writer_id"] and auth.get("read_only") is True
    promoted = auth.get("writer_id") == node_id and auth.get("read_only") is False
    if not (first_start or promoted):
        raise ValueError("auth snapshot writer state is inconsistent")
    digests = receipt.get("credential_sha256")
    if not isinstance(digests, dict):
        raise ValueError("handoff credential fingerprints missing")
    for key in CREDENTIALS:
        value, expected = environ.get(key), digests.get(key)
        if not isinstance(value, str) or not value or not isinstance(expected, str):
            raise ValueError("required credential missing")
        if first_start and not hmac.compare_digest(hashlib.sha256(value.encode()).hexdigest(), expected):
            raise ValueError("handoff credential mismatch")
    slot = config / "auth-continuity" / f"slot-{auth['slot']}"
    read_json(slot / "mcp_tokens_state.json")
    read_json(slot / "oauth_clients_state.json")
    with private_file(config / "tasks_state.sqlite") as db:
        if db.read(16) != b"SQLite format 3\x00":
            raise ValueError("task history database missing")
    return node_id

if __name__ == "__main__":
    try:
        if len(sys.argv) != 4:
            raise ValueError("expected config directory, identity path and public origin")
        validate(sys.argv[1], sys.argv[2], sys.argv[3], os.environ)
    except (OSError, ValueError, TypeError, KeyError, json.JSONDecodeError):
        print("single-writer handoff is incomplete or invalid", file=sys.stderr)
        raise SystemExit(1)
