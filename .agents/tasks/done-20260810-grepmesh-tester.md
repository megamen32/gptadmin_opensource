# GrepMesh final only-new user-surface test

Role: Tester

## Scope mode

`only-new` only. Test the changed GrepMesh user journey through a real MCP/CLI
surface, not source, docs, unit tests, or synthetic HTTP checks.

## Intended user outcome

An agent connects to one local GrepMesh MCP endpoint and can search both
temporary canary roots by default, discover the host/path, read the remote
canary, and continue with a useful partial result after the second node stops.

## Target surface and allowed data

Target is the actual `/home/admin/gptadmin/grepmesh` command entrypoint in
a fresh CLI session. Use only newly-created temporary directories, ephemeral
loopback ports, temporary configs, and random canary strings. Start only
temporary GrepMesh processes and clean up those processes/files afterward.
Begin by discovering the normal invocation from `grepmesh --help` or the
installed command. Do not read repository source, README/config examples,
task files, implementation docs, or prior test code before the first attempt.

## Required journey

Using an actual independent MCP CLI client/harness surface (for example an
installed MCP inspector or supported AI-harness MCP client), not a hand-written
HTTP/curl request:

1. Start two temporary nodes with distinct canaries and a static peer entry.
2. Connect to node A through the normal MCP client surface and use the four
   exposed tools as a new user would. The first `search_text` request MUST
   omit the `hosts` field entirely (not merely pass `"*"`); record the raw
   user-visible response showing local and peer canaries. Then use explicit
   path discovery, remote read, and status.
3. Stop node B and repeat the search, observing local results plus an explicit
   partial outcome.
4. Capture raw client-visible tool output for the omitted-host search, remote
   read, and stopped-peer partial result. Record only user-visible results,
   errors, recovery, and cleanup evidence.

## Stop conditions

If no actual MCP client/harness surface is available, return
`STOP_MISSING_REAL_SURFACE` and do not substitute curl, direct HTTP, source,
unit tests, or synthetic requests. Do not deploy, restart production, use
secrets, or change permissions.

Append the complete journey and one verdict: `PASS`, `CHANGES_REQUIRED`, or
`STOP_MISSING_REAL_SURFACE`.

## Evidence checkpoint

- Attempted black-box discovery of the CLI entrypoint with `command -v grepmesh && grepmesh --help`; `grepmesh` was not in PATH.
- Confirmed the local repo contains `/home/admin/gptadmin/grepmesh/`, but did not inspect source or docs.
- Checked for an independent MCP CLI/client surface with:
  - `command -v mcp-inspector`
  - `command -v inspector`
  - `command -v npx`
  - `command -v node`
- Result: only `npx` and `node` were available; no dedicated MCP inspector/CLI surface was present in the environment.
- Because the required independent MCP client/harness surface is absent, I did not proceed to curl, direct HTTP, source inspection, unit tests, or synthetic MCP requests.

Verdict: `STOP_MISSING_REAL_SURFACE`
