# GPTAdmin plugin (MiniMax Code)

Connects MiniMax Code to **your own** GPTAdmin Hub — a remote MCP server that
gives one agent authenticated access to every machine you point it at.

This plugin contains no hub address, no account, and no credential. It ships a
placeholder that you replace with the address of your own Hub.

---

## 1. Before you start

You need exactly one thing you may already have:

| Thing | What it is | How many |
|---|---|---|
| **GPTAdmin Hub** | An HTTPS service that speaks MCP and handles OAuth | Exactly one |
| **GPTAdmin ShellMCP** | The per-host agent that gives the Hub shell + child-MCP tools | One per machine you want to control |

You do **not** need to run either as part of this install. The plugin is just
an MCP entry plus instructions.

If you do not have a Hub yet, install the agent first and host the Hub yourself,
then come back to step 2.

---

## 2. Set your Hub URL — the one required edit

Open `servers.mcp.json` and replace the placeholder:

```json
"url": "https://gptadmin.example.com/mcp"
```

with your own Hub's public HTTPS `/mcp` endpoint:

```json
"url": "https://gptadmin.your-domain.tld/mcp"
```

That is the only file you must edit. Nothing else in this plugin is tied to a
particular Hub, host, tunnel, or deployment.

Two rules for that URL:

1. **It must be the issuer too.** If your Hub's OAuth metadata advertises a
   different address than the one you connect to — for example a throwaway
   `*.trycloudflare.com` or `u-xxxx.t.your-domain` tunnel — then saved tokens
   break every time that address changes. Configure the Hub to advertise one
   stable address and use that same address here.
2. **It must be reachable over plain HTTPS from this machine.** No `http://`,
   no LAN IP.

---

## 3. Authorize

The Hub is OAuth-protected. On first use the client follows OAuth discovery
(`/.well-known/oauth-protected-resource` → authorization server → dynamic
registration) and opens your browser to a consent page.

Enter the Hub admin password **in that browser page**. Never paste it into
chat, into `servers.mcp.json`, into a skill, or into a task file. Scopes
requested: `gptadmin.read`, `gptadmin.exec`, `offline_access`.

If you get `401`, `oauth_token_invalid_grant` or `reauthentication_required`,
just re-run the browser flow. Do not paste tokens anywhere to work around it.

---

## 4. Verify before trusting it

Three checks, in order. Do not skip to "it looks connected".

1. **`tools/list` returns.** Proves the MCP handshake and OAuth both work.
2. **Discovery lists your machines.** Expect your own hosts. A `stale` host has
   not checked in recently; a `failed` host will not work.
3. **One read-only command runs on one `online` host**, for example `uptime`.

Only the third check proves anything useful. A green connection banner does not.

---

## 5. Adding your first machine

The Hub can only reach a machine once that machine runs ShellMCP and is
approved:

1. Run the Hub's `deploy/install_shellmcp.sh` on the target machine. Confirm
   the repository and commit before running anything you were not expecting to.
2. Start its `shellmcp` service.
3. In discovery the machine appears as pending. Approve it with the exact id
   discovery reported — never a guessed one.
4. Re-run discovery and require `status: online` before you trust it.

Approval is a Hub-side decision and can be required by your Hub policy. If a
machine never appears, the agent is not installed or not reporting; approval
cannot fix that.

---

## 6. What this plugin cannot do

- It **cannot install anything by itself.** The MiniMax Code plugin format does
  not allow installers, so step 5 is something you ask an agent to do, and it
  must ask you first.
- It **cannot hold a shared Hub.** If you point it at someone else's Hub you are
  sending them your requests. Use your own.
- It **cannot survive an unreachable Hub.** Availability is entirely the Hub's
  problem. A tunnel address as issuer is the most common reason a working setup
  breaks "by itself".

---

## 7. Troubleshooting

| Symptom | Likely cause |
|---|---|
| Skills load, but the Hub exposes no tools | MCP entry exists but was never authorized — see step 3 |
| Agent reaches your machines anyway, without the Hub | The user has a leftover token or a second access path; the plugin's own channel is still broken |
| Browser opens, password rejected | Hub admin password, not your OS/SSH password |
| `401` right after authorizing | Token issued for a different issuer than the URL you connect to |
| Worked yesterday, not today | Issuer is a temporary tunnel address; make it stable (step 2) |
| Machine missing from discovery | ShellMCP not installed, not running, or not reporting |
| Machine `stale` | Relay has not checked in; calls may hang — prefer `online` |
| `502` / connection refused | Hub is restarting or down, not a plugin problem |

---

## 8. What's in the package

| Path | Purpose |
|---|---|
| `.minimax-plugin/plugin.json` | Plugin manifest |
| `servers.mcp.json` | The MCP entry — **edit this first** |
| `skills/gptadmin-connect/` | OAuth connect, re-auth, issuer checks |
| `skills/gptadmin-mcp-install/` | Discovery, schema-first calls, child MCPs |
| `skills/gptadmin-workflow/` | Profile routing, memory, adding a machine |
| `icon.png`, `icon-dark.png` | Icons |

## License

AGPL-3.0
