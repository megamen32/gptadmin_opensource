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

- What slowed or confused L? `system_inspect` returned different errors for `/home/admin` versus `/home`; the target's inspection-root contract is not self-describing.
- Which instruction should change? none
- Which skill, MCP, or tool is missing? Proposed: expose configured inspection roots and executor mode in GPTADMIN schema/status without revealing secrets.
- What operation or error repeated? `shell_exec` failed twice with missing `/usr/bin/sudo`; guard: preflight executor binary and report remediation separately from command failure.
- State: Proposed

## 2026-08-09 — Health Incident Autopilot plan gate (Full)

- What slowed or confused L? The required plan-selection question remained unanswered across three automatic goal continuations.
- Which instruction should change? none; the Full-cycle approval boundary is explicit.
- Which skill, MCP, or tool is missing? none; a human plan selection is required.
- What operation or error repeated? 3 goal turns reached the same selection gate; guard: keep implementation untouched until one plan is named.
- State: needs human decision

## 2026-08-06 — Server-100 ShellMCP repair (Short)

- What slowed or confused L? GPTADMIN shell_exec failed inside the service namespace while SSH could see sudo; the decisive evidence was the live systemd unit and process identity.
- Which instruction should change? none
- Which skill, MCP, or tool is missing? Proposed: a bounded GPTADMIN live deployment helper for atomic drop-in install, rollback receipt, restart, and canary.
- What operation or error repeated? Phone proxy CONNECT failed on four tested ports/paths; guard: separate proxy transport acceptance from external HTTPS success and fall back to server01 only when explicitly authorized.
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

## 2026-08-09 — Health Incident Autopilot research (Full)

- What slowed or confused L? context-mode shell capture broke compound `if/for` commands and later local file-processing calls hung; direct narrow reads recovered the evidence.
- Which instruction should change? Proposed: context-mode shell wrapper should preserve compound commands or emit a fast fallback hint.
- Which skill, MCP, or tool is missing? Proposed: a bounded cross-repo topology/evidence query that avoids full recursive raw output.
- What operation or error repeated? 3 context-mode calls hung (large `rg` batch plus resume-time `ctx_search`); guard: scope searches by repo/file type, use direct fallback, and terminate read-only batches on timeout.
- State: Proposed

## 2026-08-09 — Health Incident Autopilot implementation hardening (Full)

- What slowed or confused L? A durable core fix initially looked green while the HTTP workflow wrapper recomputed `useful_progress` differently; independent Reviewer/Critic caught the cross-layer mismatch and a separate-process SQLite race.
- Which instruction should change? Keep business-state verdicts authoritative at the deepest durable boundary and require fresh black-box/review reruns after wrapper changes.
- Which skill, MCP, or tool is missing? Proposed: a reusable cross-process state-machine canary that checks HTTP status, persisted rows, and returned receipts together.
- What operation or error repeated? Heartbeat-only receipts, oversized Hermes session actors, and unproven fake transport provenance were each exposed only by independent adversarial passes; guard: reject heartbeat-only before persistence, bound every bridge actor, and print real/fake transport provenance in canaries.
- State: Proposed

## 2026-08-09 — Fleet health rollout checkpoint c819ec0 (Full)

- What slowed or confused L? Live verify falsely reported the installed config absent because remote `test -s -- path` is not portable.
- Which instruction should change? none; the runner now uses the portable command and has a regression.
- Which skill, MCP, or tool is missing? none.
- What operation or error repeated? One false readiness receipt; guard: exercise remote shell primitives against the real target before trusting Fleet preconditions.
- State: fixed now

## 2026-08-09 — Health activation approval boundary (Full)

- What slowed or confused L? The same required credential/timer approval remained unanswered across three continuation turns while preflight stayed unchanged.
- Which instruction should change? none; the Lead boundary correctly requires a direct question for secret-write and systemd activation.
- Which skill, MCP, or tool is missing? none.
- What operation or error repeated? Three read-only preflight confirmations showed absent env and inactive timer; guard: stop after the third identical boundary instead of polling.
- State: needs human decision

## 2026-08-09 — Health canary model routing and black-box gate (Full)

- What slowed or confused L? A successful HTTP 200 from legacy OpenCode PATCH hid that the first prompt still used the default model; the real-user Tester also lacked a live Touchpoint surface.
- Which instruction should change? Treat effective provider/model telemetry and request ordering as acceptance evidence; never infer them from an adapter receipt.
- Which skill, MCP, or tool is missing? A stable BrowserOS/Touchpoint real-user surface for the final black-box gate.
- What operation or error repeated? Silent model fallback and `Transport closed`; guard with v2 model-switch-before-prompt, fail-closed tests, and `STOP_MISSING_REAL_SURFACE`.
- State: fixed now; user plan selection remains pending

## 2026-08-09 — Two-stage orchestrator provenance and Fleet credential (Full)

- What slowed or confused L? `omniroute/orchestrator` was accepted by the local session endpoint but did not exist upstream; a durable orchestration object was also initially string-truncated by the core sanitizer.
- Which instruction should change? Separate requested logical roles from proven effective model IDs, order mandatory stage traces first, and test persisted JSON rather than only callback return values.
- Which skill, MCP, or tool is missing? A post-deploy Fleet credential contract test that compares the dedicated health token with the scoped producer token.
- What operation or error repeated? Two live canaries exposed missing final trace retention and a Fleet activation check/write mismatch; guard with v8 timing/provenance canary, `4 passed` activation regression, and live token equality verification.
- State: fixed now; plan selection/remediation and real Touchpoint surface remain pending
## 2026-08-10 — Hermes CLI health remediation review correction
- What changed: separated Hermes observation MCP from the health CLI job, added request-bound execution-profile validation, bounded redacted CLI trace export, real CLI-output progress, a 20-minute watchdog with SIGTERM/SIGKILL escalation, and tracked systemd profile variables.
- Evidence: Agent-Herder build; focused 11 tests; full 105-test suite; diff checks clean; prior live canary and Fleet activation remained no-send. Fresh Reviewer/Critic findings were recorded and fixed; black-box Tester remained `STOP_MISSING_REAL_SURFACE` because Touchpoint transport was unavailable.
- Future shield: never call a stale PID a current-build canary; require fresh restart plus post-restart canary before claiming the seam is live, and treat service-ops 9119 probes separately from the direct Hermes 8644 health endpoint.
- What operation or error repeated? 2 related stale probes: service-ops user DBus/9119 versus direct Hermes 8644; guard is to report direct endpoint authority separately.

## 2026-08-10 — health monitoring Hermes review (Full)

- What slowed or confused L? The reviewed live PID (started 00:05) predated the fresh `dist` build (00:24); a current-build canary cannot be inferred from an active unit.
- Which instruction should change? `/home/admin/.local/share/last-human-commit/current/common/agents/Lead.md`: require fresh PID/start-time evidence after every source rebuild before calling a service canary live.
- Which skill, MCP, or tool is missing? A canonical Agent-Herder restart/status probe is missing from `service-ops`; smallest useful addition is a read-only status plus explicit approval-bound restart for that unit.
- What operation or error repeated? 2 related stale probes: service-ops user DBus/9119 versus direct Hermes 8644; guard is to report direct endpoint authority separately.
- State: needs human decision

## 2026-08-10 — current-dist Hermes receipt parser (Full)

- What slowed or confused L? The first progress-visible Hermes canary returned a valid answer but no native ID because the CLI emitted `Session: <id>` instead of `session_id:`.
- Which instruction should change? none; the adapter must accept documented and observed CLI receipt variants.
- Which skill, MCP, or tool is missing? none; a bounded isolated current-dist canary exposed the gap.
- What operation or error repeated? 1 parser miss; guard is a fixture for both receipt formats plus a current-dist canary asserting native ID, progress, and terminal status.
- State: fixed now

## 2026-08-10 — NoticePlace Hermes handoff (Full)

- What slowed or confused L? Fleet had already switched the live profile to Hermes, but NoticePlace `agent_job_helper.py` still rejected Hermes and expected the old OpenCode model string.
- Which instruction should change? none; keep profile allowlists and deployment examples tested against the applied Fleet profile.
- Which skill, MCP, or tool is missing? none; read-only source/live profile comparison found the mismatch.
- What operation or error repeated? 1 full NoticePlace suite failure remains in an unrelated Telegram severity-route test (`147 passed, 1 failed`); guard is the recorded bounded todo, not an opportunistic fix.
- State: fixed now; live NoticePlace deploy and restart need human decision

## 2026-08-10 — live NoticePlace deployment boundary (Full)

- What slowed or confused L? Source helper hash `36e9bc...` and live `/opt/noticeplace` hash `a166a2...` differ; the source fix cannot be called live from a passing isolated canary.
- Which instruction should change? none; require source/live hash equality after every service deployment before accepting an integration claim.
- Which skill, MCP, or tool is missing? Fleet has health activation workflows but no canonical NoticePlace application deploy adapter; smallest useful capability is a backup-first preview/apply for `/opt/noticeplace` plus notification-center restart proof.
- What operation or error repeated? 1 full suite failure (`notify/tests/test_telegram_controls.py`, `147 passed, 1 failed`); guard remains the bounded Telegram-routing todo.
- State: needs human decision

## 2026-08-10 — Fleet timer verification and pytest path race (Full)

- What slowed or confused L? The approved credential/timer path was live, but a full NoticePlace run referenced a sibling `notify` test file that disappeared before the follow-up inspection; the focused NoticePlace test was green.
- Which instruction should change? none; distinguish a reproducible product failure from a shared-worktree/test-collection race and preserve the bounded todo.
- Which skill, MCP, or tool is missing? Proposed: a canonical Fleet status receipt that reports timer last-run status, target count, and no-send mode without dumping event payloads.
- What operation or error repeated? One nondeterministic full-suite path mismatch; guard is focused tests plus `PYTEST_DISABLE_PLUGIN_AUTOLOAD=1` collection evidence before any Telegram change.
- State: needs human decision

## 2026-08-10 — Fleet runtime deployment seam re-audit (Full)

- What slowed or confused L? The Fleet health workflows are intentionally central SSH fan-out and configuration activation; they do not publish NoticePlace application source or Agent-Herder `dist`, so a source/live mismatch remained after a successful timer rollout.
- Which instruction should change? none; distinguish collector installation, configuration activation, and application-runtime release as separate seams with separate evidence.
- Which skill, MCP, or tool is missing? A canonical backup-first application-runtime deploy workflow for NoticePlace plus Agent-Herder, with preview/apply/verify and no-send canary.
- What operation or error repeated? 1 false assumption that Fleet activation implied source deployment; guard is source/live hash comparison and an explicit restart boundary.
- State: needs human decision
## 2026-08-10 — Health runtime deploy and branch drift (Full)

- What slowed or confused L? The shared GPTAdmin checkout changed branches during the active task, so the previous parent task record was absent even though Fleet/NoticePlace/Agent-Herder runtime evidence remained current.
- Which instruction should change? Require the active branch and task-record path in every deployment receipt; never infer authoritative source from prior thread memory after an external branch switch.
- Which skill, MCP, or tool is missing? Overseer was useful for safety review, but its independent context also needed current branch reconciliation; a compact branch/task/runtime evidence bundle would prevent stale “ASK_USER” findings.
- What operation or error repeated? One pre-apply runtime workflow review exposed hash fail-open, root-backup user-unit rollback, and missing post-apply verification; these were fixed and covered before apply.
- State: fixed now; full business canary and real user-facing surface remain pending

## 2026-08-10 — Touchpoint replacement and BrowserOS registration (Full)

- What slowed or confused L? The Linux BrowserOS daemon was healthy, but the fresh Tester could not see it because Codex MCP registration still pointed at dead port 9200; a live daemon probe alone was insufficient evidence.
- Which instruction should change? Treat “daemon healthy” and “fresh Tester has the semantic tool surface” as separate gates; require a new-harness reload after MCP config changes.
- Which skill, MCP, or tool is missing? The current harness exposes Touchpoint but not the registered BrowserOS namespace; Mac BrowserClaw raw MCP over its documented SSH loopback forward remains the reliable fallback.
- What operation or error repeated? Fresh Testers returned `STOP_MISSING_REAL_SURFACE` despite direct BrowserOS 9000 and Mac BrowserClaw 9010 handshakes; guard: require fresh Tester tool enumeration plus `tabs → snapshot/read` evidence before claiming user-facing acceptance.
- State: BrowserOS registration corrected backup-first; current harness reload and real Health UI canary remain pending

## 2026-08-10 — Hermes BrowserOS CDP drift (Full)

- What slowed or confused L? The capability skill/check expected CDP 9103 while the live BrowserOS service, Hermes config, and tests consistently used 9223; this created a false integration failure.
- Which instruction should change? Derive capability ports from the canonical live service/config and keep the secret-safe check synchronized; require MCP 9000 plus CDP 9223 evidence.
- Which skill, MCP, or tool is missing? Fresh Tester still lacks the registered BrowserOS namespace even after `codex mcp get` reports it enabled; a harness reload boundary must be explicit.
- What operation or error repeated? BrowserOS daemon/MCP was healthy but subagents exposed only Touchpoint; guard: separate daemon health, Codex registration, and fresh Tester tool enumeration.
- State: skill check corrected and green; fresh Tester/user Health topic remains pending

## 2026-08-10 — Linux BrowserClaw parity 6c96c06 (Short)

- What slowed or confused L? “Same as Mac” separates the Mac-only BrowserClaw.app from the supported Linux BrowserOS AppImage; the standalone Linux sidecar also failed its first `--help` with glibc 2.38/2.39 requirements.
- Which instruction should change? Proposed: browser-surface guidance should preflight OS, glibc, and existing MCP/CDP ownership before recommending a sidecar.
- Which skill, MCP, or tool is missing? none; official release metadata plus direct MCP initialize/tools/list were sufficient.
- What operation or error repeated? One sidecar launch failed; guard: preflight binary compatibility and reject a second owner when the existing BrowserOS MCP already passes the Mac-equivalent canary.
- State: fixed now; Health Telegram topic remains a separate business blocker

## 2026-08-10 — Health remediation receipt c2bf01d (Full)

- What slowed or confused L? The first independent review found four real gaps after the local canary: state-derived progress idempotency, prefix-form secret leakage, malformed callback exceptions, and unbounded agent elapsed time.
- Which instruction should change? Proposed: require a fresh Reviewer rerun and Critic after every CHANGES_REQUIRED fix set; current multi-agent thread limit prevented both reruns.
- Which skill, MCP, or tool is missing? none; the missing capability was reviewer-thread capacity, not a domain tool.
- What operation or error repeated? Full NoticePlace invocations were slow in journal commit wait, while focused suites were stable; guard: use focused gates first, then run full suite only after the diff settles.
- State: fixed now; production deploy and live Telegram/Hermes gates remain pending
## 2026-08-10 — GrepMesh MCP Full planning (handoff)

- What slowed or confused L? `ctx_execute_file` rejected skill files outside its project root, and wrapped shell `if ... then` commands failed before execution.
- Which instruction should change? context-mode skill: document a safe external-skill read fallback and preserve shell compound-command syntax when injecting `NODE_OPTIONS`.
- Which skill, MCP, or tool is missing? Proposed: a host-authorized skill-reader or context-mode mode for absolute instruction files.
- What operation or error repeated? 2 external-file access blocks and 2 `unexpected token then` wrapper errors; guard: use direct `sed` only for skill instructions and `test ... && ... || ...` for probes.
- State: Proposed

## 2026-08-10 — GrepMesh MCP normal plan selection (approval wait)

- What slowed or confused L? none; the user selected the recommended plan directly.
- Which instruction should change? none
- Which skill, MCP, or tool is missing? none; graphify fast-path confirmed the existing managed-MCP ownership.
- What operation or error repeated? 1 read-only graphify query; no repeated error, guard: preserve the isolated `grepmesh/` root and existing lifecycle owners.
- State: needs human decision

## 2026-08-10 — GrepMesh MCP implementation and Inspector handoff (Full)

- What slowed or confused L? The first custom Streamable HTTP adapter always
  advertised `2026-07-28`, which the independent Inspector rejected; later
  black-box runs also exposed a temporary listener-readiness race and stale
  test processes.
- Which instruction should change? Require an independent MCP consumer after
  every transport change, explicit protocol negotiation evidence, bounded
  listener readiness, and a final process-cleanup audit before handoff.
- Which skill, MCP, or tool is missing? none; the official Inspector CLI via
  ephemeral `npx` supplied the missing real surface.
- What operation or error repeated? Unit/e2e green initially masked consumer
  incompatibility; guard: raw Inspector `tools/list` plus omitted-host,
  remote-read, partial-failure output is now a release gate.
- State: fixed now; production enrollment/auth/restart and official `rmcp`
  crate migration remain explicit future gates

## 2026-08-10 — Health topic boundary (Full handoff)

- What slowed or confused L? Admin `save_topic` read auto-create only from primary env; the enabled flag existed in route env, so the first POST rejected without creating the topic.
- Which instruction should change? Proposed: NoticePlace admin seam should preflight both documented env sources or expose the source of the auto-create flag in its validation error.
- Which skill, MCP, or tool is missing? none; the deployed Bot-API helper plus protected admin HTTP seam were sufficient.
- What operation or error repeated? 1 configuration rejection, then 1 successful topic creation and 1 route persistence; guard: preflight env ownership before external topic mutation.
- State: needs human decision; no code change made because the user explicitly excluded env changes and Telegram sends.

## 2026-08-11 — Health card ordering regression (Full)

- What slowed or confused L? `service-ops` reported Hermes down because user D-Bus variables were absent and probed stale port `9119`; machine user-systemd showed `hermes-gateway.service` active on `18791`. Separately, the first real health send exposed plan-ordering failure.
- Which instruction should change? Proposed: service-ops status/health must distinguish user-bus probe failure from unit state and support start-only without invoking the tracked-change-blocked updater.
- Which skill, MCP, or tool is missing? none; BrowserOS plus the live NoticePlace DB supplied the missing business proof.
- What operation or error repeated? 2 health cards sent before plans, including 1 synthetic canary; guard added: retry until exactly three validated plans and supersede initial delivery on `health.plans_attached`.
- State: fixed now locally; fresh Reviewer/Critic and production deploy remain pending.

## 2026-08-11 — Health delivery coalescing and lease generation (Short)

- What slowed or confused L? The first delivery fix handled only queued legacy rows and two named delivery-key suffixes; claimed rows and generic Telegram policy roots still produced duplicate sends.
- Which instruction should change? Health reconciliation must inspect every `telegram.*` row, choose one canonical active slot, cancel all other queued/claimed rows under the send lock, and compare the worker's lease generation before external delivery.
- Which skill, MCP, or tool is missing? none; red-first temporary-database reproductions and the selected delivery suite supplied the required evidence.
- What operation or error repeated? Fresh Reviewer/Critic found the same P0 three-row/race class twice; guard: do not deploy until mixed `claimed`, `sent`, and generic-root regressions are green and a fresh pair passes.
- State: fixed locally (`110 passed` selected suite); production queue audit, Fleet deploy, live canary, Hermes egress, remediation, and fresh black-box Tester remain pending.

## 2026-08-11 — GrepMesh rg-only federation (Full)

- What slowed or confused L? The untracked GrepMesh tree plus a Worker shutdown left a partial source edit; ownership and compile checks had to be re-established before each gate.
- Which instruction should change? SHARED_WORKTREE: after a child shutdown, explicitly inspect and label partial owned paths before reassignment.
- Which skill, MCP, or tool is missing? none; the official MCP Inspector CLI supplied the required real consumer surface.
- What operation or error repeated? 2 wrong-root combined commands (`cargo`/`go`); guard: run each tool with its explicit project cwd and record exit code separately.
- State: fixed now; production install/auth/restart and five-host rollout remain explicit future gates.

## 2026-08-11 — GrepMesh deploy preflight and Earn or Halt MCP canary (Full)

- What slowed or confused L? No GrepMesh deploy manifest/target list existed; a broad `rg` over `/home` also produced multi-megabyte dependency output before path scoping was tightened.
- Which instruction should change? SHARED_WORKTREE or graphify: provide a bounded source-search recipe that excludes caches, dependencies, task logs, and generated artifacts by default.
- Which skill, MCP, or tool is missing? A GrepMesh rollout/preview skill is missing; the existing `lhc-rollout` manifest targets LastHumanCommit and cannot deploy GrepMesh.
- What operation or error repeated? 1 artifact/unit naming mismatch (`grepmesh` vs `/usr/local/bin/grepmesh-mcp`); guard: require an artifact-to-ExecStart check in the deployment preview.
- State: Proposed; no production mutation made.

## 2026-08-11 — GrepMesh recovery vertical slice
- What slowed or confused L? The product gap was deployment/trust topology, not another local search benchmark.
- Which instruction should change? Require a real transport/auth preflight and a release artifact preview before any service restart claim.
- Which skill, MCP, or tool is missing? A first-class GrepMesh rollout adapter for GPTAdmin ShellMCP is missing; current helper is SSH/local oriented.
- What operation or error repeated? Peer env was absent on both hosts; guard: fail closed on non-loopback bind and ask for explicit trust-material authority.
- State: code/tests/release/preview complete; install and canary blocked on peer trust material.

## 2026-08-11 — GrepMesh rollout provenance preflight
- What slowed or confused L? A manifest pinned to its own checkout HEAD made a reproducible committed rollout impossible.
- Which instruction should change? Deployment confirmation must bind the manifest, unit, generated config, artifact, and a committed binary-source revision separately.
- Which skill, MCP, or tool is missing? GPTAdmin ShellMCP needs a rollout adapter that can transfer sealed artifacts without falling back to SSH.
- What operation or error repeated? server-88 home traversal would fail for a new service user; guard: preflight directory mode and encode a minimum supplementary group in the unit.
- State: committed two-stage provenance and permission fix; production token/install gate remains explicit.

## 2026-08-11 — GrepMesh deployment block
- What slowed or confused L? The secure transport was available but did not imply authority to mint shared inter-node credentials.
- Which instruction should change? Deployment requests should specify whether a missing peer secret may be generated or must come from an existing secret authority.
- Which skill, MCP, or tool is missing? GPTAdmin needs an auditable create-or-reference peer-secret workflow.
- What operation or error repeated? Three continuations reached the absent `peer.env` gate; guard: mark the goal blocked instead of retrying a credential-creating action.
- State: blocked pending one explicit authorization or secret reference.

## 2026-08-11 — Dual-agent GrepMesh speed and all-host source trace (Full)

- What slowed or confused L? Broad GrepMesh roots plus server load around 47-51 made the first MCP agent exceed 120 seconds; a local canary could not cross the remote upstream boundary.
- Which instruction should change? GrepMesh task guidance should require bounded root aliases and a remote-peer target before comparing MCP against bare `rg`.
- Which skill, MCP, or tool is missing? A production GrepMesh mesh/deployment manifest is missing; the temporary single-node MCP cannot inspect server-88.
- What operation or error repeated? 5 SSH probes used nonexistent alias `server-88` before the configured alias `88` succeeded; guard: validate SSH aliases from `~/.ssh/config` before remote probes.
- State: Proposed; no production mutation made.
# 2026-08-12 — GrepMesh business-result drift

- Friction: source repair and A/B baseline were ready, but Lead spent repeated
  Overseer/Reviewer/task-lineage cycles while the live rollout remained undone.
- Time: the active P0 began about 07:09; a later 07:50 task record reset the
  clock to 30/90 minutes. At 09:37 actual elapsed was about 148 minutes, already
  58 minutes over the stated maximum.
- Owning instructions: mandatory serial Overseer/Reviewer/Critic/Tester gates,
  lifecycle snapshots, and exact restart approval displaced the shortest path.
- Useful gate: Reviewer caught the real exit-2 false-success regression.
- Waste: missing-context reconstruction and repeated serial audits added no
  live-canary delta; elapsed-time control was not enforced.
- Proposed: estimates inherit actual objective start; after one correctness
  review, ask one rollout question and stop all process-only gates until answer.
- State: Proposed; live rollout still needs the existing explicit restart gate.
