"""Conformance tests for the versioned third-party MCP extension manifest."""

from __future__ import annotations

import json
import os
import signal
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path

import pytest

import cli


ROOT = Path(__file__).resolve().parents[1]
FIXTURE = ROOT / "tests" / "fixtures" / "mcp-extension-example.json"
REFERENCE = ROOT / "tests" / "fixtures" / "mcp_extension_reference.py"
RELAY = ROOT / "agents" / "generic_stdio_mcp_relay" / "generic_stdio_mcp_relay.py"


def _free_port() -> int:
    """Return an unused loopback port for a disposable Hub."""

    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


def _rpc(url: str, name: str, arguments: dict) -> dict:
    """Call one authenticated Hub MCP tool and return structured content."""

    request = urllib.request.Request(
        url,
        data=json.dumps({"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": {"name": name, "arguments": arguments}}).encode(),
        headers={"Authorization": "Bearer ctl", "Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(request, timeout=5) as response:
        payload = json.loads(response.read().decode())
    if payload.get("error"):
        raise AssertionError(payload["error"])
    return payload["result"]["structuredContent"]


def test_reference_extension_manifest_passes_sdk_conformance() -> None:
    """A third-party-shaped manifest validates without Hub source changes."""

    manifest = cli._validate_mcp_extension_manifest(FIXTURE)
    assert manifest["schema"] == "gptadmin.mcp-extension/v1"
    assert manifest["id"] == "example.echo"
    assert manifest["capabilities"][0]["name"] == "echo"


def test_extension_manifest_rejects_missing_provenance_and_risk(tmp_path: Path) -> None:
    """Extensions must declare ownership, provenance and risk before use."""

    manifest = json.loads(FIXTURE.read_text(encoding="utf-8"))
    manifest.pop("provenance")
    manifest.pop("risk_level")
    path = tmp_path / "invalid.json"
    path.write_text(json.dumps(manifest), encoding="utf-8")
    with pytest.raises(ValueError, match="provenance"):
        cli._validate_mcp_extension_manifest(path)


def test_extension_sdk_documentation_describes_reference_contract() -> None:
    """The extension milestone must ship a usable SDK contract, not only a validator."""

    documentation = (ROOT / "docs" / "EXTENSION_SDK.md").read_text(encoding="utf-8")
    assert len(documentation.strip()) >= 500
    for required in ("gptadmin.mcp-extension/v1", "discover", "schema", "execute", "provenance", "risk_level"):
        assert required in documentation


def test_reference_extension_runs_the_mcp_lifecycle_without_hub_source_changes() -> None:
    """A third-party-shaped stdio adapter must implement initialize/list/call."""

    requests = [
        {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {}},
        {"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": {}},
        {"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": {"name": "echo", "arguments": {"message": "sdk-conformance"}}},
    ]
    completed = subprocess.run(
        [sys.executable, str(REFERENCE)],
        input="".join(json.dumps(request) + "\n" for request in requests),
        capture_output=True,
        text=True,
        cwd=ROOT,
        check=True,
        timeout=10,
    )
    responses = [json.loads(line) for line in completed.stdout.splitlines() if line.strip()]
    assert responses[0]["result"]["serverInfo"]["name"] == "example.echo"
    assert responses[1]["result"]["tools"][0]["name"] == "echo"
    assert responses[2]["result"]["content"][0]["text"] == "sdk-conformance"


def test_reference_extension_forwards_through_live_hub_discover_schema_execute(tmp_path: Path) -> None:
    """The reference adapter must cross the real relay and request-scoped Hub path."""

    port = _free_port()
    base_url = f"http://127.0.0.1:{port}"
    binary = tmp_path / "gptadmin-hub"
    subprocess.run(["go", "build", "-buildvcs=false", "-o", str(binary), "./cmd/gptadmin-hub"], cwd=ROOT / "go-hub", check=True, timeout=120)
    config_dir = tmp_path / "hub-config"
    config_dir.mkdir()
    env = os.environ.copy()
    env.update({
        "GPTADMIN_HUB_HOST": "127.0.0.1",
        "GPTADMIN_HUB_PORT": str(port),
        "CTL_TOKEN": "ctl",
        "MCP_RELAY_AGENT_TOKEN": "relay",
        "PUBLIC_ORIGIN": base_url,
        "MCP_RESOURCE": base_url,
        "GPTADMIN_CONFIG_DIR": str(config_dir),
        "GPTADMIN_ROOT": str(ROOT),
        "NO_PROXY": "localhost,127.0.0.1",
        "no_proxy": "localhost,127.0.0.1",
        "HTTP_PROXY": "",
        "HTTPS_PROXY": "",
        "ALL_PROXY": "",
    })
    hub = subprocess.Popen([str(binary)], cwd=ROOT, env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, start_new_session=True)
    relay = None
    try:
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            try:
                with urllib.request.urlopen(base_url + "/healthz", timeout=1) as response:
                    if response.status == 200:
                        break
            except (OSError, urllib.error.URLError):
                time.sleep(0.1)
        else:
            raise AssertionError("disposable Hub did not become ready")

        agent_config = tmp_path / "extension-agent.json"
        agent_config.write_text(json.dumps({
            "hub_url": base_url,
            "token": "relay",
            "agent_id": "example.echo",
            "name": "Example Echo",
            "command": sys.executable,
            "args": [str(REFERENCE)],
            "stdio_format": "ndjson",
        }), encoding="utf-8")
        relay = subprocess.Popen([sys.executable, str(RELAY), "--agent-config", str(agent_config)], cwd=ROOT, env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, start_new_session=True)

        deadline = time.monotonic() + 20
        while time.monotonic() < deadline:
            try:
                discovered = _rpc(base_url + "/mcp", "discover", {})
                if any(server.get("server_id") == "example.echo" for server in discovered.get("servers", [])):
                    break
            except (AssertionError, OSError, urllib.error.URLError, KeyError, TypeError):
                time.sleep(0.2)
        else:
            raise AssertionError("reference extension did not register with Hub")

        schema = _rpc(base_url + "/mcp", "schema", {"target": "example.echo"})
        assert any(tool.get("name") == "echo" for tool in schema["response"]["tools"])
        executed = _rpc(base_url + "/mcp", "execute", {"target": "example.echo", "tool": "echo", "arguments": {"message": "through-hub"}, "idempotency_key": "extension-sdk-e2e-1"})
        assert "through-hub" in json.dumps(executed, ensure_ascii=False)
    finally:
        for process in (relay, hub):
            if process is not None and process.poll() is None:
                os.killpg(os.getpgid(process.pid), signal.SIGTERM)
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    os.killpg(os.getpgid(process.pid), signal.SIGKILL)
                    process.wait(timeout=5)
