import os

from fastapi.testclient import TestClient

os.environ.setdefault("GPTADMIN_AUDIT_LOG", "/tmp/gptadmin-test-audit.log")
os.environ.setdefault("CTL_TOKEN", "unit-test-ctl-token")
os.environ.setdefault("GPTADMIN_UPDATE_TOKEN", "unit-test-update-token")
os.environ.setdefault("SHELLMCP_UPDATE_TOKEN", "unit-test-shell-update-token")
os.environ.setdefault("MCP_RELAY_AGENT_TOKEN", "unit-test-relay-token")

import gptadmin_hub  # noqa: E402


def _client() -> TestClient:
    return TestClient(gptadmin_hub.app)


def _auth_headers() -> dict[str, str]:
    return {"Authorization": f"Bearer {gptadmin_hub.CTL_TOKEN}"}


def _jsonrpc_call(client: TestClient, name: str, arguments: dict) -> dict:
    token = gptadmin_hub._sign_jwt({
        "sub": "unit-test",
        "client_id": "mcp-connector-compat-test",
        "scope": "gptadmin.read gptadmin.exec",
    })
    response = client.post(
        "/mcp",
        headers={"Authorization": f"Bearer {token}", "Content-Type": "application/json"},
        json={"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": {"name": name, "arguments": arguments}},
    )
    assert response.status_code == 200, response.text
    payload = response.json()
    assert "error" not in payload, payload
    return payload["result"]["structuredContent"]


def test_actions_openapi_exposes_camelcase_and_snake_case_connector_operations():
    response = _client().get("/actions/openapi.yaml")
    assert response.status_code == 200
    text = response.text
    for op in ("listMcpAgents", "listMcpTools", "callMcpTool", "getMcpJob"):
        assert f"operationId: {op}" in text
    for op in ("list_mcp_agents", "list_mcp_tools", "call_mcp_tool", "get_mcp_job"):
        assert f"operationId: {op}" in text
    for path in ("/mcp-relay/list_mcp_agents", "/mcp-relay/list_mcp_tools", "/mcp-relay/call_mcp_tool", "/mcp-relay/get_mcp_job/{job_id}"):
        assert path in text


def test_snake_case_rest_aliases_cover_connector_call_flow():
    client = _client()
    headers = _auth_headers()

    agents = client.get("/mcp-relay/list_mcp_agents", headers=headers)
    assert agents.status_code == 200, agents.text
    assert any(item["agent_id"] == "hub" for item in agents.json()["agents"])

    tools = client.post("/mcp-relay/list_mcp_tools", headers=headers, json={"target": "hub"})
    assert tools.status_code == 200, tools.text
    tool_names = {item["name"] for item in tools.json()["response"]["tools"]}
    assert "list_servers" in tool_names

    call = client.post(
        "/mcp-relay/call_mcp_tool",
        headers=headers,
        json={"target": "hub", "tool_name": "list_servers", "arguments": {}},
    )
    assert call.status_code == 200, call.text
    body = call.json()
    assert body["agent_id"] == "hub"
    assert body["status"] == "completed"
    assert "structuredContent" in body["response"]


def test_mcp_jsonrpc_snake_case_tools_call_flow():
    client = _client()
    agents = _jsonrpc_call(client, "list_mcp_agents", {})
    assert any(item["agent_id"] == "hub" for item in agents["agents"])

    tools = _jsonrpc_call(client, "list_mcp_tools", {"target": "hub"})
    assert tools["agent_id"] == "hub"
    assert any(item["name"] == "list_servers" for item in tools["response"]["tools"])

    call = _jsonrpc_call(client, "call_mcp_tool", {"target": "hub", "tool_name": "list_servers", "arguments": {}})
    assert call["agent_id"] == "hub"
    assert call["status"] == "completed"
    assert "structuredContent" in call["response"]



def test_live_mcp_connector_shell_exec_smoke():
    """Optional live smoke: exercises public/local REST snake alias against a real shell agent.

    Enable with GPTADMIN_MCP_LIVE_TESTS=1 and provide:
    HUB_URL, CTL_TOKEN, and optional GPTADMIN_MCP_LIVE_TARGET.
    """
    import pytest
    import requests

    if os.environ.get("GPTADMIN_MCP_LIVE_TESTS") != "1":
        pytest.skip("set GPTADMIN_MCP_LIVE_TESTS=1 to run live connector smoke")
    base = os.environ.get("HUB_URL", "http://127.0.0.1:9001").rstrip("/")
    token = os.environ.get("CTL_TOKEN")
    assert token, "CTL_TOKEN is required for live MCP connector smoke"
    target = os.environ.get("GPTADMIN_MCP_LIVE_TARGET", "shell:admin-server-100")
    headers = {"Authorization": f"Bearer {token}"}

    agents = requests.get(f"{base}/mcp-relay/list_mcp_agents", headers=headers, timeout=15)
    assert agents.status_code == 200, agents.text
    assert any(item.get("agent_id") == target for item in agents.json().get("agents", []))

    tools = requests.post(f"{base}/mcp-relay/list_mcp_tools", headers=headers, json={"target": target}, timeout=20)
    assert tools.status_code == 200, tools.text
    assert any(item.get("name") == "shell_exec" for item in tools.json().get("response", {}).get("tools", []))

    call = requests.post(
        f"{base}/mcp-relay/call_mcp_tool",
        headers=headers,
        json={"target": target, "tool_name": "shell_exec", "arguments": {"cmd": "printf MCP_LIVE_TEST_OK", "timeout": 5}, "timeout": 30},
        timeout=40,
    )
    assert call.status_code == 200, call.text
    result = call.json().get("response", {}).get("structuredContent", {}).get("result", {})
    assert result.get("returncode") == 0
    assert "MCP_LIVE_TEST_OK" in (result.get("stdout") or "")
