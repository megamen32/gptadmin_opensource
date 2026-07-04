import asyncio
import time

import gptadmin_hub


def test_private_shell_agents_are_real_mcp_with_generic_tunnel_transport():
    saved = dict(gptadmin_hub.servers)
    try:
        gptadmin_hub.servers.clear()
        gptadmin_hub.servers["unit-shell"] = {
            "time": time.time(),
            "mode": "long_poll",
            "backend": "local",
            "base_url": "http://127.0.0.1:25900",
            "default_cwd": "/tmp",
        }
        agents = gptadmin_hub._all_public_agents(statuses=["all"])
        agent = next(item for item in agents if item["agent_id"] == "shell:unit-shell")
        assert agent["kind"] == "real_mcp"
        assert agent["transport"] == "mcp-tunnel"
        assert "tools/list" in agent["capabilities"]
        assert agent["meta"]["transport_layer"] == "mcp_tunnel"
        assert agent["meta"]["protocol_role"] == "mcp_server"
        assert agent["meta"]["tunnel"]["poll_mode"] is True
        assert agent["meta"]["tunnel"]["streamable_http_facade"] is True
    finally:
        gptadmin_hub.servers.clear()
        gptadmin_hub.servers.update(saved)


def test_tunnel_backed_mcp_resources_are_available():
    saved = dict(gptadmin_hub.servers)
    try:
        gptadmin_hub.servers.clear()
        gptadmin_hub.servers["unit-shell"] = {"time": time.time(), "mode": "webhook", "backend": "local"}
        listed = asyncio.run(gptadmin_hub._mcp_tunnel_request("shell:unit-shell", "resources/list", {}))
        uris = {item["uri"] for item in listed["resources"]}
        assert "mcp-tunnel://unit-shell/capabilities" in uris

        read = asyncio.run(
            gptadmin_hub._mcp_tunnel_request(
                "shell:unit-shell",
                "resources/read",
                {"uri": "mcp-tunnel://unit-shell/capabilities"},
            )
        )
        text = read["contents"][0]["text"]
        assert '"kind": "real_mcp"' in text
        assert '"transport_layer": "mcp_tunnel"' in text
    finally:
        gptadmin_hub.servers.clear()
        gptadmin_hub.servers.update(saved)


def test_tunnel_poll_registration_does_not_duplicate_public_agent():
    saved_servers = dict(gptadmin_hub.servers)
    saved_agents = dict(gptadmin_hub.mcp_relay_agents)
    try:
        gptadmin_hub.servers.clear()
        gptadmin_hub.mcp_relay_agents.clear()
        gptadmin_hub.servers["unit-shell"] = {"time": time.time(), "mode": "long_poll", "backend": "local"}
        gptadmin_hub.mcp_relay_agents["shell:unit-shell"] = {
            "agent_id": "shell:unit-shell",
            "name": "MCP: unit-shell",
            "transport": "mcp-tunnel",
            "capabilities": ["tools/list", "tools/call"],
            "meta": {"transport_layer": "mcp_tunnel"},
            "last_seen": time.time(),
        }
        agents = gptadmin_hub._all_public_agents(statuses=["all"])
        matches = [item for item in agents if item["agent_id"] == "shell:unit-shell"]
        assert len(matches) == 1
        assert matches[0]["kind"] == "real_mcp"
        assert matches[0]["transport"] == "mcp-tunnel"
    finally:
        gptadmin_hub.servers.clear()
        gptadmin_hub.servers.update(saved_servers)
        gptadmin_hub.mcp_relay_agents.clear()
        gptadmin_hub.mcp_relay_agents.update(saved_agents)
