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
3. use `search` to find caller-visible tools and settings, then run concise
   `exec` JavaScript for calls and aggregation;
4. use the memory MCP explicitly named by the active GPTAdmin profile.

OAuth success is not the same as business acceptance: after authorization,
verify `tools/list`, then run a read-only `search` before any write or command.
`discover` and `schema` remain available for read-only compatibility. The public
direct `execute(target, tool, args)` route is retired; use `search` plus
`exec` instead.

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

## Compact native results

The plugin requests `X-GPTAdmin-Result-Mode: text`. GPTAdmin sends one
representation when text and structuredContent contain exactly the same JSON.
Media, errors, distinct content and widget data keep their normal contracts.
The public tool catalog is `ui`, `discover`, `schema`, `search`, `exec`,
`job`, and `expand`. Use `search` results to obtain a concise schema and a
ready-to-use JavaScript call or scoped handle. `exec` keeps intermediate
data in the sandbox and returns only the selected result. Use `job` or `expand`
to inspect an existing operation receipt; never replay an uncertain write.

This package currently describes and configures the Codex plugin entrypoint.
The MCP facade itself is client-independent, but this repository does not
claim certification or end-to-end testing for a fixed number of other clients.
Each client integration must be inventoried and verified separately.

Upgrade through `codex plugin add gptadmin@gptadmin-local`. Existing chats may
retain their loaded tool catalog; validate a fresh native MCP connection.
Keep the separate legacy gptadmin MCP entry disabled when this plugin supplies
the same connection, to avoid duplicate tool catalogs.

## Current profile context

`discover`, selected `schema`, and search results return the current effective
profile instructions and a `context` receipt. Pass the context when refreshing
read-only search/discovery/schema results or when it is required by the current
client contract. `exec` uses the authenticated caller context for each
nested operation.
A missing or stale receipt produces `instructions_required` and `executed:false`
before execution; read the returned text and make the intended call again. Never
interpret an unrelated timeout as proof that an operation did not run.

This package includes the portable Agent Plugins 1.0 root `plugin.json` and
`mcp.json`. The existing `.codex-plugin/plugin.json` and `.mcp.json` remain the
Codex-specific compatibility entrypoints, at the same package version. Portable
MCP uses `streamable-http` / `headers`; the native compatibility file keeps its
client-specific `http` / `http_headers` spelling. Neither contains credentials.

Updating a package does not retroactively reload an existing conversation.
The Hub gate enforces current delivery independently of plugin activation. The
skill helps the client retain actual instructions, not just a receipt. Per-host
plugin reload and after-compaction hooks depend on the host; this package does
not claim a universal hook or automatic changes to every local project.

An optional project instruction, added only by the project owner, can say:

> Before GPTAdmin work, read the current profile returned by search or the
> compatibility discovery/schema tools. Retain its text and context together.
> On `instructions_required`, refresh them; inspect job/expand receipts before
> repeating operations with an uncertain outcome.
