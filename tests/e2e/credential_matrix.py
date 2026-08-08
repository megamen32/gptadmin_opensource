#!/usr/bin/env python3
"""Verify every known credential on its entitled GPTAdmin paths without leaks.

The inventory contains only a stable credential ID, the name of an environment
variable that holds its bearer, and the paths that credential is entitled to
use. Bearer values and response bodies are never printed or persisted.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import urllib.error
import urllib.request
from collections.abc import Mapping, Sequence
from typing import Any


class CredentialMatrixError(RuntimeError):
    """Raised when an inventory entry cannot pass its safe entitled probes."""


_SUPPORTED_PATHS = {"custom", "mcp_remote", "relay_vrp"}


def _request(base_url: str, path: str, bearer: str, payload: dict[str, Any] | None = None) -> int:
    """Make one direct bounded probe and return only its status code."""

    data = json.dumps(payload).encode("utf-8") if payload is not None else None
    headers = {"Accept": "application/json", "Authorization": f"Bearer {bearer}"}
    if payload is not None:
        headers["Content-Type"] = "application/json"
    request = urllib.request.Request(base_url.rstrip("/") + path, data=data, headers=headers, method="POST" if payload else "GET")
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    try:
        with opener.open(request, timeout=10) as response:
            response.read(1024)
            return response.status
    except urllib.error.HTTPError as exc:
        return exc.code
    except (OSError, urllib.error.URLError) as exc:
        raise CredentialMatrixError(f"{path}: transport failure ({type(exc).__name__})") from None


def _probe(base_url: str, path_name: str, bearer: str) -> None:
    """Run the minimal harmless probe for one supported integration path."""

    if path_name == "custom":
        status = _request(base_url, "/server/hub/mcp", bearer, {"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": {}})
    elif path_name == "mcp_remote":
        status = _request(base_url, "/mcp", bearer, {"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": {}})
    elif path_name == "relay_vrp":
        status = _request(base_url, "/mcp-relay/servers", bearer)
    else:
        raise CredentialMatrixError(f"unsupported path {path_name!r}")
    if status != 200:
        raise CredentialMatrixError(f"{path_name}: HTTP {status}")


def run_credential_matrix(base_url: str, credentials: Sequence[Mapping[str, Any]], environ: Mapping[str, str] | None = None) -> dict[str, Any]:
    """Probe every inventory credential only on its declared entitled paths.

    Args:
        base_url: Public Hub origin.
        credentials: Secret-free entries with `id`, `env`, and non-empty `paths`.
        environ: Environment mapping used to resolve bearer values; injectable for
            regression tests and defaults to the process environment.
    """

    if not base_url.strip():
        raise CredentialMatrixError("base URL is required")
    environment = os.environ if environ is None else environ
    summary: list[dict[str, Any]] = []
    seen_ids: set[str] = set()
    for raw_entry in credentials:
        credential_id = str(raw_entry.get("id", "")).strip()
        env_name = str(raw_entry.get("env", "")).strip()
        paths = raw_entry.get("paths")
        if not credential_id or not env_name or not isinstance(paths, list) or not paths:
            raise CredentialMatrixError("each credential requires id, env, and paths")
        if credential_id in seen_ids:
            raise CredentialMatrixError(f"duplicate credential id {credential_id!r}")
        if any(not isinstance(path_name, str) or path_name not in _SUPPORTED_PATHS for path_name in paths):
            raise CredentialMatrixError(f"{credential_id}: unsupported entitled path")
        if len(set(paths)) != len(paths):
            raise CredentialMatrixError(f"{credential_id}: duplicate entitled path")
        bearer = str(environment.get(env_name, "")).strip()
        if not bearer:
            raise CredentialMatrixError(f"{credential_id}: required credential environment is unavailable")
        for path_name in paths:
            try:
                _probe(base_url, path_name, bearer)
            except CredentialMatrixError as exc:
                raise CredentialMatrixError(f"{credential_id}: {exc}") from None
        seen_ids.add(credential_id)
        summary.append({"id": credential_id, "paths": paths})
    if not summary:
        raise CredentialMatrixError("credential inventory is empty")
    return {"status": "passed", "credentials": summary}


def _load_inventory(path: str) -> list[Mapping[str, Any]]:
    """Load a secret-free JSON credential inventory from an explicit path."""

    try:
        with open(path, encoding="utf-8") as inventory_file:
            decoded = json.load(inventory_file)
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise CredentialMatrixError(f"inventory could not be read ({type(exc).__name__})") from None
    entries = decoded.get("credentials") if isinstance(decoded, dict) else None
    if not isinstance(entries, list):
        raise CredentialMatrixError("inventory credentials array is required")
    return entries


def main(argv: list[str] | None = None) -> int:
    """Run the matrix from explicit deployment-session inputs."""

    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", default=os.environ.get("GPTADMIN_LIVE_BASE_URL", ""))
    parser.add_argument("--inventory", default=os.environ.get("GPTADMIN_CREDENTIAL_MATRIX_FILE", ""))
    args = parser.parse_args(argv)
    try:
        if not args.inventory:
            raise CredentialMatrixError("credential inventory path is required")
        result = run_credential_matrix(args.base_url, _load_inventory(args.inventory))
    except CredentialMatrixError as exc:
        print(json.dumps({"status": "failed", "error": str(exc)}, ensure_ascii=False))
        return 1
    print(json.dumps(result, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    sys.exit(main())
