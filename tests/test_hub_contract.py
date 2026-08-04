#!/usr/bin/env python3
"""Black-box contract tests for every GPTAdmin hub implementation.

The suite starts each command from ``HUB_CONTRACT_COMMANDS`` as an external
process and verifies the public HTTP, OpenAPI, and MCP JSON-RPC contract. It
does not import Go or Python hub internals, so a replacement implementation
must pass the same assertions before it can replace the active hub.

Example:
  HUB_CONTRACT_COMMANDS=$'cd go-hub && go run ./cmd/gptadmin-hub\npython3 /path/to/legacy/hub.py' \
    pytest -q tests/test_hub_contract.py
"""
from __future__ import annotations

import contextlib
import json
import os
import queue
import shlex
import shutil
import signal
import socket
import subprocess
import threading
import time
import urllib.error
import urllib.request
from dataclasses import dataclass
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any, Iterator

import pytest

import cli

ROOT = Path(__file__).resolve().parents[1]
DEFAULT_COMMANDS = ["go run ./cmd/gptadmin-hub"]
CONTRACT_TOKEN = "hub-contract-token"
RELAY_TOKEN = "hub-contract-relay-token"


def _contract_commands() -> list[str]:
    """Return configured hub commands, defaulting to the Go implementation."""
    raw = os.getenv("HUB_CONTRACT_COMMANDS", "").strip()
    if not raw:
        return DEFAULT_COMMANDS
    return [line.strip() for line in raw.splitlines() if line.strip() and not line.lstrip().startswith("#")]


CONTRACT_COMMANDS = _contract_commands()
pytestmark = pytest.mark.skipif(
    CONTRACT_COMMANDS == DEFAULT_COMMANDS and shutil.which("go") is None,
    reason="Go is unavailable for the default hub contract command",
)


def _free_port() -> int:
    """Allocate a free localhost TCP port for one isolated hub process."""
    with contextlib.closing(socket.socket(socket.AF_INET, socket.SOCK_STREAM)) as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


def _decode_json(body: bytes) -> dict[str, Any]:
    """Decode a JSON response, preserving non-JSON payloads for diagnostics."""
    try:
        value = json.loads(body.decode("utf-8") or "{}")
    except json.JSONDecodeError:
        return {"raw": body.decode("utf-8", "replace")}
    if isinstance(value, dict):
        return value
    return {"value": value}


def _request(
    method: str,
    url: str,
    *,
    token: str | None = None,
    payload: dict[str, Any] | None = None,
    headers: dict[str, str] | None = None,
    timeout_s: float = 5.0,
) -> tuple[int, dict[str, Any], dict[str, str]]:
    """Send one direct HTTP request without inheriting workstation proxy settings."""
    request_headers = {"Connection": "close"}
    if token:
        request_headers["Authorization"] = f"Bearer {token}"
    if headers:
        request_headers.update(headers)
    data = None
    if payload is not None:
        request_headers["Content-Type"] = "application/json"
        data = json.dumps(payload).encode("utf-8")
    request = urllib.request.Request(url, data=data, headers=request_headers, method=method)
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    try:
        with opener.open(request, timeout=timeout_s) as response:
            body = response.read()
            return response.status, _decode_json(body), dict(response.headers.items())
    except urllib.error.HTTPError as error:
        body = error.read()
        return error.code, _decode_json(body), dict(error.headers.items())


def _request_text(url: str) -> tuple[int, str]:
    """Fetch a text endpoint directly, returning HTTP errors as their body."""
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    request = urllib.request.Request(url, headers={"Connection": "close"})
    try:
        with opener.open(request, timeout=5) as response:
            return response.status, response.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as error:
        return error.code, error.read().decode("utf-8", "replace")


@dataclass
class HubProcess:
    """One externally started hub implementation under contract test."""

    command: str
    port: int
    process: subprocess.Popen[bytes]
    log_path: Path

    @property
    def base_url(self) -> str:
        """Return the localhost base URL for this implementation."""
        return f"http://127.0.0.1:{self.port}"

    def request(
        self,
        method: str,
        path: str,
        *,
        token: str | None = CONTRACT_TOKEN,
        payload: dict[str, Any] | None = None,
        headers: dict[str, str] | None = None,
    ) -> tuple[int, dict[str, Any], dict[str, str]]:
        """Call one hub endpoint."""
        return _request(method, f"{self.base_url}{path}", token=token, payload=payload, headers=headers)

    def rpc(self, path: str, method: str, params: dict[str, Any], request_id: int) -> dict[str, Any]:
        """Call an MCP JSON-RPC method and return its successful envelope."""
        status, body, _ = self.request(
            "POST",
            path,
            payload={"jsonrpc": "2.0", "id": request_id, "method": method, "params": params},
        )
        assert status == 200, body
        assert body.get("jsonrpc") == "2.0", body
        assert body.get("id") == request_id, body
        assert "error" not in body, body
        return body["result"]

    def stop(self) -> None:
        """Stop the process and leave its log available on a failure."""
        if self.process.poll() is not None:
            return
        try:
            os.killpg(os.getpgid(self.process.pid), signal.SIGTERM)
        except ProcessLookupError:
            return
        try:
            self.process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            os.killpg(os.getpgid(self.process.pid), signal.SIGKILL)
            self.process.wait(timeout=5)


def _command_id(command: str) -> str:
    """Create a compact pytest parameter id from a process command."""
    return Path(shlex.split(command)[0]).name


@pytest.fixture(scope="session")
def default_hub_contract_command(tmp_path_factory: pytest.TempPathFactory) -> str:
    """Build the default Go Hub once outside each process readiness window."""
    binary = tmp_path_factory.mktemp("hub-contract") / "gptadmin-hub-contract"
    subprocess.run(
        ["go", "build", "-buildvcs=false", "-o", str(binary), "./cmd/gptadmin-hub"],
        cwd=str(ROOT / "go-hub"),
        check=True,
        timeout=120,
    )
    return str(binary)


@pytest.fixture(params=CONTRACT_COMMANDS, ids=_command_id)
def hub_contract(
    request: pytest.FixtureRequest,
    tmp_path: Path,
) -> Iterator[HubProcess]:
    """Start one implementation with isolated state and deterministic credentials."""
    command = str(request.param)
    if command == DEFAULT_COMMANDS[0]:
        command = str(request.getfixturevalue("default_hub_contract_command"))
    port = _free_port()
    state_dir = tmp_path / "state"
    log_path = tmp_path / "hub.log"
    env = os.environ.copy()
    env.update(
        {
            "CTL_TOKEN": CONTRACT_TOKEN,
            "GPTADMIN_CTL_TOKEN": CONTRACT_TOKEN,
            "OAUTH_CLIENT_SECRET": "hub-contract-oauth-secret",
            "MCP_RELAY_AGENT_TOKEN": RELAY_TOKEN,
            "MCP_BRIDGE_KEY": CONTRACT_TOKEN,
            "GPTADMIN_HUB_HOST": "127.0.0.1",
            "HUB_HOST": "127.0.0.1",
            "GPTADMIN_HUB_PORT": str(port),
            "HUB_PORT": str(port),
            "PORT": str(port),
            "GPTADMIN_ROOT": str(ROOT),
            "GPTADMIN_CONFIG_DIR": str(state_dir),
            "GPTADMIN_ARTIFACT_DIR": str(tmp_path / "artifacts"),
            "GPTADMIN_OUTPUT_DIR": str(tmp_path / "outputs"),
            "GPTADMIN_HOME": str(tmp_path / "home"),
            "GPTADMIN_REGISTRY_STATE_FILE": str(state_dir / "registry_state.json"),
            "GPTADMIN_FAILOVER_CONFIG_FILE": str(state_dir / "failover_config.json"),
            "GPTADMIN_FAILOVER_STATE_FILE": str(state_dir / "failover_state.json"),
            "GPTADMIN_FAILOVER_RECLAIM_COMMAND_FILE": str(state_dir / "failover_reclaim_command.json"),
            "PUBLIC_ORIGIN": f"http://127.0.0.1:{port}",
            "MCP_RESOURCE": f"http://127.0.0.1:{port}",
            "MCP_RELAY_DEFAULT_TIMEOUT": "1",
            "MCP_RELAY_POLL_MAX_TIMEOUT": "1",
            "NO_PROXY": "localhost,127.0.0.1",
            "no_proxy": "localhost,127.0.0.1",
            "HTTP_PROXY": "",
            "HTTPS_PROXY": "",
            "ALL_PROXY": "",
        }
    )
    state_dir.mkdir()
    with log_path.open("wb") as log:
        process = subprocess.Popen(
            command,
            shell=True,
            cwd=str(ROOT),
            env=env,
            stdout=log,
            stderr=subprocess.STDOUT,
            start_new_session=True,
        )

    hub = HubProcess(command=command, port=port, process=process, log_path=log_path)
    deadline = time.monotonic() + 15
    last_error = "not started"
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise AssertionError(f"hub exited early rc={process.returncode}; log:\n{log_path.read_text(errors='replace')}")
        try:
            status, body, _ = hub.request("GET", "/version", token=None)
            if status == 200 and body.get("ok") is True:
                break
            last_error = f"HTTP {status}: {body}"
        except Exception as error:  # The process can still be binding its port.
            last_error = repr(error)
        time.sleep(0.1)
    else:
        hub.stop()
        raise AssertionError(f"hub did not become ready: {last_error}; log:\n{log_path.read_text(errors='replace')}")

    try:
        yield hub
    finally:
        hub.stop()


def _structured_content(result: dict[str, Any]) -> dict[str, Any]:
    """Extract the Apps SDK structured payload from an MCP tool result."""
    structured = result.get("structuredContent")
    assert isinstance(structured, dict), result
    return structured


def test_hub_contract_health_and_auth(hub_contract: HubProcess) -> None:
    """Expose version/health publicly while guarding the relay API with Bearer auth."""
    status, version, _ = hub_contract.request("GET", "/version", token=None)
    assert status == 200
    assert version.get("ok") is True
    assert isinstance(version.get("name"), str) and version["name"]

    status, health, _ = hub_contract.request("GET", "/healthz", token=None)
    assert status == 200
    assert health == {"ok": True}

    status, body, _ = hub_contract.request("GET", "/mcp-relay/servers", token=None)
    assert status == 401
    assert body


def test_cli_issued_bearer_is_accepted_by_a_real_hub_process(hub_contract: HubProcess) -> None:
    """Keep CLI signing claims aligned with the Hub's audience/resource checks."""
    token = cli.make_mcp_bearer_token(
        {
            "HUB_PUBLIC_URL": hub_contract.base_url,
            "OAUTH_CLIENT_SECRET": "hub-contract-oauth-secret",
        },
        "custom-gpt-contract",
        access_mode="readonly",
    )
    status, body, _ = hub_contract.request("GET", "/mcp-relay/servers", token=token)
    assert status == 200, body
    assert isinstance(body.get("servers"), list)


def test_connection_manifest_drives_safe_first_mcp_action(hub_contract: HubProcess) -> None:
    """A generic client can consume the manifest and call the safe demo tool."""
    status, manifest, _ = hub_contract.request("GET", "/connect.json", token=None)
    assert status == 200, manifest
    config = manifest["client_configs"]["custom"]
    first_action = config["first_action"]
    assert config["url"] == f"{hub_contract.base_url}/mcp"
    assert first_action == {"method": "tools/call", "tool": "demo", "arguments": {}}
    result = hub_contract.rpc(
        "/mcp",
        first_action["method"],
        {"name": first_action["tool"], "arguments": first_action["arguments"]},
        request_id=17,
    )
    structured = _structured_content(result)
    assert structured["status"] == "ok"
    assert structured["access_mode"] == "full"


def test_hub_contract_relay_and_openapi(hub_contract: HubProcess) -> None:
    """Verify selected-target REST relay behavior and the published Action schema."""
    status, servers_body, _ = hub_contract.request("GET", "/mcp-relay/servers")
    assert status == 200
    servers = servers_body.get("servers")
    assert isinstance(servers, list) and servers
    assert any(server.get("server_id") == "hub" for server in servers if isinstance(server, dict))
    hub_server = next(server for server in servers if server.get("server_id") == "hub")
    assert set(hub_server) <= {"server_id", "name", "kind", "status"}

    status, detailed_body, _ = hub_contract.request("GET", "/mcp-relay/servers?detail=full")
    assert status == 200
    detailed_hub = next(server for server in detailed_body["servers"] if server.get("server_id") == "hub")
    assert "meta" in detailed_hub
    assert "capabilities" in detailed_hub

    status, tools_body, _ = hub_contract.request("POST", "/mcp-relay/tools", payload={"target": "hub"})
    assert status == 200
    assert tools_body.get("server_id") == "hub"
    tools = tools_body.get("response", {}).get("tools")
    assert isinstance(tools, list) and any(tool.get("name") == "discover" for tool in tools)
    schema_version = tools_body["response"].get("schema_version")
    schema_digest = tools_body["response"].get("schema_digest_sha256")
    assert schema_version == "gptadmin.mcp-schema/v1"
    assert isinstance(schema_digest, str) and len(schema_digest) == 64

    status, executed, _ = hub_contract.request(
        "POST",
        "/mcp-relay/call",
        payload={
            "target": "hub",
            "tool_name": "demo",
            "arguments": {},
            "schema_version": schema_version,
            "schema_digest_sha256": schema_digest,
        },
    )
    assert status == 200, executed
    assert executed.get("status") == "completed", executed

    status, stale, _ = hub_contract.request(
        "POST",
        "/mcp-relay/call",
        payload={
            "target": "hub",
            "tool_name": "demo",
            "arguments": {},
            "schema_version": schema_version,
            "schema_digest_sha256": "0" * 64,
        },
    )
    assert status == 409, stale
    assert stale.get("error", {}).get("code") == "schema_mismatch", stale

    for path, payload in (
        ("/mcp-relay/tools", {"target": "default"}),
        ("/mcp-relay/call", {"target": "default", "tool_name": "shell_exec"}),
        ("/mcp-relay/tools", {"target": "unknown-contract-target"}),
    ):
        status, body, _ = hub_contract.request("POST", path, payload=payload)
        assert status in {400, 404}, body
        assert "job_id" not in body, body

    status, schema = _request_text(f"{hub_contract.base_url}/actions/openapi.yaml")
    assert status == 200
    assert "required: [target]" in schema
    assert 'Never use target="default"' in schema
    assert "default: default" not in schema
    assert "/webhooks/v1/{route}" not in schema
    assert "/webhook-jobs/{job_id}" not in schema
    assert "/webhook-routes/{route}" not in schema
    assert "schema_digest_sha256" in schema


def test_hub_contract_global_mcp(hub_contract: HubProcess) -> None:
    """Exercise the Apps SDK tools through the standard MCP JSON-RPC endpoint."""
    initialized = hub_contract.rpc("/mcp", "initialize", {}, 1)
    assert initialized.get("protocolVersion")
    assert initialized.get("serverInfo", {}).get("name")

    tools_result = hub_contract.rpc("/mcp", "tools/list", {}, 2)
    tools = tools_result.get("tools")
    assert isinstance(tools, list)
    names = {tool.get("name") for tool in tools if isinstance(tool, dict)}
    assert {"discover", "schema", "execute"} <= names
    discover_schema = next(tool for tool in tools if tool.get("name") == "discover")
    assert discover_schema["inputSchema"]["properties"]["detail"]["enum"] == ["full"]

    list_servers = hub_contract.rpc(
        "/mcp",
        "tools/call",
        {"name": "discover", "arguments": {}},
        3,
    )
    servers = _structured_content(list_servers).get("servers")
    assert isinstance(servers, list)
    assert any(server.get("server_id") == "hub" for server in servers if isinstance(server, dict))

    detailed = hub_contract.rpc(
        "/mcp",
        "tools/call",
        {"name": "discover", "arguments": {"detail": "full"}},
        31,
    )
    detailed_servers = _structured_content(detailed).get("servers")
    assert any("meta" in server for server in detailed_servers if isinstance(server, dict))

    rejected = hub_contract.rpc(
        "/mcp",
        "tools/call",
        {"name": "schema", "arguments": {"target": "default"}},
        4,
    )
    payload = _structured_content(rejected)
    assert payload.get("status") == "failed"
    assert payload.get("error", {}).get("status_code") == 400


def test_hub_contract_per_server_mcp_and_action_proxy(hub_contract: HubProcess) -> None:
    """Verify the MCP and generated OpenAPI proxy for the built-in hub server."""
    status, schema = _request_text(f"{hub_contract.base_url}/server/hub/actions/openapi.yaml")
    assert status == 200
    assert "/server/hub/actions/tools/discover" in schema

    tools_result = hub_contract.rpc("/server/hub/mcp", "tools/list", {}, 5)
    tools = tools_result.get("tools")
    assert isinstance(tools, list)
    assert any(tool.get("name") == "discover" for tool in tools if isinstance(tool, dict))

    status, action, _ = hub_contract.request("POST", "/server/hub/actions/tools/discover", payload={})
    assert status == 200
    assert action.get("server_id") == "hub"
    assert action.get("status") == "completed"


def test_hub_contract_profile_binding_enforces_mcp_tool_policy(hub_contract: HubProcess) -> None:
    """Exercise profile CRUD, managed-token binding and policy filtering in a real Hub process."""
    profile = {
        "id": "process-readonly",
        "name": "Process readonly",
        "access_mode": "full",
        "approval_mode": "ask_before_write",
        "allowed_targets": ["hub"],
        "allowed_tools": ["discover"],
    }
    status, created, _ = hub_contract.request(
        "PUT",
        "/admin/api/access-profiles/process-readonly",
        payload=profile,
        headers={"If-Match": "*"},
    )
    assert status == 200, created
    assert created.get("id") == "process-readonly"
    assert created.get("approval_mode") == "ask_before_write"

    status, issued, _ = hub_contract.request(
        "POST",
        "/admin/api/mcp/issue-token",
        payload={"client_id": "process-profile-client", "ttl_days": 1},
    )
    assert status == 200, issued
    token_id = issued.get("token_id")
    access_token = issued.get("access_token")
    assert token_id and access_token

    status, binding, _ = hub_contract.request(
        "PUT",
        f"/admin/api/client-bindings/{token_id}",
        payload={"profile_id": "process-readonly"},
    )
    assert status == 200, binding

    status, listed, _ = hub_contract.request(
        "POST",
        "/mcp",
        token=access_token,
        payload={"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": {}},
    )
    assert status == 200, listed
    assert listed.get("error") is None, listed
    names = {tool.get("name") for tool in listed.get("result", {}).get("tools", [])}
    assert "discover" in names
    assert "execute" not in names

    status, allowed, _ = hub_contract.request(
        "POST",
        "/mcp",
        token=access_token,
        payload={"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": {"name": "discover", "arguments": {}}},
    )
    assert status == 200, allowed
    assert allowed.get("error") is None, allowed
    assert "servers" in json.dumps(allowed)

    status, forbidden, _ = hub_contract.request(
        "POST",
        "/mcp",
        token=access_token,
        payload={
            "jsonrpc": "2.0",
            "id": 3,
            "method": "tools/call",
            "params": {"name": "execute", "arguments": {"target": "hub", "tool": "discover"}},
        },
    )
    assert status == 200, forbidden
    assert forbidden.get("error") is not None, forbidden


def test_hub_contract_webhook_route_job_and_callback_through_process(hub_contract: HubProcess) -> None:
    """Exercise webhook ingress, policy-dispatched MCP work and callback delivery."""

    callback_bodies: queue.Queue[bytes] = queue.Queue()

    class CallbackHandler(BaseHTTPRequestHandler):
        def do_POST(self) -> None:  # noqa: N802 - stdlib handler contract
            length = int(self.headers.get("Content-Length", "0"))
            callback_bodies.put(self.rfile.read(length))
            self.send_response(204)
            self.end_headers()

        def log_message(self, *_args: object) -> None:
            return

    callback_server = ThreadingHTTPServer(("127.0.0.1", 0), CallbackHandler)
    callback_thread = threading.Thread(target=callback_server.serve_forever, daemon=True)
    callback_thread.start()
    route = {
        "id": "process-hook",
        "token": "process-hook-token",
        "action": {"kind": "mcp", "target": "hub", "tool": "demo", "arguments": {}},
        "callback": {"url": f"http://127.0.0.1:{callback_server.server_port}", "token": "callback-token"},
    }
    try:
        status, created, _ = hub_contract.request("POST", "/webhook-routes", payload=route)
        assert status == 201, created
        assert created == {"id": "process-hook", "kind": "mcp", "target": "hub", "tool": "demo", "auth_mode": "token", "callback_configured": True}

        status, accepted, _ = hub_contract.request(
            "POST",
            "/webhooks/v1/process-hook",
            token="process-hook-token",
            payload={"event": "push", "value": "safe"},
        )
        assert status == 202, accepted
        job_id = accepted["job_id"]

        job = {}
        for _ in range(50):
            status, job, _ = hub_contract.request("GET", f"/webhook-jobs/{job_id}", token="process-hook-token")
            assert status == 200, job
            if job.get("status") == "failed" or (
                job.get("status") == "completed" and job.get("callback_status") == "delivered"
            ):
                break
            time.sleep(0.1)
        assert job.get("status") == "completed", job
        assert job.get("callback_status") == "delivered", job
        callback_body = callback_bodies.get(timeout=2)
        assert json.loads(callback_body)["job_id"] == job_id
        assert "process-hook-token" not in callback_body.decode("utf-8")
    finally:
        callback_server.shutdown()
        callback_thread.join(timeout=5)
        callback_server.server_close()
