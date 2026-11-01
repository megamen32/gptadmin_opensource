---
name: gptadmin-connect
description: Connect and reauthenticate the OAuth-protected GPTAdmin remote MCP without exposing passwords or tokens in chat.
---

# Connect GPTAdmin

GPTAdmin is a remote MCP server. Installing this plugin adds the configured
`gptadmin` HTTP MCP entry; it does not install a local copy of the Hub.

## First connection

1. Confirm the MCP URL is the user's public HTTPS Hub URL ending in `/mcp`.
   The packaged default is `https://mcp.bezrabotnyi.com/mcp`; this is the
   canonical OAuth resource and issuer for the GPTAdmin installation. Use the
   user's own Hub URL only when it is a separate installation.
2. Let the MCP client perform OAuth discovery from the Hub. It should use the
   protected-resource and authorization-server metadata, dynamic registration,
   and Authorization Code + PKCE (S256).
3. When the browser consent page appears, tell the user that GPTAdmin is being
   authorized and that the admin password must be entered in that browser page.
   Never ask the user to paste the password into chat or a config file.
4. For the normal reconnect flow, request `gptadmin.read gptadmin.exec
   offline_access`. The first two scopes cover profile/discovery and execution;
   `offline_access` lets the client retain a refreshable connection.
5. After the callback, call `tools/list` and perform a read-only discovery. Do
   not claim the connection is useful until the authenticated MCP call works.

## Reauthentication

If the client reports `401`, `oauth_token_invalid_grant`,
`oauth_refresh_token_missing`, or `reauthentication_required`, stop before any
write. Re-run the client's OAuth reconnect flow in the browser. Do not rotate,
copy, decode, or print a token as a workaround.

If the saved entry for this managed installation still names a legacy
`u-f….t.became.bezrabotnyi.com` address, replace the entry with
`https://mcp.bezrabotnyi.com/mcp` before reconnecting. Here FRP `u-f…` names
are fallback transport addresses only; they are not the managed OAuth issuer,
resource, or identity. A separate Hub uses its own explicitly selected stable
HTTPS origin.

## Safety boundary

The Hub's OAuth consent and profile policy are authoritative. The plugin's
skills are workflow guidance, not an authorization bypass. Keep passwords,
authorization codes, access/refresh tokens, and signing keys out of messages,
logs, task files, screenshots, and generated config.
