# ZCode managed MCP idle recovery

ZCode can retain a managed HTTP connection after the Hub's five-minute idle
session has expired. The Hub rejects that request before dispatch, but the
installed native client leaves the expired connection in place. Subsequent tools
then fail even while the model session remains alive.

`tools/managed_mcp_stdio.py` is a supported MCP stdio transport extension. It
forwards to the exact existing managed HTTPS URL with its original credential.
It does not replace the Hub, use the legacy unscoped route, change TTL, or patch
ZCode source. It caches initialization and replaces the upstream session only
after the exact managed-endpoint HTTP404 detail:

`connection session expired or unavailable; initialize again`

That refusal precedes child dispatch. After one successful reinitialization and
initialized notification, the transport resubmits that rejected request once,
preserving its ID and arguments. A second refusal is returned to the caller.
Network failures, lost responses,401, ordinary404 and RPC/tool errors do not
trigger resubmission. An admitted action with a lost response remains uncertain.
Redirects are refused, so credentials cannot follow a different endpoint.

Normal stdin closure attempts one authenticated DELETE for its own managed
scope with a five-second deadline. Unconfirmed close is logged rather than
reported as confirmed. Abrupt process death retains the existing server expiry
and bounded cleanup contract.

## Installation and rollback

The installer changes one explicitly named entry in the existing
`~/.zcode/cli/config.json`. Its expected SHA256 fences concurrent configuration
changes. Original bytes are backed up; the original managed binding and token
stay in a private0700 directory with0600 files. The stdio entry contains only
the Python command and protected upstream path. Other MCP definitions, enabled
flags, model/provider and hooks are retained. No source checkout or new daemon
is created. The installer refuses an already populated unknown private state.

```sh
python3 tools/install_zcode_mcp_recovery.py \
  --config "$HOME/.zcode/cli/config.json" \
  --server agent-herder \
  --state-dir "$HOME/.config/gptadmin/zcode-mcp-recovery/agent-herder" \
  --expected-sha256 REVIEWED_CONFIG_SHA256
```

Rollback restores the protected original entry from its exact backup, retaining
any later changes to other entries. Do not overwrite a newer whole configuration
blindly. Existing native inputs and unknown tool results are never replayed.

## Budget and verification

The bridge is one serial process inside its existing native workspace budget;
it spawns no children. At most one upstream request is active. Input/output
frames are limited to8MiB, HTTP waits to90 seconds and own close to5 seconds.
It reads protected configuration and writes no spool or temporary artifacts.
Existing native CPU/RAM/swap/task limits remain in force.

Fast unit checks run on44 in a finite512MiB/CPU1/tasks64/swap0/IO10 service,
private16MiB temporary allowance, expected5s/emergency50s. They use a simulated
HTTP upstream only to prove narrow transport/install logic; this is not live
integration acceptance. Recovery tests were written before implementation:
two behavioral failures on a forwarding-only transport became green after
adding recovery. Separate lifecycle/header tests failed before that change.
Installation and stale-review tests preceded their implementation too.

Focused integration uses an actual native ZCode session on88, one initial
human input, a310-second sleep, then a harmless note action after natural idle
expiry. RED uses the original HTTP definition; GREEN uses the transport.
No manual continuation or tool retry is permitted in either fixture. Native
history and the node-side note receipt are required, not an MCP catalogue or
HTTP200 alone. Current acceptance and retained failed setup attempts:
`.agents/tasks/20261015-zcode-mcp-expiry.md` and
`.planning/debug/zcode-mcp-expiry.md`.

Actual15Oct acceptance: original HTTP native turn failed after333961ms idle;
reviewed transport native turn recovered after327053ms, one initialization
replacement event and one node-side note receipt, one user input and final
MCP_EXPIRY_RECOVERED. Main global agent-herder definition now uses that exact
helper. Eight regressions pass both on44 and through repaired100 bounded
runner (3.652s,44.45MB peak, no swap/OOM). Fixture credential copies and owned
CLI trees were removed after completion; shared services were not restarted.
Already loaded old HTTP clients retain their cached transport until a native
configuration reload/new MCP host; installation does not claim hot replacement
of a foreign running turn. The tested native host needs no continuation when
its managed connection expires.
