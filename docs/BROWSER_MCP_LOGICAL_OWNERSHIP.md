# Browser MCP logical ownership in GPT Admin

Each device owns a logical `Browser` child target in GPT Admin Hub. The
browser executes in the device's **ordinary Chrome profile**, through the
existing Vibe extension's outbound WSS connection. GPT Admin does not install
another browser, SSH port-forward or per-device proxy process.

| Logical target | Physical relay child | Vibe session |
| --- | --- | --- |
| `mcp:shell:MacBook-Pro-User.local:Browser` | `shell:node-44/VibeBrowserMacM1` | `mac-m1` |
| `mcp:shell:mac-mini-2012.lan:Browser` | `shell:node-44/VibeChromeMacMini` | `mac-mini` |
| `mcp:shell:admin-node-100:Browser` | `shell:node-44/VibeChromeServer100` | `node-100` |

The authoritative route file is `<GPTADMIN_CONFIG_DIR>/browser-mcp-aliases.json` on
the Hub writer (currently `/etc/gptadmin/browser-mcp-aliases.json`). It contains
**only target names and identities**, no tokens, URLs with credentials or secrets.
Its `aliases` array uses `owner`, `ref` (must be `Browser`), `name`,
`upstream`, `upstream_ref`, and `hide_backing` fields. Backing children
must exist and be enabled. Both the owner and upstream must be registered
and approved in GPT Admin.

Tools/list and tools/call travel over GPT Admin's existing authenticated
Hub → node-44 GPTAdmin Node peer /mcp-relay transport, to the remote child.
The Hub maps the logical target to the pinned physical child **before**
forwarding. A stale standalone ShellMCP heartbeat must not remove aliases,
and the Hub must never fall back to a legacy queue for a logical Browser.
No additional bearer credential is copied to Chrome hosts. The alias target is routed using
`parent_server_id` (actual upstream) and `child_ref` while the exposed
`agent_id` identifies the logical owner. The Vibe HTTP transports on 44
use `--session <device>` and never pick the first connected browser.

The `set_remote` tool is excluded for a logical Browser and direct calls are
rejected. No agent should be given `set_remote` through this interface. If
the requested device is offline, return the real failure; never silently
switch to another browser. Root access to raw node-44 MCP children remains
administrator-only.

## Activation and acceptance

1. Publish three routes with `hide_backing:false` first.
2. Verify one real `list_pages` from each *logical* target, with different
   pages and the intended session.
3. Verify `set_remote` is not in the tool schema and its invocation fails.
4. Set `hide_backing:true` to omit the internal node-44 children from normal
   discovery. Existing running Vibe services are not removed.
5. Repeat one targeted discovery and real browser call after hiding.

The route file is read by the writer for discovery. Add new devices there
after ensuring the extension and a uniquely pinned Vibe session exist; no
new binaries or per-host forwarders are required. Revert by setting
`hide_backing:false`, then removing the alias entries if necessary.
The original physical children remain functional throughout.

## Implementation

`go-hub/internal/hub/browser_aliases.go`, with guards in
`go-hub/internal/hub/server.go` and tests in
`go-hub/internal/hub/browser_aliases_test.go`.

**Never** store extension UUIDs, Vibe secrets, bearer tokens, session cookies,
or authentication codes in route metadata, version control, logs or Agent Memory.
