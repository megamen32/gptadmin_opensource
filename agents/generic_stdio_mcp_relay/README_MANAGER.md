# GPTAdmin MCP agent manager

`generic_stdio_mcp_relay.py` is the runtime for one stdio MCP server. `mcp_agent_manager.py` is the install/supervisor layer around it.

Manual starts can use compact command form:

```bash
generic_stdio_mcp_relay.py --hub ... --agent-id ... npx -y mcp-remote https://example.com/mcp
```

Managed services use `--agent-config FILE` to avoid service-file quoting problems and to keep env/cwd/token settings in one auditable JSON file.
The older `--config FILE --server NAME` mode remains only for compatibility with Claude-style `mcpServers` JSON.

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
