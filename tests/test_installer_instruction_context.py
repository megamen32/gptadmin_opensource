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


@pytest.mark.parametrize("failure", [None, "missing_context", "http_428"])
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
        if request.method == "GET":
            online = any(method == "POST" for method, _, _ in calls)
            server = {**pending, "status": "online", "last_seen": time.time()} if online else pending
            body = {"servers": [server, foreign], "instructions": "Current local fixture instructions."}
            if failure != "missing_context":
                body["context"] = "current-caller-context"
        else:
            assert payload == {
                "target": "hub", "tool": "approve_pending_server",
                "arguments": {"server_id": "shell:local"}, "context": "current-caller-context",
            }
            if failure == "http_428":
                raise urllib.error.HTTPError(request.full_url, 428, "Precondition Required", {}, None)
            body = {"status": "completed", "response": {"ok": True}}
        return io.BytesIO(json.dumps(body).encode())

    monkeypatch.setattr(cli.urllib.request, "urlopen", urlopen)
    if failure == "missing_context":
        with pytest.raises(RuntimeError, match="instructions/context"):
            cli.maybe_autoapprove_local_shellmcp({"HUB_PORT": "9234"}, True, True)
    elif failure == "http_428":
        with pytest.raises(urllib.error.HTTPError):
            cli.maybe_autoapprove_local_shellmcp({"HUB_PORT": "9234"}, True, True)
    else:
        cli.maybe_autoapprove_local_shellmcp({"HUB_PORT": "9234"}, True, True)
    assert sum(method == "POST" for method, _, _ in calls) == (0 if failure == "missing_context" else 1)


@pytest.mark.parametrize("result", [
    {"isError": True, "structuredContent": {"secret": "private-provider-text"}},
    {"structuredContent": {"status": "failed", "executed": False, "error": {"code": "instructions_required"}}},
])
def test_candidate_rejects_tool_errors_without_provider_payload(result):
    with pytest.raises(RuntimeError) as error:
        cli._candidate_mcp_content(result)
    assert "private-provider-text" not in str(error.value)


@pytest.mark.parametrize("native", [False, True])
def test_systemd_client_fetches_context_before_single_dispatch(monkeypatch, native):
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
            assert request.get_header("X-gptadmin-context") == "runtime-context"
            assert json.loads(request.data) == payload
            body = {"returncode": 0, "stdout": "harmless"}
        return Response(json.dumps(body).encode())

    monkeypatch.setattr(module.urllib.request, "urlopen", urlopen)
    payload = ({"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": {
        "name": "shell_exec", "arguments": {"cmd": "printf harmless"},
    }} if native else {"target": "shell:local", "tool": "shell_exec", "arguments": {"cmd": "printf harmless"}})
    module.request("POST", "/server/local/mcp" if native else "/mcp-relay/call", "fixture-bearer", payload)
    assert [request.method for request in calls] == ["GET", "POST"]
