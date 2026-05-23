# Mac Chrome MCP Relay Agent

This connects your logged-in Mac Chrome to `hub_proxy` through long polling.

## 1. On Mac: install dependency

```bash
python3 -m pip install --user playwright
```

No browser install is needed; it connects to your existing Chrome through CDP.

## 2. Start Chrome with remote debugging

Close Chrome first. Then run:

```bash
./start_logged_in_chrome.sh
```

This uses your real Chrome profile:

```text
~/Library/Application Support/Google/Chrome
```

If Chrome refuses because profile is already running, fully quit Chrome first:

```bash
osascript -e 'quit app "Google Chrome"'
```

## 3. Run agent

Copy the relay token from the server:

```bash
ssh admin@203.0.113.10 'cat /home/admin/gptadmin/config/mcp_relay_agent_token'
```

Then on Mac:

```bash
export GPTADMIN_MCP_RELAY_TOKEN='PASTE_TOKEN_HERE'
export GPTADMIN_MCP_RELAY_HUB='https://gptadminmcp.bezrabotnyi.com'
export GPTADMIN_MCP_RELAY_AGENT_ID="$(hostname -s)-chrome"
python3 mac_chrome_mcp_relay.py
```

## Exposed remote MCP tools

- `chrome_tabs`
- `chrome_open`
- `chrome_current_page`
- `chrome_click_text`
- `chrome_type`
- `chrome_press`
- `chrome_eval`

From ChatGPT/GPTAdmin use:

1. `mcp_relay_agents`
2. `mcp_relay_tools_list`
3. `mcp_relay_call_tool`
