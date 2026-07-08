# GPTAdmin as a secure MCP proxy/relay

GPTAdmin can expose each registered MCP server through two public, authenticated compatibility layers:

1. **MCP-compatible endpoint** for MCP clients such as Claude Desktop, Codex, OpenCode, Cursor-like tools, or any client that can speak MCP over HTTP.
2. **OpenAPI Action endpoint** for ChatGPT Custom GPTs and other OpenAPI-action clients.

This lets you keep real MCP servers on private machines, behind NAT, behind stdio, or behind an internal tunnel, while giving external AI clients one HTTPS entry point with GPTAdmin authentication, audit logging, routing, queues, and output handling.

## Why use GPTAdmin as the front door

- One public HTTPS endpoint instead of exposing many MCP servers.
- Bearer/OAuth protection at the gateway.
- Per-server stable URLs and slugs.
- Works with stdio MCP, remote MCP, shell connectors, and internal GPTAdmin hub tools.
- OpenAPI schemas are generated from the upstream MCP server `tools/list` response, so the Action schema follows the real tool set.
- Calls are proxied only to the selected MCP server; a Custom GPT can see OpenMemory only, FileShare only, or any other single server without seeing the full GPTAdmin relay.

## URL layout

Assume your hub is published at:

```text
https://hub.example.com
```

Every registered MCP server gets a slug, visible in `/admin` and in `GET /mcp-relay/servers` under `meta.public_mcp_slug`.

| Purpose | URL |
|---------|-----|
| MCP-compatible endpoint | `https://hub.example.com/server/{slug}/mcp` |
| Server card / discovery | `https://hub.example.com/server/{slug}/card` |
| Health | `https://hub.example.com/server/{slug}/health` |
| OpenAPI Action schema | `https://hub.example.com/server/{slug}/actions/openapi.yaml` |
| OpenAPI Action schema, JSON | `https://hub.example.com/server/{slug}/actions/openapi.json` |
| OpenAPI Action tool call | `POST https://hub.example.com/server/{slug}/actions/tools/{tool_name}` |

The legacy `/agent/{slug}/...` route is kept as a compatibility alias, but new clients should use `/server/{slug}/...`.

## Example: expose only OpenMemory to a Custom GPT

Use this schema URL in the GPT editor Action import:

```text
https://hub.example.com/server/openmemory/actions/openapi.yaml
```

Configure authentication as an API key / bearer token and provide a GPTAdmin token accepted by your hub.

The generated schema will contain OpenMemory tools such as:

```text
openmemory_query
openmemory_store_project
openmemory_store
openmemory_list
```

It will not include GPTAdmin relay tools such as `call_mcp_tool` unless the selected server is the internal `hub` server.

A direct Action call looks like:

```bash
curl -fsS \
  -H 'Authorization: Bearer <GPTADMIN_TOKEN>' \
  -H 'Content-Type: application/json' \
  -d '{"query":"deployment notes","project_id":"gptadmin","k":3}' \
  https://hub.example.com/server/openmemory/actions/tools/openmemory_query
```

Response shape:

```json
{
  "server_id": "OpenMemory",
  "tool_name": "openmemory_query",
  "status": "completed",
  "response": {
    "content": [
      {"type": "text", "text": "..."}
    ]
  }
}
```

## Example: connect an MCP-compatible client

Use the per-server MCP URL when the client already speaks MCP:

```text
https://hub.example.com/server/openmemory/mcp
```

This endpoint accepts standard MCP JSON-RPC methods such as:

```text
initialize
tools/list
tools/call
resources/list
resources/read
prompts/list
prompts/get
```

For the full GPTAdmin hub surface, use:

```text
https://hub.example.com/server/hub/mcp
```

For a single upstream server, use its slug:

```text
https://hub.example.com/server/fileshare/mcp
https://hub.example.com/server/chromedevtools-admin-server-100/mcp
https://hub.example.com/server/openmemory/mcp
```

## How schemas are generated

When a client requests:

```text
GET /server/{slug}/actions/openapi.yaml
```

GPTAdmin resolves `{slug}` to exactly one registered MCP server, calls `tools/list`, and converts each MCP tool descriptor into an OpenAPI `POST /server/{slug}/actions/tools/{tool_name}` operation. The MCP `inputSchema` becomes the OpenAPI request body schema.

This means:

- adding a new MCP tool automatically updates the OpenAPI Action schema;
- removing a tool removes it from the generated schema;
- per-server Custom GPTs stay small and focused;
- users do not need to hand-maintain large OpenAPI files.

## Security notes

- Do not expose raw stdio MCP servers directly to the internet; put GPTAdmin in front.
- Use HTTPS for public hubs.
- Use strong bearer/OAuth credentials and rotate them if shared with a Custom GPT or MCP client.
- Prefer per-server OpenAPI schemas for Custom GPTs when the GPT only needs one capability.
- Use `/server/hub/mcp` or the GPTAdmin Apps SDK only when the client genuinely needs the full relay/admin surface.

## See also

- [API Reference](./API_REFERENCE.md)
- [Integrations](./INTEGRATIONS.md)
- [Security](./SECURITY_DOCS.md)
- [Hub](./HUB.md)
