# Adding GPTAdmin MCP stdio servers

Use this from `~/gptadmin` on `roomhacker-server-100`.

For a Mac that has only ShellMCP installed, setup must receive the target
Hub's `MCP_RELAY_AGENT_TOKEN`; a locally generated `CTL_TOKEN` is not a relay
credential and will be rejected by that Hub:

```bash
sudo gptadmin setup --shellmcp --no-hub --hub-url https://hub.example \
  --mcp-relay-token "$MCP_RELAY_AGENT_TOKEN"
```

## One-command add + install

Remote HTTP/SSE MCP via `mcp-remote`:

```bash
./mcp-add my-remote --url https://example.com/mcp
```

Local stdio MCP package:

```bash
./mcp-add my-server -- npx -y some-mcp-package --flag value
```

The helper writes `/etc/gptadmin/mcp.json`, projects enabled entries into the
aggregate Go ShellMCP supervisor, restarts `shellmcp.service`, and prints the
supervisor status. It does not create one systemd unit per MCP server.

Use `--force` explicitly when replacing an existing entry. Do not pass secret
values through `--env`: they are persisted in the JSON registry. For a secret-
backed MCP, use a root-only environment file and a launcher under `/opt` that
reads it before executing the MCP server.

## Chrome DevTools example

```bash
./mcp-add chrome-devtools-88 \
  --agent-id ChromeDevTools-roomhacker-server-88 \
  --run-as-user roomhacker \
  --cwd /home/roomhacker \
  --stdio-format ndjson \
  --env NO_PROXY=127.0.0.1,localhost \
  --env no_proxy=127.0.0.1,localhost \
  --env CHROME_DEVTOOLS_MCP_NO_USAGE_STATISTICS=1 \
  -- npx -y chrome-devtools-mcp@latest --browser-url=http://127.0.0.1:9222 --no-usage-statistics
```

## Useful commands

```bash
python3 cli.py mcp list
sudo python3 cli.py mcp add NAME --install --status -- npx -y package
sudo python3 cli.py mcp install NAME
python3 cli.py mcp status NAME
python3 cli.py mcp cat NAME
```

Files:

- main config: `/etc/gptadmin/mcp.json`
- aggregate supervisor config: `/etc/gptadmin/mcp-supervisor.json`
- supervisor unit: `shellmcp.service`
