# GrepMesh final Inspector user-surface gate

Role: Tester

## Scope

`only-new` only. Use the independent official MCP Inspector CLI as the sole
MCP client. Do not inspect source/docs/tests or use curl, reqwest, Python/Node
HTTP, or direct JSON-RPC.

## User outcome

From one local GrepMesh endpoint, an agent omits `hosts` and finds both
temporary canaries, discovers paths, reads the remote canary, and receives
local results plus `partial=true` after the peer stops.

## Setup and real surface

First run:
`npx --yes @modelcontextprotocol/inspector --cli --help`.
Use its CLI with `--server-url <A_URL> --transport http`. Start only temporary
GrepMesh processes with random temporary roots, configs, and ephemeral ports.
The allowed temporary config shape is:

```json
{
  "host_id": "A",
  "bind": "127.0.0.1:PORT_A",
  "root": "/tmp/.../root-a",
  "peers": [{
    "host_id": "B",
    "local_url": "http://127.0.0.1:PORT_B/mcp",
    "routable_url": "http://127.0.0.1:PORT_B/mcp"
  }],
  "limits": {"max_results": 20, "context_lines": 1}
}
```

B uses the same shape with no peers and its own root/port. Do not use
production paths, secrets, permissions, or public listeners.

## Journey and raw evidence

1. Start B and A. Inspector `tools/list` must complete.
2. Call `search_text` with `--tool-args-json` containing the canary query and
   limit but NO `hosts` property. Capture raw Inspector output and verify both
   host IDs/canaries.
3. Call `find_paths`, `read_text` for B's absolute canary path, and
   `search_status`; capture raw outputs.
4. Stop B. Call `search_text` again with `hosts` omitted and capture raw output;
   verify A remains and `partial=true`.
5. Stop processes and remove only temporary data.

If Inspector cannot connect, record the raw error and return
`CHANGES_REQUIRED`; if Inspector itself cannot run, return
`STOP_MISSING_REAL_SURFACE`. Append exact user-visible evidence and one
verdict: `PASS`, `CHANGES_REQUIRED`, or `STOP_MISSING_REAL_SURFACE`.

## Tester checkpoint

- Verdict: `CHANGES_REQUIRED`
- Inspector CLI help completed successfully.
- Attempted Inspector `tools/list` against the temporary A endpoint at `http://127.0.0.1:36033/mcp`.
- Raw user-visible failure:

  `{"error":{"code":"unreachable","message":"fetch failed","cause":"connect ECONNREFUSED 127.0.0.1:36033"}}`

- Temporary GrepMesh server processes were stopped, and the temp workspace under `/tmp/grepmesh-test.jsPirG` was removed with nonrecursive cleanup.
