# Execute: restart/reconnect investigation, 2026-10-18

Scope: `codex/execute-reliability-20261018`, based on `5393752`. The initial sections describe the source-review checkpoint. The orchestrator
subsequently authorized integration and rollout; the result and current release
blocker are recorded in the addendum below. Production currently runs the
verified previous binary after rollback.

## Confirmed defect and fix

An Execute call using a schema reference lost on restart, expiry (30 minutes),
or operation-cache eviction returned JSON-RPC `-32602`. The tool descriptor
was still exported. This is a recoverable handle-lifecycle failure, not an
invalid MCP call. The live existing connector reproduced this error with the
deliberately absent read-only probe reference `op_execute_reliability_missing`.
No target execution was dispatched.

The orchestrator independently reported a live failure using a previously valid
reference after approximately 15 minutes, followed immediately by successful
fresh discover → schema → Execute (`REFRESHED_EXECUTE_OK`, exit 0). Age alone
does not distinguish expiry from eviction: the shared operation cache is also
bounded to 128 items/32 MiB, so a handle can disappear before its 30-minute TTL.
The patch handles all these unavailable-reference cases without guessing or
automatically reconstructing the old call.

Unavailable references now return a tool result with `isError:true`,
`executed:false`, `error.code:schema_reference_unavailable` and model-visible
guidance to obtain an exact fresh schema before making a new call. References
remain ephemeral and caller-bound; they grant no new permissions. Conflicting
explicit target/tool values remain protocol errors. No automatic replay,
credential changes, profile changes or bypass of `instructions_required` was
introduced. Execute aliases and the internal facade receive the same recovery
contract.

This follows the MCP specification's guidance for expired handles and
[tool execution errors](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/main/docs/specification/2026-07-28/server/tools.mdx#stateful-tools).

## What is and is not explained

- Default `tools/list` exports `ui`, `discover`, `schema`, `execute`, `job`,
  `expand`. Inventory and child MCP status do not determine this list.
- Read-only scopes and restrictive access profiles intentionally hide/deny
  Execute. They are regression-tested; their permissions were not relaxed.
- The existing legacy OAuth-origin fix `d2bae55` is retained. OAuth metadata,
  access-profile, instruction-context and protocol tests passed in the full
  integrated Go suite.
- Compact discovery removes metadata; exact `schema(target,tool)` selects one
  definition even with `detail:full`. These behaviours are tested with 80
  shell parents and 160 child descriptors, including repeated child names,
  reinitialize and restored registry state.
- Compact discovery is not a fixed byte limit: its size still grows with the
  matching inventory. No ChatGPT client failure caused by a large result was
  reproduced. Use the existing query/status filters and exact-tool schema
  while investigating; do not present the size hypothesis as a proven cause.
- No disappearing-Execute UI episode was reproduced in ChatGPT. The reference
  bug explains failed recovery calls, **not proven removal from the UI**.

OpenAI documents a separate metadata refresh and new-conversation check for
custom MCP connections in [Connect and test](https://developers.openai.com/plugins/deploy/connect-chatgpt#refresh-metadata).
Its [troubleshooting guide](https://developers.openai.com/plugins/deploy/troubleshooting)
calls for server logs, client transcript/console and screenshots when isolating
client failures. This task did not access or change the user's ChatGPT session.

## Verification

1. RED: `go test ./internal/hub -run '^TestExecuteReferenceLifecycleRecovery$'
   -count=1` failed on the original source for expired, evicted and restarted
   handles, each with `-32602` instead of a recoverable tool result.
2. GREEN: the same regression passed after the source fix; export/inventory
   stability and unchanged instruction/permission gates also passed.
3. Focused integrated suite: `go test ./internal/hub -run
   'Test(Execute|InstructionContext|Token|ChildSchema|ManagedClientAccessProfile|OAuthResourceMetadata|MCPLegacyProtocol|ConfiguredBearer)'
   -count=1` passed.
4. Full `go-hub`: `go test ./...` passed (Hub package 145.319 seconds).
   The orchestrator's whole-package run with `-timeout=2m` exhausted that
   overall limit. An additional isolated run of
   `TestMCPRelayCallIdempotencyReturnsCompletedResultAfterRetry` with
   `-timeout=5m -count=1` passed in 2.033 seconds. The full suite's successful
   run used Go's default 10-minute timeout, exceeding the earlier 2-minute cap.
5. Real candidate: build `./cmd/gptadmin-node`, then run
   `python3 scripts/verify_execute_reliability.py /path/to/gptadmin-node`.
   This starts a real candidate Node with a real local shell executor,
   temporary identity/configuration, loopback ports and fresh private test
   credentials. It verifies the instruction gate, compact discovery, exact
   full shell schema, real command execution, new TCP connections, graceful
   process restart, unchanged tool definitions, rejection of the old handle,
   fresh schema and real command execution after restart. The output file
   contains exactly two expected lines, proving no stale-handle dispatch or
   replay. Candidate SHA-256 is in `.tmp/execute-reliability/candidate.json`.
6. Live existing production connector: shell `printf` returned
   `execute-reliability-live-shell-ok`, exit 0. AgentBrowser performed
   open → snapshot → fill → click → get_text on an isolated local data page;
   visible output was `execute-reconnect-ok`. Own session `ex1018`, namespace
   `exr`, was closed. This proves the existing live path, **not deployment of
   the candidate** and not a production restart acceptance.

Task-local logs and receipts: `.tmp/execute-reliability/{focused.log,
regression.log,go-all.log,candidate.json,live-metrics.json,
live-journal-summary.json}`. The source regression and smoke script are
versioned; temporary machine-specific logs are not.

The live `/metrics` snapshot reported build 1024, aggregate telemetry failure
count 3 and task failed count 0. These counters have no episode/client
correlation and cannot establish a ChatGPT cause. Bounded service/PID journal
queries contained no current request records, so no claim of error-free hub
or gateway logs is made.

## Remaining acceptance, owned by the orchestrator

Review/integrate this branch, agree to production deployment, deploy that exact
reviewed source, then exercise the ChatGPT connection after reconnect and a
controlled restart. Check exported Execute metadata and the real harmless
shell/AgentBrowser journey; verify stale-reference recovery without replaying
accepted/unknown work. If Execute disappears again, preserve the ChatGPT
transcript and connection metadata alongside the exact time/request trace.
Until that evidence exists, the source lifecycle fix is verified and full
resolution of the intermittent ChatGPT UI symptom remains unproven.

Safe rollout proposal (no step below has been executed by this task):

1. Review the scoped diff and integrate only these owned changes into the
   agreed release history. Build the candidate from that exact integrated
   commit; retain its source identity and executable digest.
2. Identify the actually running executable from the service MainPID and
   `/proc/<pid>/exe` rather than assuming a historical unit/install path.
   Preserve the old binary and configuration. Use the existing native drain
   and task receipt checks; keep accepted/UNKNOWN work and OAuth/profile state.
3. After the orchestrator's rollout agreement, replace only the intended
   executable and perform one controlled service restart. Check the running
   executable digest and supported readiness/authentication paths. Do not
   rotate credentials, recreate the ChatGPT connection or activate stopped
   user sessions as part of this fix.
4. On the real ChatGPT connection, verify Execute is still advertised and
   callable. Test a controlled old harmless schema handle, its explicit
   non-execution error, fresh schema → harmless shell command, and an isolated
   AgentBrowser interaction. Record transcript, result, time and request IDs.
5. If the real consumer fails, retain the evidence and restore the previous
   executable through the existing rollback procedure. Never restore task
   state backwards or replay accepted/unknown commands to manufacture success.

## Rollout addendum: rolled back, delivery incomplete

After explicit orchestrator review/approval, canonical main was verified clean
and unchanged from the task base, then fast-forwarded and pushed to the source
commit below. The normal `tools/build.sh node --arch amd64` package recorded
`source_dirty:false`. That exact packaged binary also passed the isolated
real Node/restart/shell canary before installation.

- Reviewed source: `4a0e3aa36a3d9f301bedc2654778f6b73442248a`.
- Packaged/running candidate SHA-256: `02d334d785a3349ec5264bfb81bbca51718247628696701c7917efb21922c24e`.
- Candidate PID: 1584479; build 1024.
- Verified backup: `/opt/gptadmin/backups/execute-reliability-20261018-211848-4a0e3aa3/previous-gptadmin-node`.
- Current rollback PID: 1584928; source `5393752`, build 1024.
- Current/backup binary SHA-256: `a00fc97b36d4df8000edc3892a87668c676eb1444f459618f363ccd678f6de8a`.

The first preflight refused an active request before mutation. The second
confirmed native `ready_to_stop:true` with all four counters zero, backed up
the binary and used an atomic same-directory replacement plus one normal
controlled restart. The candidate passed local tools/list stability, stale
reference non-execution, `instructions_required`, and real shell Execute.

The immediate public OAuth metadata probe then returned HTTP 502. The fail-closed
rollout performed its authorized rollback, including a restart onto the old
binary. Its first immediate hash check raced startup; a settled process hash
and `/version` confirmed the rollback and the receipt was corrected without
replaying the operation. Nginx logged `no live upstreams` at 21:18:59; the
upstream uses `max_fails=1 fail_timeout=1s`. This is evidence of a public ingress
startup window, not proof of a candidate-code defect. All three public origins
subsequently returned correct 200 resource/issuer metadata on the restored
version. A future rollout needs a bounded read-only public-readiness wait.

**Restart safety blocker:** the systemd journal also showed termination of
browser/Chrome descendants within the old GPTAdmin service cgroup, despite
zero native drain counters. The preflight examined only immediate children
and failed to account for these descendants. Their ownership and possible
user-state loss are unknown; preservation of foreign browser sessions cannot
be claimed. No further rollout/restart was attempted. Before another restart,
inspect the whole service cgroup and resolve persistent browser ownership and
shutdown handling. Do not reactivate stopped user sessions, move foreign
processes or change nginx/systemd policy as an unreviewed workaround.

The restored real connector passed discover, schema and shell Execute
(`ROLLBACK_CONSUMER_OK`, exit 0). AgentBrowser on server100 passed a fresh own
open → snapshot → fill → snapshot → click → snapshot → get_text → close journey,
returning `ROLLBACK_BROWSER_OK`; own session `exrb`, namespace `er`, was closed.
No Codex, ZCode, OpenCode or AgentHerder service restart was requested.

The machine-derived deployment/rollback receipt, binary hashes, old/new PIDs,
canary outcomes and blocker are in
`.tmp/execute-reliability/production-receipt.json`. This is **not a completed
production release**: the source fix is on main, production is rolled back, and
ChatGPT button visibility remains unverified.
