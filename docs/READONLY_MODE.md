# Read-only MCP clients

GPTAdmin provides a separate read-only client profile for ChatGPT and other
clients where inspection should work without command confirmations or write
access.

## Product contract

A read-only JWT has `access_mode=readonly` and the scopes `gptadmin.read` and
`gptadmin.inspect`. It cannot access the admin API, invoke arbitrary MCP tools,
run `shell_exec`, manage child MCP servers, create backups or change services.

The global Hub MCP surface for this profile omits the generic
`call_mcp_tool`. It exposes `inspect_system`, marked with MCP
`readOnlyHint=true`, plus the bounded discovery tools. The tool list is stable
for the lifetime of the connection.

Issue this connection in **Admin -> JWT for a client without OAuth -> Read
only**, or use the advanced CLI fallback:

```bash
gptadmin issue-token chatgpt-readonly --readonly
```

Use a full connection for Codex or another client only when it must edit files,
run commands or change infrastructure.

## Why this is not a command filter

There is no safe portable list of "read-only shell commands". A command that
looks harmless can write through redirection, an interpreter, a socket, a
subprocess or an operating-system API. PowerShell constrained mode, a denied
`rm`, or a prompt instruction is not a read-only guarantee.

GPTAdmin therefore does not expose a command interpreter to a read-only
connection. `inspect_system` calls the typed ShellMCP `system_inspect` tool,
which currently supports:

- `read_file`: read a bounded regular file;
- `list_directory`: list bounded file metadata without reading file contents.

Both operations use Go filesystem APIs on Linux, macOS, Windows and Android.
No Bash, PowerShell or CMD process is started.

## Filesystem boundary

ShellMCP limits inspection to `SHELLMCP_INSPECT_ROOTS`. The value is a
platform path list (`:` on Unix, `;` on Windows). A normal installation uses
`SHELLMCP_DEFAULT_CWD` as the default root.

Before reading, ShellMCP resolves symlinks and verifies that the resulting
path still belongs to one configured root. Known credential directories such
as `.ssh`, `.gnupg`, `.aws`, `.kube`, `.docker` and `.password-store` are
denied even when they are under an allowed root. Files must be regular files;
responses and directory lists are bounded.

This boundary prevents writes and accidental broad filesystem traversal. It is
not a claim that pattern matching can recognize every possible confidential
value. Operators should configure roots that contain diagnostics the AI is
intended to inspect, and run ShellMCP as the normal user whenever possible.

## Automatic secret redaction

Before text reaches the MCP response, ShellMCP replaces recognizable:

- Bearer values and JWTs;
- API keys, tokens, secrets and passwords in assignments;
- `Cookie` and `Set-Cookie` values;
- PEM private-key blocks.

Typed markers such as `<redacted:jwt>` and `<redacted:password>` preserve enough
context for diagnosis without exposing the value. Redaction is mandatory for
`system_inspect`; the client cannot disable it.

Future secret handles may let an approved tool use a managed credential
without revealing it to the model. They must not weaken the read-only profile
or turn redaction into reversible model-visible data.

## Verification contract

Black-box tests must prove that the same read-only JWT is denied through:

- relay API tool calls;
- the global Hub MCP endpoint;
- pinned server MCP endpoints;
- generated Action endpoints;
- admin APIs.

Cross-platform CI must compile the same inspector for Windows and execute it on
macOS, while Linux tests verify path roots, symlink escape, credential
directories, output bounds and secret patterns.
