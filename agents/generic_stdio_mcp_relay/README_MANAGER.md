# GPTAdmin MCP agent manager

`generic_stdio_mcp_relay.py` is the runtime for one stdio MCP server. `mcp_agent_manager.py` is the install/supervisor layer around it.

Backends:

- Linux: `systemd`
- macOS: `launchd`
- Windows: pure Task Scheduler + PowerShell restart loop, no NSSM

Example:

```bash
python3 mcp_agent_manager.py validate examples/gptadminmcp.mcp-agent.example.json
python3 mcp_agent_manager.py render --backend systemd examples/gptadminmcp.mcp-agent.example.json
```

Windows render prints the pure `schtasks /Create ...` command. The task starts `run_mcp_agent.ps1`, which restarts the relay if it exits.
