# Generic stdio MCP Relay

Runs any local `mcpServers` stdio MCP server and exposes it to GPTAdmin via `/mcp-relay` long polling.

## PageAgent example

Create `page-agent.mcp.json` from the example and put your real key in it:

```json
{
  "mcpServers": {
    "page-agent": {
      "command": "npx",
      "args": ["-y", "@page-agent/mcp"],
      "env": {
        "LLM_BASE_URL": "https://dashscope.aliyuncs.com/compatible-mode/v1",
        "LLM_API_KEY": "sk-xxx",
        "LLM_MODEL_NAME": "qwen3.5-plus"
      }
    }
  }
}
```

Run:

```bash
export GPTADMIN_MCP_RELAY_TOKEN="$(ssh roomhacker@192.168.2.100 'cat /home/roomhacker/gptadmin/config/mcp_relay_agent_token')"
export GPTADMIN_MCP_RELAY_HUB="https://gptadminmcp.bezrabotnyi.com"
export GPTADMIN_MCP_RELAY_AGENT_ID="$(hostname -s)-page-agent"

python3 generic_stdio_mcp_relay.py --config page-agent.mcp.json --server page-agent
```

Then from ChatGPT/GPTAdmin:

1. `mcp_relay_agents`
2. `mcp_relay_tools_list` with `target="default"` or your agent id
3. `mcp_relay_call_tool`
