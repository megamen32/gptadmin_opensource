# GPTAdmin Codex plugin

Installing this plugin adds the remote GPTAdmin MCP server to Codex. It is an
OAuth connection to the GPTAdmin Hub, not a local mock or a copied bearer token.

On first use, the MCP client follows the Hub's OAuth discovery and opens the
browser consent page. Enter the GPTAdmin admin password in that page. Never put
the password, authorization code, access token, refresh token, or internal
signing secret into Codex chat, `.mcp.json`, a skill, or a task file.

The default endpoint is `https://became.bezrabotnyi.com/mcp`. For another
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
