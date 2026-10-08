---
name: gptadmin-connect
description: Connect or reauthenticate the OAuth-protected GPTAdmin MCP hub without exposing the admin password, OAuth codes, or tokens in chat or config files.
---

# Connect GPTAdmin

GPTAdmin is a remote MCP server. This plugin adds the configured `gptadmin-hub`
HTTP MCP entry. It does not install a local copy of the Hub.

## First connection

1. Confirm the MCP URL is the user's public HTTPS Hub URL ending in `/mcp`.
   The packaged default is `https://mcp.bezrabotnyi.com/mcp`, the canonical
   OAuth resource for this installation. Use a different Hub URL only for a
   separate installation.
2. Let the MCP client perform OAuth discovery from the Hub: protected-resource
   metadata, authorization-server metadata, dynamic client registration, then
   Authorization Code + PKCE (S256).
3. When the browser consent page appears, tell the user that GPTAdmin is being
   authorized and that the admin password is entered in that browser page.
   Never ask the user to paste the password into chat, a skill, or a config.
4. Request `gptadmin.read gptadmin.exec offline_access` for a normal reconnect.
   The first two cover discovery and execution; `offline_access` keeps the
   connection refreshable.
5. After the callback, call `tools/list`, then run a read-only `discover`.
   Do not call the connection useful until an authenticated MCP call works.

## Verify after authorization

Run `discover` and read the response. Report the real target count and how many
targets are `online`. A successful TCP or TLS handshake proves nothing.

## Reauthentication

On `401`, `oauth_token_invalid_grant`, `oauth_refresh_token_missing`, or
`reauthentication_required`, stop before any write and re-run the browser OAuth
flow. Do not rotate, copy, decode, or print a token as a workaround.

If the client's saved entry still names a legacy `u-f….t.gptadmin.bezrabotnyi.com`
address, replace it with `https://mcp.bezrabotnyi.com/mcp` before reconnecting.
Those `u-f…` names are fallback transport addresses only, never an issuer,
resource, or identity.

## Issuer stability check

If the Hub advertises an issuer that contains a `t.gptadmin` FRP host instead
of the stable public hostname, the token will break whenever that tunnel
restarts. Report it and use the Hub's `HUB_PUBLIC_URL` / `PublicOrigin` setting
rather than hard-coding the tunnel host anywhere.

## Safety boundary

The Hub's OAuth consent and profile policy are authoritative. These skills are
workflow guidance, not an authorization bypass. Keep passwords, authorization
codes, access and refresh tokens, and signing keys out of messages, logs, task
files, screenshots, and generated configuration.