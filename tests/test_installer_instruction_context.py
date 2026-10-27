"""Local client-contract regressions; executable candidate proof runs separately."""

from __future__ import annotations

import importlib.util
import io
import json
import time
import urllib.error
from pathlib import Path

import pytest

import cli


@pytest.mark.parametrize("failure", [
    None, "missing_context", "http_428", "legacy", "legacy_428",
    "new_missing_context", "schema_error", "unknown_schema", "malformed_receipt",
])
def test_local_autoapproval_carries_matching_discovery_context_once(monkeypatch, failure):
    identity = {"server_id": "local-signed-id", "public_key": "local-public-key"}
    monkeypatch.setattr(cli, "_load_local_shellmcp_identity", lambda *_args, **_kwargs: identity)
    monkeypatch.setattr(cli, "make_mcp_bearer_token", lambda *_args, **_kwargs: "fixture-bearer")
    monkeypatch.setenv("GPTADMIN_AUTO_APPROVE_LOCAL_SHELLMCP", "1")
    calls = []
    pending = {"server_id": "shell:local", "status": "awaiting_approval", "meta": identity}
    foreign = {"server_id": "shell:foreign", "status": "awaiting_approval", "meta": {
        "server_id": "foreign-id", "public_key": "foreign-public-key",
    }}

    def urlopen(request, timeout):
        assert request.get_header("Authorization") == "Bearer fixture-bearer"
        assert request.full_url.startswith("http://127.0.0.1:9234/")
        payload = json.loads(request.data) if request.data else None
        calls.append((request.method, request.full_url, payload))
        capability_probe = request.full_url.endswith("/mcp")
        if capability_probe:
            assert payload == {"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": {}}
            assert timeout <= 5
            # Exact inputSchema from public v1021 server.go appsSDKTools(),
            # lines 8971-8974; it is absent from that version's hubTools().
            schema = {"type": "object", "properties": {"target": {"type": "string"}}, "required": ["target"], "additionalProperties": False}
            if failure == "new_missing_context":
                schema["properties"]["context"] = {"type": "string"}
            if failure == "unknown_schema":
                schema.pop("additionalProperties")
            body = {"jsonrpc": "2.0", "id": 1, "result": {"tools": [{"name": "schema", "inputSchema": schema}]}}
            if failure == "schema_error":
                body["error"] = {"code": -32601, "message": "private-provider-text"}
        elif request.method == "GET":
            online = any(url.endswith("/mcp-relay/call") for _, url, _ in calls)
            server = {**pending, "status": "online", "last_seen": time.time()} if online else pending
            body = {"servers": [server, foreign]}
            if failure not in {"legacy", "legacy_428", "new_missing_context", "schema_error", "unknown_schema"}:
                body["instructions"] = "Current local fixture instructions."
                if failure != "missing_context":
                    body["context"] = 42 if failure == "malformed_receipt" else "current-caller-context"
        else:
            expected = {"target": "hub", "tool": "approve_pending_server", "arguments": {"server_id": "shell:local"}}
            if failure not in {"legacy", "legacy_428"}:
                expected["context"] = "current-caller-context"
            assert payload == expected
            if failure in {"http_428", "legacy_428"}:
                raise urllib.error.HTTPError(request.full_url, 428, "Precondition Required", {}, None)
            body = {"status": "completed", "response": {"ok": True}}
        return io.BytesIO(json.dumps(body).encode())

    monkeypatch.setattr(cli.urllib.request, "urlopen", urlopen)
    rejected = {"missing_context", "new_missing_context", "schema_error", "unknown_schema", "malformed_receipt"}
    if failure in rejected:
        with pytest.raises(RuntimeError) as caught:
            cli.maybe_autoapprove_local_shellmcp({"HUB_PORT": "9234"}, True, True)
        assert "private-provider-text" not in str(caught.value)
    elif failure in {"http_428", "legacy_428"}:
        with pytest.raises(urllib.error.HTTPError):
            cli.maybe_autoapprove_local_shellmcp({"HUB_PORT": "9234"}, True, True)
    else:
        cli.maybe_autoapprove_local_shellmcp({"HUB_PORT": "9234"}, True, True)
    assert sum(url.endswith("/mcp-relay/call") for _, url, _ in calls) == (0 if failure in rejected else 1)
    assert sum(url.endswith("/mcp") for _, url, _ in calls) == (
        1 if failure in {"legacy", "legacy_428", "new_missing_context", "schema_error", "unknown_schema"} else 0
    )


def test_legacy_runtime_verifier_uses_same_capability_proof_before_one_probe(monkeypatch):
    monkeypatch.setattr(cli, "_load_local_shellmcp_identity", lambda *_args, **_kwargs: {"public_key": "pk"})
    monkeypatch.setattr(cli, "_server_matches_local_shell_identity", lambda *_args: True)
    monkeypatch.setattr(cli, "make_mcp_bearer_token", lambda *_args, **_kwargs: "fixture-bearer")
    calls = []

    def http(base, method, path, **kwargs):
        assert base == "http://127.0.0.1:9234"
        assert kwargs["token"] == "fixture-bearer"
        calls.append(path)
        if path.startswith("/mcp-relay/servers"):
            return {"servers": [{"server_id": "shell:local", "status": "online"}]}
        if path == "/mcp":
            assert kwargs["max_response_bytes"] == 1024 * 1024
            assert kwargs["payload"] == {"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": {}}
            return {"jsonrpc": "2.0", "id": 1, "result": {"tools": [{
                "name": "schema", "inputSchema": {
                    "type": "object", "properties": {"target": {"type": "string"}}, "required": ["target"], "additionalProperties": False,
                },
            }]}}
        assert path == "/mcp-relay/call"
        assert "context" not in kwargs["payload"]
        marker = kwargs["payload"]["arguments"]["cmd"].split("'")[1]
        return {"returncode": 0, "stdout": marker}

    monkeypatch.setattr(cli, "_candidate_http", http)
    assert cli._require_real_local_shell_exec({"HUB_PORT": "9234"}, timeout_s=9)["status"] == "passed"
    assert calls == ["/mcp-relay/servers?detail=full", "/mcp", "/mcp-relay/call"]


@pytest.mark.parametrize("result", [
    {"isError": True, "structuredContent": {"secret": "private-provider-text"}},
    {"structuredContent": {"status": "failed", "executed": False, "error": {"code": "instructions_required"}}},
])
def test_candidate_rejects_tool_errors_without_provider_payload(result):
    with pytest.raises(RuntimeError) as error:
        cli._candidate_mcp_content(result)
    assert "private-provider-text" not in str(error.value)


@pytest.mark.parametrize("native", [False, True])
def test_systemd_client_reuses_credential_receipt_without_context_header(monkeypatch, native):
    path = Path(__file__).parent / "e2e" / "systemd" / "runtime_acceptance.py"
    spec = importlib.util.spec_from_file_location("runtime_context_contract", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    calls = []

    class Response(io.BytesIO):
        headers = {"Content-Type": "application/json"}

    def urlopen(request, timeout):
        assert request.get_header("Authorization") == "Bearer fixture-bearer"
        calls.append(request)
        if request.method == "GET":
            assert request.full_url == module.BASE + "/mcp-relay/servers?detail=full"
            body = {"instructions": "Current runtime fixture instructions.", "context": "runtime-context"}
        else:
            assert request.get_header("X-gptadmin-context") is None
            assert json.loads(request.data) == payload
            body = {"returncode": 0, "stdout": "harmless"}
        return Response(json.dumps(body).encode())

    monkeypatch.setattr(module.urllib.request, "urlopen", urlopen)
    payload = ({"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": {
        "name": "shell_exec", "arguments": {"cmd": "printf harmless"},
    }} if native else {"target": "shell:local", "tool": "shell_exec", "arguments": {"cmd": "printf harmless"}})
    module.request("POST", "/server/local/mcp" if native else "/mcp-relay/call", "fixture-bearer", payload)
    assert [request.method for request in calls] == ["GET", "POST"]
