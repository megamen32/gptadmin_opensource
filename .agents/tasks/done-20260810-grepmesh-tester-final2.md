# GrepMesh final Inspector user-surface gate rerun

Role: Tester

## Scope

`only-new` only. Use the independent official MCP Inspector CLI as the sole
MCP client. Do not inspect source/docs/tests or use curl, reqwest, Python/Node
HTTP, or direct JSON-RPC.

## User outcome

From one local GrepMesh endpoint, an agent omits `hosts` and finds both
temporary canaries, discovers paths, reads the remote canary, and receives
local results plus `partial=true` after the peer stops.

## Setup and readiness

First run `npx --yes @modelcontextprotocol/inspector --cli --help`. Use its CLI
with `--server-url <A_URL> --transport http`. Start only temporary GrepMesh
processes with random roots/configs and ephemeral ports. Use the same allowed
config shape as the prior Tester task: `host_id`, `bind`, `root`, a `peers`
array of `{host_id,local_url,routable_url}`, and optional `limits`.

Before starting A/B, build the already selected command once with:
`cd /home/admin/gptadmin/grepmesh && cargo build --quiet`.
Start B then A using the normal `cargo run -- --config <temporary-config>`
entrypoint or the just-built binary. Wait at least 8 seconds after A starts
for the listener and index initialization. If the first Inspector connection
gets `ECONNREFUSED`, wait 5 more seconds and retry the same Inspector command
once. This is setup readiness only; do not replace Inspector with curl or a
synthetic request.

## Journey and raw evidence

1. Inspector `tools/list` must complete.
2. Call `search_text` with JSON containing the canary query and limit but NO
   `hosts` property. Capture raw Inspector output and verify both host IDs and
   canaries.
3. Call `find_paths`, `read_text` for B's absolute canary path, and
   `search_status`; capture raw outputs.
4. Stop B. Call `search_text` again with `hosts` omitted, capture raw output,
   and verify A remains with `partial=true`.
5. Stop processes and remove only temporary data.

If Inspector cannot connect after the bounded retry, record the raw error and
return `CHANGES_REQUIRED`; if Inspector itself cannot run, return
`STOP_MISSING_REAL_SURFACE`. Append exact user-visible evidence and one
verdict: `PASS`, `CHANGES_REQUIRED`, or `STOP_MISSING_REAL_SURFACE`.

## Tester evidence

Verdict: `PASS`

Surface/tool:

- Inspector CLI only, against local GrepMesh HTTP endpoint `http://127.0.0.1:16921`

Exact journey:

1. Ran `npx --yes @modelcontextprotocol/inspector --cli --help`
2. Built GrepMesh with `cargo build --quiet`
3. Started B from `/tmp/grepmesh-test-p4v3i66z/b.json`, then A from `/tmp/grepmesh-test-p4v3i66z/a.json`
4. Waited 8 seconds after A started
5. Ran `tools/list`
6. Ran `search_text` with `{"query":"grepmesh-tester-canary-20260810","max_matches":20}` and no `hosts`
7. Ran `find_paths` with `{"pattern":"canary"}`
8. Ran `read_text` for host `B` and path `/tmp/grepmesh-test-p4v3i66z/root-b/remote-canary.txt`
9. Ran `search_status`
10. Stopped B
11. Ran `search_text` again with the same query and no `hosts`
12. Stopped A and cleaned the temp root

Observed raw results:

- `tools/list` exposed `search_text`, `find_paths`, `read_text`, and `search_status`
- Initial `search_text` returned both host IDs and both canaries:
  - host `A`, path `/tmp/grepmesh-test-p4v3i66z/root-a/local-canary.txt`
  - host `B`, path `/tmp/grepmesh-test-p4v3i66z/root-b/remote-canary.txt`
  - `partial=false`
- `find_paths` returned both paths for hosts `A` and `B`
- `read_text` for host `B` returned `grepmesh-tester-canary-20260810 host=B role=remote`
- `search_status` reported both hosts `ok=true`
- After stopping B, `search_text` returned only host `A` and set `partial=true`
  - one `host_status` entry failed for the peer URL `http://127.0.0.1:43897/`

Raw Inspector snippets:

```json
{"hop_count":0,"host_id":"A","host_status":[{"error":null,"host_id":"A","ok":true},{"error":null,"host_id":"B","ok":true}],"matches":[{"column":1,"context":[{"line_number":1,"text":"grepmesh-tester-canary-20260810 host=A role=local"}],"host_id":"A","line_number":1,"path":"/tmp/grepmesh-test-p4v3i66z/root-a/local-canary.txt","text":"grepmesh-tester-canary-20260810 host=A role=local"},{"column":1,"context":[{"line_number":1,"text":"grepmesh-tester-canary-20260810 host=B role=remote"}],"host_id":"B","line_number":1,"path":"/tmp/grepmesh-test-p4v3i66z/root-b/remote-canary.txt","text":"grepmesh-tester-canary-20260810 host=B role=remote"}],"origin_host":"A","partial":false}
```

```json
{"paths":[{"host":"A","kind":"file","path":"/tmp/grepmesh-test-p4v3i66z/root-a/local-canary.txt"},{"host":"B","kind":"file","path":"/tmp/grepmesh-test-p4v3i66z/root-b/remote-canary.txt"}],"partial":false}
```

```json
{"chunks":[{"end_line":1,"lines":[{"line_number":1,"text":"grepmesh-tester-canary-20260810 host=B role=remote"}],"start_line":1}],"host_id":"A","origin_host":"A","partial":false,"path":"/tmp/grepmesh-test-p4v3i66z/root-b/remote-canary.txt","target_host_id":"B","truncated":false}
```

```json
{"host_id":"A","host_status":[{"error":null,"host_id":"A","ok":true},{"error":"error sending request for url (http://127.0.0.1:43897/)","host_id":"A","ok":false}],"partial":true,"results":[{"host_id":"A","path":"/tmp/grepmesh-test-p4v3i66z/root-a/local-canary.txt","text":"grepmesh-tester-canary-20260810 host=A role=local"}]}
```

No in-scope defects found.

## Checkpoint 2026-08-10

- Inspector CLI established successfully.
- No bounded retry was needed for Inspector connectivity.
- Temporary A/B state was already cleaned up after the successful run.
- Current outcome remains `PASS`.
