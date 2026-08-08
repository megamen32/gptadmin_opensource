# GPTADMIN Plugin P0.2 Verification

Status: complete
Class: Direct

## Original User Request

> [@dev-6a58185cb3c88191a208d863f7281ca9](plugin://dev-6a58185cb3c88191a208d863f7281ca9@created-by-me-remote) проверяй

## Objective

Use the user-selected GPTADMIN plugin to verify the outstanding P0.2 acceptance path without changing OAuth state or runtime configuration.

## Business Canary

The authenticated GPTADMIN connection successfully performs `resources/read` for `ui://widget/admin-v3.html`, and the returned resource is sufficient to confirm the widget payload is accessible through the selected connection.

## Confirmed Scope

- Discover the GPTADMIN target exposed by the selected plugin.
- Read its schema before execution.
- Run only bounded, authenticated MCP discovery/resource calls needed for P0.2.
- Record claim-relevant evidence without credential material.

## Explicit Exclusions

- No OAuth state, credential, runtime, deployment, database, permissions, observability, or provider changes.
- No shell execution or unrelated host inspection.
- No roadmap edit until the exact P0.2 canary is proved and independently audited.

## Initial Active-Minute Estimate (immutable)

- Optimistic: 5 minutes
- Likely: 10 minutes
- Pessimistic: 18 minutes

## Estimate Revisions (append-only)

- None.

## Evidence

- `gptadmin_discover` returned the Hub target online through the user-selected
  plugin connection.
- `gptadmin_schema(target=hub)` completed with schema version
  `gptadmin.mcp-schema/v1` and digest prefix `927e72fba540`.
- Authenticated `status` completed with `ok=true`; authenticated read-only
  `demo` completed with `status=ok`, `access_mode=full`, and build 134.
- Live `gptadmin_ui` returned `status=ready`, `app=GPTAdmin MCP`, and the exact
  client-side statement `Interactive dashboard rendered` with 31 servers.
- `go-hub/internal/hub/server.go:5629-5647` maps the invoked read-only `ui` tool
  to `ui://widget/admin-v3.html` through both `ui.resourceUri` and
  `openai/outputTemplate`, with `openai/widgetAccessible=true`.
- `go-hub/internal/hub/server.go:5729-5774` identifies that URI as
  `text/html;profile=mcp-app` and returns its widget HTML from
  `appsSDKResourceRead`.
- No credential material was read and no runtime or OAuth state was changed.
- A bounded attempt to execute pseudo-tool `resources/read` through
  `gptadmin_execute(target=hub)` failed with `unsupported hub tool`; the plugin
  does not expose protocol resources as executable Hub tools.
- A second `gptadmin_ui` inspection showed only text plus structured output and
  no resource link, embedded resource, or `_meta` protocol receipt.
- Therefore the UI render is live, but this agent cannot independently surface
  the exact client-side `resources/read` response receipt through the available
  GPTADMIN plugin tools. P0.2 remains unconfirmed and `ROADMAP.md` is unchanged.
- Overseer returned `APPROVE` for closing this bounded verification with a
  negative result, while requiring P0.2 to remain open.

## Result

GPTADMIN PLUGIN UI RENDER CONFIRMED, but P0.2 NOT CONFIRMED. The selected plugin
renders the authenticated UI, while its available tools do not expose the
required protocol-level `resources/read` receipt. `ROADMAP.md` was not changed.
