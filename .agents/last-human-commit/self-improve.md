## 2026-08-04 — docs-custom-gpt-virtual-mcp-public-docs (Short)

- What slowed or confused L? `tests/test_product_auth_language.py` does not exist here, so I had to re-scope to real focused checks.
- Which instruction should change? none
- Which skill, MCP, or tool is missing? none
- What operation or error repeated? 1 failed combined pytest attempt because the missing test file short-circuited `&&`; a small existence check or direct known-test list would avoid it.
- State: fixed now

## 2026-08-05 — Root docs translation recovery (Short)

- What slowed or confused L? `englishFiles()` read a generated website mirror, so existing mirror-equality checks did not reveal that a new root document could not be translated first.
- Which instruction should change? none.
- Which skill, MCP, or tool is missing? Proposed: a docs-contract fixture helper that creates a stale mirror and root manifest without retained temporary diagnostics.
- What operation or error repeated? Two review passes preceded discovery that the first canary leaked its retained `/tmp/gptadmin-docs-*`; guard: require test-owned cleanup for diagnostic-mode fixtures.
- State: fixed now

## 2026-08-05 — Local main consolidation (Full)

- What slowed or confused L? Divergent stale worktrees and a nested gitlink hid two independent merge contracts: the website mirror and optional virtual MCP tests.
- Which instruction should change? Proposed: when a user requires a canonical local checkout, require explicit no-new-worktree mode before any task bootstrap.
- Which skill, MCP, or tool is missing? Proposed: a read-only worktree inventory that classifies clean/dirty state, unique commits, and gitlink/tree collisions.
- What operation or error repeated? Merge choices retained stale test and source variants; guard: after every cross-line merge, run the full target package after focused tests and restore the newer contract when it has explicit coverage.
- State: fixed now

## 2026-08-06 — GPTAdmin plugin smoke test (Direct)

- What slowed or confused L? `ALL_TOOLS` descriptions were too large to inspect safely; filtering to exact tool names resolved it.
- Which instruction should change? none
- Which skill, MCP, or tool is missing? none; GPTAdmin connector exposed discovery, schema, and execute directly.
- What operation or error repeated? none; one discovery, one schema call, and one canary execute all completed.
- State: not actionable

## 2026-08-06 — Server-100 GPTADMIN blocker audit (Direct)

- What slowed or confused L? `system_inspect` returned different errors for `/home/roomhacker` versus `/home`; the target's inspection-root contract is not self-describing.
- Which instruction should change? none
- Which skill, MCP, or tool is missing? Proposed: expose configured inspection roots and executor mode in GPTADMIN schema/status without revealing secrets.
- What operation or error repeated? `shell_exec` failed twice with missing `/usr/bin/sudo`; guard: preflight executor binary and report remediation separately from command failure.
- State: Proposed

## 2026-08-06 — Server-100 ShellMCP repair (Short)

- What slowed or confused L? GPTADMIN shell_exec failed inside the service namespace while SSH could see sudo; the decisive evidence was the live systemd unit and process identity.
- Which instruction should change? none
- Which skill, MCP, or tool is missing? Proposed: a bounded GPTADMIN live deployment helper for atomic drop-in install, rollback receipt, restart, and canary.
- What operation or error repeated? Phone proxy CONNECT failed on four tested ports/paths; guard: separate proxy transport acceptance from external HTTPS success and fall back to vpn2 only when explicitly authorized.
- State: Proposed

## 2026-08-06 — Custom Actions and selected MCP HTTP Remote audit (Full)

- What slowed or confused L? A successful VPN2 egress check initially looked like an Actions check; the public schema must be called explicitly and redirect-followed.
- Which instruction should change? none
- Which skill, MCP, or tool is missing? Proposed: a public-surface canary that validates OpenAPI, MCP, selected-server URL, and redacted Bearer config together.
- What operation or error repeated? Public routes returned 404 across 6 endpoints after redirect; guard: fail the release canary on canonical-host route absence before UI claims.
- State: Proposed

## 2026-08-07 — GPTAdmin status and FRP URL audit (Direct)

- What slowed or confused L? The local CLI defaulted to user scope and the server CLI status summary printed unknown despite active raw units; `doctor --json` was needed for authoritative state.
- Which instruction should change? none
- Which skill, MCP, or tool is missing? Proposed: a status command that emits the effective public URL and normalized systemd states in one machine-readable result.
- What operation or error repeated? Wrong hostname returned redirects/404s before the FRP URL was read from `gptadmin urls`; guard: always derive the URL from the target runtime before external canaries.
- State: Proposed

## 2026-08-07 — BrowserOS Mac mini URL check (Direct)

- What slowed or confused L? The GPTADMIN agent name implied a connected MCP, but schema failed and the stored relay port 19000 had no listener; direct Mac inspection found BrowserOS on 9000 returning 503.
- Which instruction should change? none
- Which skill, MCP, or tool is missing? Proposed: target discovery should expose the effective remote MCP URL and a health result, not only an agent-config wrapper.
- What operation or error repeated? `mcp_tools`/schema failed once with stdio exit -15 and HTTP probes returned 503 on six paths; guard: require tools/list plus health before claiming connected.
- State: Proposed

## 2026-08-07 — BrowserOS vs BrowserClaw log audit (Direct)

- What slowed or confused L? Two similarly named products shared the Mac host: BrowserOS.app 0.47.18 and BrowserClaw 0.48.1.0; process identity was decisive.
- Which instruction should change? none
- Which skill, MCP, or tool is missing? none; context-mode log extraction plus upstream docs were sufficient.
- What operation or error repeated? Old `browseros_server` crash reports repeatedly showed `EXC_BAD_INSTRUCTION`; guard: health must bind to process identity and successful MCP initialize/tools/list, not an app name or port alone.
- State: fixed now

## 2026-08-07 — FRP edges, Custom GPT, and BrowserClaw release 52694d4 (Full)

- What slowed or confused L? Prior green evidence was stale: direct probes found primary/VUSA on build 140 and VPN2 client using obsolete port 27000.
- Which instruction should change? none
- Which skill, MCP, or tool is missing? Proposed: one built-in per-edge authenticated canary that supports SNI/forced-IP resolution and nested MCP child calls.
- What operation or error repeated? FRP restart loop and failover proxy conflicts repeated for minutes; guard: endpoint-port regression, bounded unit/child watchdog, cooldown, and post-push per-edge canary.
- State: fixed now

## 2026-08-07 — Direct child MCP catalog (Full)

- What slowed or confused L? Existing `mcpAgentsForCapabilities()` was mistaken for Hub public exposure; source tracing found the missing `Beat.MCPAgents` wiring and child alias dispatch.
- Which instruction should change? none.
- Which skill, MCP, or tool is missing? Proposed: a topology query that distinguishes “catalog exists on ShellMCP” from “catalog is transported to Hub and publicly exposed.”
- What operation or error repeated? One reviewer found disabled child publication; guard: direct child aliases must be enabled-only and have a regression test.
- State: needs human decision

## 2026-08-07 — Lazy child MCP health (Full)

- What slowed or confused L? A health refresh lifecycle race was found only by independent review: cancel handle cleanup could overlap `Close()`/next refresh.
- Which instruction should change? none.
- Which skill, MCP, or tool is missing? Proposed: a reusable lifecycle blackbox harness for cancel, single-flight, and service close races.
- What operation or error repeated? Three review passes found P1/P2 test gaps; guard: require race tests plus blocked remote blackbox before claiming health complete.
- State: needs human decision

## 2026-08-08 — Rollout canary and edge trust (Full)

- What slowed or confused L? The rollout script reported failure after a successful HAOS start because it assumed `addon_` container names, and Mac polling hit one DNS edge serving a self-signed fallback certificate.
- Which instruction should change? Treat deploy-script exit as provisional until the actual Supervisor container, process log, and consumer canary are checked.
- Which skill, MCP, or tool is missing? Proposed: a secret-safe per-edge TLS/SNI canary and a rollout script that discovers the Supervisor-generated `app_` container name.
- What operation or error repeated? Mac ShellMCP started with `heartbeat=false` or could not trust the bad edge; guard: set both heartbeat env names, pin/route only a valid edge, and verify direct initialize/tools/list/browser flow.
- State: fixed now
