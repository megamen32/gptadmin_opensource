#!/usr/bin/env python3
"""Generate a secret-free failover bundle from Supervisor app options."""

from __future__ import annotations

import argparse
import json
import secrets
from pathlib import Path
from typing import Any


INTERNAL_SECRET_KEYS = (
    "ctl_token",
    "mcp_relay_agent_token",
    "shellmcp_token",
    "oauth_client_secret",
    "mcp_bridge_key",
)


def _failover_options(options: dict[str, Any]) -> dict[str, Any]:
    failover = options.get("failover")
    if not isinstance(failover, dict):
        raise ValueError("failover options are required")
    return failover


def _required(failover: dict[str, Any], key: str) -> Any:
    value = failover.get(key)
    if value is None or value == "" or value == []:
        raise ValueError(f"failover.{key} is required")
    return value


def _endpoints(failover: dict[str, Any]) -> list[str]:
    raw = _required(failover, "endpoints")
    if not isinstance(raw, list) or not all(isinstance(item, str) and item.strip() for item in raw):
        raise ValueError("failover.endpoints must be a non-empty list of host:port strings")
    return [item.strip() for item in raw]


def build_failover_config(options: dict[str, Any]) -> dict[str, Any]:
    """Build watchdog policy and node metadata without copying credentials."""

    failover = _failover_options(options)
    node_id = str(_required(failover, "node_id"))
    local_port = int(failover.get("local_hub_port") or 9001)
    return {
        "enabled": True,
        "fail_count_base": int(failover.get("fail_count_base") or 3),
        "deterministic_rank_backoff": True,
        "primary_health_url": str(_required(failover, "primary_health_url")).rstrip("/"),
        "primary_public_url": str(_required(failover, "primary_public_url")).rstrip("/"),
        "public_confirm_timeout_sec": int(failover.get("public_confirm_timeout_sec") or 3),
        "reclaim_max_age_sec": 120,
        "nodes": [
            {
                "server_id": node_id,
                "rank": int(failover.get("rank") or 1),
                "enabled": True,
                "hub_url": f"http://127.0.0.1:{local_port}",
                "local_hub_port": local_port,
            }
        ],
    }


def build_failover_state(options: dict[str, Any]) -> dict[str, Any]:
    """Build FRP routing metadata while excluding token and signing secrets."""

    failover = _failover_options(options)
    endpoints = _endpoints(failover)
    return {
        "hub_public_url": str(_required(failover, "primary_public_url")).rstrip("/"),
        "tunnel": {
            "frp": {
                "endpoints": endpoints,
                "subdomain": str(_required(failover, "subdomain")),
                "domain": str(_required(failover, "domain")),
                "public_url": str(_required(failover, "primary_public_url")).rstrip("/"),
                "local_port": int(failover.get("local_hub_port") or 9001),
            }
        },
    }


def write_bundle(options_path: Path, config_path: Path, state_path: Path) -> None:
    """Write the generated bundle with restrictive permissions."""

    options = json.loads(options_path.read_text(encoding="utf-8"))
    config_path.parent.mkdir(parents=True, exist_ok=True)
    state_path.parent.mkdir(parents=True, exist_ok=True)
    for path, payload in ((config_path, build_failover_config(options)), (state_path, build_failover_state(options))):
        path.write_text(json.dumps(payload, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        path.chmod(0o600)


def ensure_internal_secrets(path: Path) -> dict[str, str]:
    """Create and persist internal credentials without exposing them as options."""

    try:
        current = json.loads(path.read_text(encoding="utf-8"))
    except (FileNotFoundError, json.JSONDecodeError):
        current = {}
    if not isinstance(current, dict):
        current = {}
    values = {
        key: str(current.get(key) or secrets.token_urlsafe(32))
        for key in INTERNAL_SECRET_KEYS
    }
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(json.dumps(values, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    temporary.chmod(0o600)
    temporary.replace(path)
    return values


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--options", type=Path)
    parser.add_argument("--config", type=Path)
    parser.add_argument("--state", type=Path)
    parser.add_argument("--ensure-internal-secrets", type=Path)
    args = parser.parse_args()
    if args.ensure_internal_secrets:
        ensure_internal_secrets(args.ensure_internal_secrets)
        return 0
    if not args.options or not args.config or not args.state:
        parser.error("--options, --config and --state are required unless --ensure-internal-secrets is used")
    write_bundle(args.options, args.config, args.state)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
