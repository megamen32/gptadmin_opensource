# GrepMesh final only-new user-surface test rerun

Role: Tester

## Scope mode

`only-new` only. Exercise the changed user journey through the independent
official MCP Inspector CLI, not source, docs, unit tests, or hand-written HTTP.

## Intended outcome

An agent using one local GrepMesh endpoint can omit `hosts` and still search
both temporary nodes, discover paths, read a remote file, and receive local
results plus `partial=true` after the peer stops.

## Fresh real surface

Use the independent Inspector CLI as the MCP consumer:

`npx --yes @modelcontextprotocol/inspector --cli --server-url <URL> --transport http ...`

Start by running its `--help` in the fresh CLI session. The Inspector is the
only allowed MCP client surface for this rerun. Do not use curl, reqwest,
Python/Node HTTP code, direct JSON-RPC, repository source, README/config
examples, or existing test code.

## Allowed temporary setup

Use only new temporary roots, random canary strings, ephemeral loopback ports,
temporary JSON configs, and temporary GrepMesh processes. The normal server
entrypoint is discovered with `cd /home/admin/gptadmin/grepmesh && cargo
run -- --config <temporary-config>`. A config may contain the already-defined
public fields `host_id`, `bind`, `root`, `peers`, `limits`, `index_path`, and
`exclude_globs`; use no production paths or secrets.

## Required journey

1. Start B on a temporary root with its own canary and start A with B's
   routable temporary URL in `peers`; use A's MCP URL as the Inspector target.
2. Use Inspector `tools/list`, then call `search_text` with JSON arguments
   containing the query and limit but deliberately NO `hosts` property. Save
   the raw Inspector output and verify both host IDs/canaries are visible.
3. Use Inspector to call `find_paths`, `read_text` for B's absolute canary
   path, and `search_status`; save raw outputs.
4. Stop B. Repeat `search_text` with `hosts` still omitted, save raw output,
   and verify A's canary remains while `partial=true` is visible.
5. Stop temporary processes and remove only the temporary canaries/configs.

If Inspector cannot connect or the result is ambiguous, record the raw
user-visible error and return `CHANGES_REQUIRED`; do not substitute another
surface. If the Inspector CLI itself cannot be run, return
`STOP_MISSING_REAL_SURFACE`.

Append exact commands/output excerpts, cleanup evidence, and one verdict:
`PASS`, `CHANGES_REQUIRED`, or `STOP_MISSING_REAL_SURFACE`.

## Tester evidence

Surface used: independent official MCP Inspector CLI.

Exact command sequence:

1. `npx --yes @modelcontextprotocol/inspector --cli --help`
2. `cd /home/admin/gptadmin/grepmesh && cargo run -- --help`
3. Temporary setup:
   - created `/tmp/tmp.835OylrG6X/root-a` and `/tmp/tmp.835OylrG6X/root-b`
   - wrote temporary GrepMesh configs under `/tmp/tmp.835OylrG6X/`
   - started B, then A with B in `peers`
4. Inspector target attempt:
   - `npx --yes @modelcontextprotocol/inspector --cli --server-url http://127.0.0.1:34675/mcp --transport http --method tools/list`

Raw setup findings:

- Initial peer config attempt failed with:

  `Error: parse config JSON`
  `Caused by: invalid type: string "http://127.0.0.1:30461", expected struct PeerConfig at line 5 column 36`

- Next peer config attempt failed with:

  `Error: parse config JSON`
  `Caused by: missing field \`local_url\` at line 5 column 61`

- Next peer config attempt failed with:

  `Error: parse config JSON`
  `Caused by: missing field \`routable_url\` at line 5 column 67`

Raw Inspector-visible failure:

```json
{"error":{"code":"error","message":"Server's protocol version is not supported: 2026-07-28"}}
```

Observed result:

- B and A both started on temporary loopback ports.
- Inspector connected far enough to receive the protocol-version rejection, but could not complete `tools/list`.
- Required journey was therefore not verifiable through the requested Inspector surface.

Cleanup evidence:

- Sent Ctrl-C to the temporary A and B server sessions.
- Removed only `/tmp/tmp.835OylrG6X` with targeted `find ... -delete` cleanup.
- Verified cleanup with `cleaned`.

Verdict: `CHANGES_REQUIRED`

Smallest in-scope repair:

- Provide an Inspector-compatible MCP protocol version for GrepMesh, or a compatible Inspector build/version, so the official CLI can complete `tools/list` and the required searches.
