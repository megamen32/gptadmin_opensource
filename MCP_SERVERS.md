# Connect a child MCP server through GPTAdmin ShellMCP

Use this onboarding guide to add a local stdio or remote HTTP/SSE MCP server.
For connecting the AI client itself to GPTAdmin, see [Integrations](docs/INTEGRATIONS.md).

## What ShellMCP does

```text
AI client → GPTAdmin Hub → selected ShellMCP host → child MCP server
```

ShellMCP is both a host agent and an MCP client: it executes local commands,
persists child-server definitions, starts stdio children, and speaks MCP to
local or remote children. The Hub authenticates the AI, applies its access
profile, and routes calls. Installing a child on one host does not install it
on the Hub or on every host. A remote URL, including `localhost`, is reached
from the selected ShellMCP host; no SSH tunnel is implied.

The normal path is **discover → choose host → inspect schema → install if
needed → register → list child tools → execute a harmless tool → verify with
the intended consumer profile**. Registration and process status alone do not
prove that an integration works.

## 1. Discover and choose the target

Connect to your Hub using its supported OAuth/client connection. The managed
public endpoint is `https://mcp.bezrabotnyi.com/mcp`; another installation uses
its own stable HTTPS origin. Generated `u-f<ID>.t.became.bezrabotnyi.com`
addresses are legacy/fallback transport, not the identity for a new connector.

The following examples use Hub tool-call notation, not shell commands. Replace
all `<...>` placeholders with values from your connection and discovery.

```javascript
discover({})
schema({target: "shell:<host>", tool: "mcp_manage", detail: "full", context: "<context>"})
execute({ref: "<call_ref>", args: {action: "list"}})
```

Read the returned profile `instructions` and retain their `context`. Pick an
explicit online `server_id` such as `shell:<host>` from discovery; never guess
`target: "default"`. `file:<host>` is the filesystem surface, not the
child-MCP manager. If the host is absent, offline or awaiting enrollment
approval, resolve that first using [ShellMCP setup](docs/SHELLMCP.md).
A discovery query filters names/IDs; use unfiltered discovery if it finds none.

An exact schema returns a `call_ref` that binds the selected target/tool and
instruction context. Use `execute({ref, args})`, or pass the same `context`
with explicit `target`, `tool`, and `args`. Obtain the exact schema before
using each additional tool. Retain the instruction text as well as its receipt;
a receipt alone is not a substitute after losing the text.

## 2. Choose transport and authentication

Read the server publisher's installation and MCP endpoint instructions first.

| Server | ShellMCP definition | Preparation on the selected host |
|---|---|---|
| Local executable/package | `transport: "stdio"`, `command`, `args`, optional `cwd` and `env` | Install the approved binary/package and required runtime using `shell_exec`; use the host's package manager and preferably a pinned version. |
| Remote Streamable HTTP | `transport: "streamable-http"`, `url`, optional `headers` | Verify the MCP URL, network reachability and upstream credentials from this host. No local package is needed for native HTTP. |
| Legacy SSE | `transport: "sse"`, `url`, optional `headers` | Use the publisher's SSE endpoint, not a web UI URL. |

A REST API or a website is not automatically an MCP endpoint. If the server
requires an OAuth-only login, complete its supported authorization/bridge setup;
adding an Authorization header does not perform login or refresh tokens.
Hub OAuth and upstream authentication are separate: never forward the Hub's
client/service credential as the upstream credential.

Use environment references such as `${UPSTREAM_MCP_TOKEN}` in definitions.
An authorized operator must provision the value through the existing protected
service environment/secret mechanism on the selected host. Expansion uses the
**ShellMCP process environment**, not the AI client's shell. Do not print
secrets, put literal values in `--env` or command arguments, or paste them into
chat. Remove the `headers`/`env` entry when the upstream needs no credential.

## 3. Install if needed, then register

For stdio, installation is a separate operation from registration. Inspect
`shell_exec` first and run the publisher's approved installation command on the
chosen host. Check executable paths and `cwd` there. The native stdio client
launches the executable directly: `command` is one executable and `args` is an
array, not a shell command string. Children inherit the ShellMCP runtime account;
`shell_exec.run_as_user` does not change their account. Arrange a supported
launcher/service identity if the child needs another user.

Example **stdio config** to place in `mcp_manage`'s `config` field (replace the
example executable and arguments with the installed server's actual contract):

```json
{
  "ref": "my-local",
  "transport": "stdio",
  "command": "/absolute/path/to/mcp-server",
  "args": ["--workspace", "/approved/workspace"],
  "cwd": "/approved/workspace",
  "env": {"API_TOKEN": "${UPSTREAM_MCP_TOKEN}"},
  "enabled": true
}
```

For a **remote server**, use the following `execute` arguments after obtaining
`schema` for the selected host's `mcp_manage` tool:

```json
{
  "ref": "<mcp_manage call_ref>",
  "args": {
    "action": "upsert",
    "config": {
      "ref": "my-remote",
      "transport": "streamable-http",
      "url": "https://mcp.example.net/mcp",
      "headers": {"Authorization": "Bearer ${UPSTREAM_MCP_TOKEN}"},
      "enabled": true
    }
  }
}
```

For SSE, change `transport` to `sse` and use the server's SSE URL. For stdio,
substitute the stdio config above. These are templates, not preinstalled services.

Choose a unique child `ref` after inspecting `list`. **`upsert` replaces the
whole definition for that ref; it is not a partial patch.** For an existing
server, inspect `config` and preserve all fields using the authorized protected
source. Returned secret values may be redacted: never save a redaction marker
back as a credential. If the complete configuration cannot be preserved, stop
that update rather than replacing it with an incomplete definition.

`upsert` saves persistent desired state and closes an existing child session.
Stdio starts lazily on discovery/call; `enable` alone does not prove it started.
See the [native child-client contract](go-shellmcp/README.md#local-stdio-mcp).

## 4. Verify schema and execute a harmless call

Read the exact `mcp_tools` and `mcp_call` schemas before their first use.
Reuse `context` from step 1; each explicit execution below carries it.

```javascript
execute({target: "shell:<host>", tool: "mcp_manage", context: "<context>",
         args: {action: "status", ref: "my-remote"}})
schema({target: "shell:<host>", tool: "mcp_tools", detail: "full", context: "<context>"})
execute({ref: "<mcp_tools call_ref>", args: {ref: "my-remote"}})
schema({target: "shell:<host>", tool: "mcp_call", detail: "full", context: "<context>"})
execute({ref: "<mcp_call call_ref>",
         args: {ref: "my-remote", name: "<actual read-only tool>", arguments: {}}})
```

`mcp_tools` performs the child's real `tools/list`. Select an actual harmless
read-only operation (for example a metadata lookup), fill `arguments` from its
returned `inputSchema`, and verify the expected data in the final child result.
`{}` is appropriate only when that tool requires no inputs. There is no universal
`ping` tool; do not invent one. Avoid sends, purchases or other writes as probes.
If the server has no harmless operation, report that verification gap.

If a call returns a running `job_id`, poll `job({id: "<job_id>"})` until it
finishes. Use `expand({id: "<operation_id>"})` for saved details/continuations.
Check nested `isError` and tool output as well as Hub status: HTTP 200 or
`completed` can still contain an upstream error. Preserve job/operation IDs;
a timeout or missing response is not permission to replay an accepted call.

For an authorized HTTP/Actions client, the equivalent API routes are
`POST /mcp-relay/tools` (schema), `POST /mcp-relay/call` (execute), and
`GET /mcp-relay/job/<job_id>` (poll). The POST bodies are the same argument
objects used above; send the managed client credential as
`Authorization: Bearer <client-credential>` and retained context as
`X-GPTAdmin-Context: <context>` when not using a context-bearing `call_ref`.
Use the authenticated parent API; direct ShellMCP service credentials are not
needed. See [relay API source](go-hub/internal/hub/server.go).

## 5. Profiles and the consumer connection

The authenticated connection selects the effective profile. It can restrict
targets, tools, read-only access, write budgets and required approvals. Use an
authorized provisioning connection for install/upsert; follow the Hub approval
flow if requested. Missing targets/tools and permission denials are boundaries,
not invitations to switch to admin credentials or a direct upstream URL.

After registration, rediscover. When projected by the host, the child appears
as `mcp:shell:<host>:<ref>`; use the **returned** ID. Bind the intended consumer's
profile to that child and the required tools using the authorized admin flow,
then verify discovery → schema → harmless execution through that consumer's
actual connection. An admin-only success does not prove consumer access.
A scoped consumer need not receive `shell_exec` or `mcp_manage` permission.

A definition's optional `tool_allowlist` limits the exposed child tools. Personal
credentials may also be fenced with `owner_profile_id`; such a child requires
its exact owner profile and a managed child connection. Use its discovered child
target and let the Hub manage connection metadata; do not invent internal
`_connection_*` fields or change ownership to bypass a denial.

## Troubleshooting

| Symptom | Check / next action |
|---|---|
| Parent connects, child absent | Distinguish host enrollment/status, registered child, Hub projection and profile visibility. Re-run discovery after registration; inspect the selected host's `list`/`status` if permitted. |
| `instructions_required`, `executed: false` | This attempt did not start. Read the returned instructions, retain the new context, then retry with it or a fresh exact schema. |
| `schema_reference_unavailable` | Obtain a fresh exact schema/call_ref after reconnect, expiry or Hub restart. For an unknown execution outcome, inspect its job/result first. |
| Write awaiting approval / budget exhausted | Complete the returned approval flow or respect the profile's budget. Do not retry through a broader connection. |
| Child 401/403 | Check upstream auth separately from Hub OAuth: correct endpoint/header, credential scope/expiry and variable availability in ShellMCP's protected environment. Do not dump the environment. |
| Executable not found / child exits | Check installation, absolute path, service PATH, `cwd`, runtime account and bounded/redacted child stderr. A successful interactive-shell install is not proof that the service can launch it. |
| HTTP/SSE handshake fails | Check the selected host's DNS/TLS/network, exact MCP endpoint and transport; an ordinary HTTP health response is not an MCP handshake. |
| No tools / tool rejected | Check `enabled`, actual `tools/list`, `tool_allowlist`, profile tool restrictions and personal-owner binding. Re-read the schema after changes. |
| Need a restart | `mcp_manage` action `restart` is stdio-only. A lifecycle change can close active sessions; inspect ownership and outstanding jobs before changing a shared child. |

## Local CLI alternative (operator on the target host)

For the existing node-100 checkout, [mcp-add](mcp-add) is a local installer
helper. It does not choose a remote ShellMCP target. Prefer the Hub tools above
for AI-driven onboarding; use this alternative only for authorized local setup.

```bash
./mcp-add my-server -- npx -y '<approved-package>@<version>'
./mcp-add my-remote --url https://mcp.example.net/mcp
```

The CLI's `--url` shortcut launches `mcp-remote` as a stdio bridge; it is distinct
from native `transport: "streamable-http"`. The helper writes the system
registry (`/etc/gptadmin/mcp.json` by default), generates the aggregate Go ShellMCP supervisor config (`/etc/gptadmin/mcp-supervisor.json`), restarts it and prints config status. It does not create one systemd unit per child. Use `--force` only for an intentional replacement. A service restart can affect other children.

CLI registry projection and native `mcp_manage` are separate configuration
routes. Check the active `SHELLMCP_MCP_CONFIG` and its owner before mixing them;
CLI regeneration must not overwrite definitions managed through the native API.
Do not treat CLI `mcp status` output as a protocol or business-operation test;
complete step 4 through GPTAdmin afterward.

## Source references

- [ShellMCP tools, lifecycle and redacted config](go-shellmcp/internal/server/mcp.go)
- [Definition fields, replacement and validation](go-shellmcp/internal/supervisor/supervisor.go)
- [Stdio client and environment expansion](go-shellmcp/internal/mcpclient/client.go)
- [Remote HTTP/SSE client and headers](go-shellmcp/internal/mcpclient/remote.go)
- [Hub relay tools and HTTP API](go-hub/internal/hub/server.go)
- [Instruction context contract](go-hub/internal/hub/instruction_context.go)
- [Personal-child ownership checks](go-hub/internal/hub/connection_owner.go)
- [CLI implementation](cli.py) · [per-server MCP/Actions exposure](docs/MCP_PROXY_RELAY.md)
