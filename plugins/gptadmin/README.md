# GPTAdmin Codex plugin

Installing this plugin adds the remote GPTAdmin MCP server to Codex. It is an
OAuth connection to the GPTAdmin Hub, not a local mock or a copied bearer token.

On first use, the MCP client follows the Hub's OAuth discovery and opens the
browser consent page. Enter the GPTAdmin admin password in that page. Never put
the password, authorization code, access token, refresh token, or internal
signing secret into Codex chat, `.mcp.json`, a skill, or a task file.

The default endpoint is `https://mcp.bezrabotnyi.com/mcp`. This hostname is the
canonical GPTAdmin OAuth resource and issuer for this installation. For another
GPTAdmin installation, change only the URL in `.mcp.json` to that Hub's public
HTTPS `/mcp` endpoint before connecting. The Hub must advertise the standard
OAuth discovery endpoints:

- `/.well-known/oauth-protected-resource`
- `/.well-known/oauth-authorization-server`
- `/register`
- `/oauth/authorize`
- `/oauth/token`

The included skills define the safe workflow:

1. complete OAuth in the browser and verify the authenticated MCP connection;
2. inspect the active profile before choosing a target or tool;
3. discover a child MCP, read its schema, and install/use it through GPTAdmin;
4. use the memory MCP explicitly named by the active GPTAdmin profile.

OAuth success is not the same as business acceptance: after authorization,
verify `tools/list`, then run a read-only discovery before any write or command.

## Recovery and tunnel boundary

If a client for this managed installation is connected to an old
`u-f….t.became.bezrabotnyi.com` address, replace its MCP entry with the
canonical `/mcp` URL above and run the browser OAuth reconnect flow. Request the reconnect scopes
`gptadmin.read gptadmin.exec offline_access`; the password is entered only in
the browser consent page.

For this managed installation the `u-f…` FRP hostname is a legacy/fallback
transport address. It is not the OAuth issuer, resource, or user identity, and
must not be copied into OAuth metadata or used to work around a failed
reconnect. A separate Hub may select its own stable HTTPS origin. Keep
passwords, codes, and tokens out of `.mcp.json` and chat.
