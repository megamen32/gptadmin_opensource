---
name: gptadmin-connect
description: Configure, connect and reauthenticate the GPTAdmin Hub over its OAuth browser flow without exposing the admin password, OAuth codes, or tokens in chat or config files.
---

# Connect GPTAdmin

The plugin ships a **placeholder** Hub URL. Every user runs their own Hub at
their own address, so this skill's first job is to establish which address is
actually configured.

## Step 1 — establish the Hub URL

Read `servers.mcp.json` from the plugin package.

- If the URL is still the placeholder (`https://gptadmin.example.com/mcp`),
  stop and ask the user for their own Hub URL. Do not guess it, do not search
  the web for it, and do not fall back to any address found in old
  configuration, chat history, or another machine.
- Confirm the URL is HTTPS and ends in `/mcp`.

Only after the user supplies their address may anything else in this skill run.

## Step 2 — let the client do OAuth

Do not perform the OAuth handshake yourself. The MCP client owns it: discovery
of `/.well-known/oauth-protected-resource`, then the authorization server, then
dynamic client registration, then Authorization Code + PKCE (S256).

When the browser consent page opens, tell the user that GPTAdmin is being
authorized and that the Hub admin password is entered **in that browser page**.
Never ask for it in chat and never write it into a file.

Scopes requested: `gptadmin.read`, `gptadmin.exec`, `offline_access`.

## Step 3 — verify, in order

1. `tools/list` returns. Proves handshake + OAuth.
2. Discovery runs and lists real hosts. Read the actual counts; do not report
   "connected" without them.
3. One read-only command on one `online` host, e.g. `uptime`.

A successful TLS handshake or a green connection badge proves nothing about
whether the Hub is usable.

## Issuer check — the failure that looks like a ghost

If the Hub's OAuth metadata advertises an issuer that differs from the URL the
client connected to, saved tokens break the moment either address changes.

Typical bad shape: the client uses a stable domain, but the Hub answers with a
`temporary tunnel address` as its `issuer` and `resource`. Everything works,
then fails days later with no change on the user's side.

When you see that, tell the user the Hub must advertise one stable address as
its issuer, and that the plugin's `url` must match it. This is a Hub
configuration issue; changing the plugin URL alone will not fix it.

## Reauthentication

On `401`, `oauth_token_invalid_grant`, `oauth_refresh_token_missing`, or
`reauthentication_required`: stop before any write, and re-run the browser
flow. Never rotate, copy, decode, or paste a token as a workaround.

## Safety boundary

Hub consent and policy are authoritative. Keep passwords, authorization codes,
access and refresh tokens, and signing keys out of messages, logs, task files,
screenshots, and generated configuration — including when the user volunteers
them.
