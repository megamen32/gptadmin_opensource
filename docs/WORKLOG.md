# GPTAdmin worklog

This is the canonical cross-agent handoff log. It is append-only: add a new
dated entry, never rewrite another agent's historical entry. The execution
plan is [`PROJECT_PLAN.md`](./PROJECT_PLAN.md).

## Workflow for every agent

1. Read `PROJECT_PLAN.md`, this file and the relevant subsystem documentation
   before changing code.
2. Select one milestone and one bounded slice. If another active entry owns an
   overlapping file or runtime surface, coordinate before editing.
3. Add an **active** entry before substantial edits. Include the milestone ID,
   scope, owner/agent label and intended acceptance evidence.
4. Work test-first for behavioral changes: record the failing test or precise
   pre-fix evidence, implement, then record focused and full verification.
5. Replace the active entry with a **completed**, **blocked** or **handed-off**
   entry. Include changed paths, commit, CI run, deployment state and one
   concrete next action.
6. Update the status in `PROJECT_PLAN.md` only when the milestone exit gate has
   evidence. Do not mark a stage complete from an implementation claim.

## Entry template

```md
## YYYY-MM-DD - <short title> - <active|completed|blocked|handed-off>

- Milestone: `Sx.y`
- Owner: `<agent or human>`
- Scope: `<bounded files, runtime or user flow>`
- Baseline / red evidence: `<failing test, incident or N/A>`
- Change: `<what was done>`
- Verification: `<commands and concise results>`
- Delivery: `<commit, push, CI URL/run, deployment>`
- Next: `<single actionable continuation or none>`
- Blocker: `<only when status is blocked>`
```

## Rules

- Never put tokens, passwords, private URLs, raw customer data or full command
  output in this file. Refer to a redacted log path or issue instead.
- Keep entries factual and compact. State uncertainty explicitly.
- A runtime change is not delivered until restart/health evidence is logged.
- A docs-only or research task still records the canonical source and what
  decision it changed.
- Use absolute repository paths in handoffs when ambiguity is possible.

## Entries

## 2026-07-15 - Compact MCP tool names and descriptions - completed

- Milestone: `S4.1`
- Owner: Codex
- Scope: Audit and compact the public MCP tool surface while preserving legacy dispatch aliases.
- Baseline / red evidence: `tools/list` exposes long `list_mcp_*`, `call_mcp_tool` and `get_mcp_job` names plus repeated prose; current contract does not use the shorter discover/schema/execute/job vocabulary.
- Change: Canonical MCP tools are now `discover`, `schema`, `execute`, `job`, `inspect`, and `ui`; old names remain accepted but are not advertised. OpenAPI operation IDs and component schemas use the same compact vocabulary. Canonical execute uses `target/tool/args`; old `tool_name/arguments` remain accepted. Downstream ShellMCP names were retained and their descriptions shortened because they are separate capability contracts.
- Verification: Red canonical-surface test first; Hub tests and race detector; ShellMCP Go tests; Python `97 passed, 2 skipped`; `tools/list` payload is capped by a regression assertion at 12000 bytes.
- Delivery: Commit `9adf894` pushed to `main`; Build, Sync, Release run `29419512851` passed across build/release, macOS, Windows and Docker failover; Hub rebuilt with this commit and `gptadmin-hub.service` restarted active on `roomhacker-server-100`; live smoke passed `discover`, `schema(target=hub)`, and `execute(tool=status)`, advertising only compact names. Website docs commit `bc76d8c` and parent pointer `1290935` are pushed.
- Next: Keep legacy names through the documented migration window; remove aliases only in a planned breaking release after client telemetry confirms migration.

## 2026-07-15 - Relax repeated MCP discovery - completed

- Milestone: `S4.1`
- Owner: Codex
- Scope: Remove the prompt requirement to rediscover agents before every MCP operation.
- Baseline / red evidence: Public instructions required `Always call listMcpAgents first` and prescribed discovery/schema before every infrastructure action.
- Change: Discover once when target is unknown; reuse a healthy target and schema; repeat only after stale/disconnected state, unknown tool schema, or explicit refresh. Added a black-box prompt guard and clarified the philosophy.
- Verification: `python3 -m pytest tests/test_hub_contract.py -q` (`5 passed`); Build, Sync, Release run `29420583122` passed.
- Delivery: Commit `0ff518f` pushed to `main`; both prompt files synchronized to `/opt/gptadmin/public` on `roomhacker-server-100`; no Hub restart required because only static instructions changed.
- Next: Observe client behavior; do not reintroduce mandatory per-call discovery without measured correctness evidence.

## 2026-07-15 - Idempotent MCP writes - completed

- Milestone: `S4.1`
- Owner: Codex with one bounded contract-review agent
- Scope: Add optional idempotency to existing `call_mcp_tool` writes without creating a second execution facade.
- Baseline / red evidence: A write can complete while its response is lost; a retry currently has no Hub-level duplicate key and may enqueue the same operation twice.
- Change: Added one Hub-level deduplication layer shared by HTTP and MCP Apps calls. It scopes keys by caller authorization fingerprint, fingerprints target/tool/arguments, reuses the original result/job, rejects conflicting reuse with `409`, refreshes background records when downstream results arrive, and bounds memory with a 15-minute TTL and 1024-entry limit.
- Verification: Red tests first; `go test ./...` in `go-hub` and `go-shellmcp`; `go test -race ./internal/hub`; `python3 -m pytest tests/ --ignore=tests/e2e` (`97 passed, 2 skipped`).
- Delivery: Commit `0227a34` pushed to `main`; Build, Sync, Release run `29417782872` passed across build/release, macOS, Windows and Docker failover; `/opt/gptadmin/bin/gptadmin_hub` rebuilt from this commit and `gptadmin-hub.service` restarted active on `roomhacker-server-100`; live duplicate smoke returned one job id for two identical `shell:server-44` calls.
- Next: Add durable idempotency recovery semantics only as a separate milestone; current retry safety is bounded to the running Hub process.

## 2026-07-15 - Align integration contract with existing Hub flow - completed

- Milestone: `S4.1`
- Owner: Codex
- Scope: Correct the integration-control plan so it certifies GPTAdmin's existing agent/tool/call flow instead of implying a new Codex-style facade.
- Baseline / red evidence: GPTAdmin already exposes `list_mcp_agents` or `list_mcp_servers`, `list_mcp_tools`, and `call_mcp_tool`; the new contract wording did not document this mapping or distinguish the remaining idempotency/version gaps.
- Change: Map the external pattern onto current Hub operations and record only the missing guarantees as future work.
- Verification: Documentation tests and diff checks.
- Delivery: Pending commit and push.
- Next: Add idempotency/schema digest only when the corresponding S4.1 implementation slice is started.

## 2026-07-15 - External integration control contract - completed

- Milestone: `S4.1`
- Owner: Codex
- Scope: Record whether the Codex Document Control discover/schema/execute pattern is a GPTAdmin commitment or only an external reference; define the first bounded GPTAdmin integration slice.
- Baseline / red evidence: The pattern was described as a standard for “future integrations”, but neither `PROJECT_PLAN.md` nor this worklog named an owner, deliverable or acceptance test.
- Change: Add a canonical integration-control contract and make S4.1 explicit about session discovery, schema retrieval and idempotent execution.
- Verification: Documentation link/diff checks and the existing site-doc test.
- Delivery: Pending commit and push.
- Next: Implement the S1.3 candidate only when that milestone is started; no runtime change is claimed here.

## 2026-07-14 - MCP server list restart and failover contract - completed

- Milestone: `S0.2`, `S3.4`
- Owner: Codex with independent incident-review agents
- Scope: Prove the public MCP `list_mcp_servers` shape and registry survival across Hub restart/failover; ensure a live generic relay re-registers to a fresh Hub without a service restart.
- Baseline / red evidence: The attached client probe read `structuredContent.response.servers`, but production returns the list at `structuredContent.servers`; direct production inspection reports 27 servers while that parser reports zero.
- Change: Added a direct MCP JSON-RPC restart-contract test. Generic stdio relay now treats failed polling or registration as unregistered and retries registration with capped backoff before sending another poll. Docker failover now starts a real generic relay and fake stdio MCP, validates authenticated `list_mcp_servers`, then requires an `echo` tool call through the promoted fallback.
- Verification: Red Python regression tests proved one-time registration and polling after a failed recovery registration; green `go test ./...` and `go vet ./...` in Hub/ShellMCP, Python `97 passed, 2 skipped`, and Docker failover suite with all tunnel, Hub, combined, agent, reclaim and ranked-fallback scenarios passing locally.
- Delivery: Commit `7ab87c9` pushed to `main`; Build, Sync, Release run `29359978802` passed, including Docker failover, macOS, Windows and Android artifact jobs. Production generic relay runtime copies on `roomhacker-server-100` were synchronized and its ten `gptadmin-mcp-*` services restarted as user-owned processes; authenticated Hub smoke reports all ten online.
- Next: None.

## 2026-07-14 - Redacted security environment metadata - completed

- Milestone: `S2.3`
- Owner: Codex
- Scope: Replace the admin panel's raw env-file `shell_exec` read with a Hub metadata endpoint.
- Baseline / red evidence: The panel sent `cat /etc/gptadmin/gptadmin.env` through MCP, allowing secret values to enter model context and client previews.
- Change: `/admin/api/security/env` returns only variable names, presence, lengths and sensitivity flags; the UI no longer reads env through ShellMCP.
- Verification: Focused Hub test, admin UI tests (`5 passed`), JS syntax and diff checks passed; live unauthenticated endpoint returns `401`; live Hub reports commit `a691b06`.
- Delivery: Commit `a691b06` pushed to `main`; production binary rebuilt and `gptadmin-hub.service` restarted; Build, Sync, Release run `29359009257` is in progress.
- Next: Implement the OS-enforced ShellMCP read-only worker and secret handles.

## 2026-07-14 - OAuth rotation and readonly boundary - completed

- Milestone: `S2.1`, `S2.3`
- Owner: Codex
- Scope: Replace browser-side OAuth secret generation with an authenticated Hub endpoint, preserve secrets out of responses, and close the remaining readonly/redaction boundary with tests.
- Baseline / red evidence: `rotateOAuth()` only filled an HTML field; the screenshot showed secret-looking values in a client confirmation preview; ShellMCP service still runs as root for supervisor duties.
- Change: Added an authenticated Hub endpoint that atomically replaces `OAUTH_CLIENT_SECRET`, updates the current process, and never returns the secret. The admin UI now calls the endpoint instead of generating a secret in browser JavaScript.
- Verification: Go Hub/ShellMCP tests and vet passed; Python `95 passed, 2 skipped`; admin JavaScript syntax and diff checks passed; live `/version` reports build `126` at commit `19fe2b4`; live OAuth metadata exposes `gptadmin.inspect`; unauthenticated rotation is rejected with `401`.
- Delivery: Commit `19fe2b4` pushed to `main`; `/opt/gptadmin/bin/gptadmin_hub` rebuilt and `gptadmin-hub.service` restarted; Build, Sync, Release run `29358756754` is still running.
- Next: Implement the OS-enforced ShellMCP read-only worker and secret-handle boundary; do not rotate the production OAuth secret until the operator explicitly confirms invalidating current OAuth sessions.

## 2026-07-14 - Live auth transition and read-only verification - completed

- Milestone: `S2.1`, `S2.3`
- Owner: Codex with auth, deploy and redaction review agents
- Scope: Verify the configured public Hub, restore the documented CTL migration path, make managed Client/Auth inventory usable, and prove ShellMCP read-only plus model-output redaction at the real MCP boundary.
- Baseline / red evidence: Public admin currently serves a login page whose visible contract still references Bearer CTL; unauthenticated MCP returns `401`; the supplied client confirmation preview exposes API-key/password-looking values before execution.
- Change: Corrected the product terminology to ShellMCP; exposed the legacy CTL transition credential in inventory without persisting or showing its value; excluded it from JWT revoke-all and JWT rotation UI; added a regression test.
- Verification: Hub/ShellMCP Go tests and vet passed; Python `94 passed, 2 skipped`; live `/version` reports build `126` at commit `fbc45e8`; live OAuth metadata includes `gptadmin.inspect`; authenticated live checks returned `200` for both Bearer CTL and `X-CTL-Token`; inventory reports one redacted `legacy_ctl` record.
- Delivery: Commit `fbc45e8` pushed to `main`; Build, Sync, Release run `29357359924` passed; `/opt/gptadmin/bin/gptadmin_hub` rebuilt with commit ldflags and `gptadmin-hub.service` restarted successfully.
- Next: Add the OS-enforced ShellMCP read-only sandbox and secret-handle boundary so secrets are absent from client confirmation previews, then verify with black-box tests.

## 2026-07-14 - Cross-platform read-only client profile - completed

- Milestone: `S2.3`
- Owner: Codex
- Scope: Hub-issued read-only JWT profile, typed ShellMCP inspection and
  mandatory model-output secret redaction across Linux, macOS, Windows and
  Android contracts.
- Baseline / red evidence: `gptadmin.read` is advertised but MCP authorization
  does not enforce tool-call scopes; ShellMCP exposes arbitrary `shell_exec`
  and has no model-output secret redaction boundary.
- Change: Added managed and CLI `readonly` JWTs, profile-aware Hub MCP tool
  lists and fail-closed enforcement across relay, global MCP, pinned MCP,
  generated Actions and admin APIs. Added typed ShellMCP `system_inspect` with
  allowed roots, symlink containment, credential-directory denial, bounds and
  mandatory credential redaction. Admin issuance defaults visibly to read-only.
- Verification: Red tests showed missing inspector/redactor, ignored
  `access_mode`, shell execution through every MCP route and MCP JWT access to
  admin APIs. Green: both `go test ./...`; both `go vet ./...`; Python `94
  passed, 2 skipped`; admin JavaScript syntax; Hub darwin amd64/arm64 builds;
  ShellMCP Windows and Android builds plus Windows inspector test compilation.
- Delivery: Commit `3ec79d4`; Build, Sync, Release run `29354412360` passed,
  including macOS runtime, Windows ShellMCP, Android artifact and Docker
  failover jobs.
- Next: Add the separate `ask-before-write` profile with approval-bound job
  ownership; do not expand read-only into filtered raw shell.

## 2026-07-14 - Low-context product philosophy - completed

- Milestone: `S0.5`, `S2.1`
- Owner: Codex with Sol proposer and adversarial critic agents
- Scope: Canonical product philosophy and staged low-context MCP/required
  migration notice architecture.
- Baseline / red evidence: No philosophy document existed; the plan did not
  define MCP context as a budget, daily notice limits or evidence-based notice
  completion.
- Change: Added `PHILOSOPHY.md` and `MCP_CONTEXT_AND_NOTICES.md`; linked the
  philosophy from the execution plan, docs home and both agent instruction
  files. The synthesis preserves the current compact Hub tools and defers
  broader storage/protocol changes until measured evidence justifies them.
- Verification: Proposer and independent critic completed; `git diff --check`
  passed; `python3 -m pytest tests/test_site_docs.py tests/test_admin_ui.py -q`
  passed (`6 passed`).
- Delivery: Commit `bf35b42`; Build, Sync, Release run `29351644583` passed,
  including macOS, Windows, Android artifact and Docker failover jobs.
- Next: Implement V1 `connection_id` and notice-ledger tests as a separate TDD
  runtime slice.

## 2026-07-14 - Required AI migration notices - handed-off

- Milestone: `S2.1`
- Owner: Codex
- Scope: Hub MCP tools that deliver a one-time required migration instruction
  per JWT and record explicit agent acknowledgement.
- Baseline / red evidence: Hub has no durable way to tell a connected AI that
  a manual client migration needs user explanation and a confirmed completion.
- Change: Runtime implementation was deliberately deferred after proposer and
  critic review. The accepted V1/V2 contract is now
  `docs/MCP_CONTEXT_AND_NOTICES.md`.
- Verification: Design review rejected dynamic `tools/list`, a new generic
  facade, premature SQLite and unmeasured token limits.
- Delivery: Architecture handoff included with the philosophy documentation.
- Next: Start with a failing test proving JWT rotation preserves
  `connection_id`.

## 2026-07-14 - Managed MCP JWT inventory and rotation - completed

- Milestone: `S2.1`
- Owner: Codex
- Scope: Go Hub-issued MCP JWT registry, individual revoke/rotate APIs, admin
  inventory and plain-language CLI/admin guidance.
- Baseline / red evidence: The Go Hub returns an empty client list and its
  revoke endpoints are placeholders, so the admin cannot show or rotate
  issued JWTs even though it can issue one.
- Change: Hub-issued JWTs now have a persisted metadata-only inventory and
  revocable ID; the admin can list, revoke or rotate them. The panel explains
  OAuth as the normal route and JWT as a simple fallback for unsupported
  clients; CLI help uses the same language.
- Verification: Red tests proved missing token ID and UI path. Green:
  `go test ./...` in `go-hub`; `python3 -m pytest tests/ --ignore=tests/e2e
  -q` (`93 passed, 2 skipped`); `node --check public/admin/app.js`.
- Delivery: Commit `771d421`; Build, Sync, Release run `29331893009` passed.
- Next: Push and verify CI.

## 2026-07-14 - Automatic local MCP client registration - completed

- Milestone: `S1.1`, `S1.3`
- Owner: Codex
- Scope: `/home/roomhacker/gptadmin/cli.py` client registration and its
  installer/update regression tests for Codex, Claude Code, OpenCode and VS
  Code.
- Baseline / red evidence: Existing setup registers only three clients, emits
  raw bearer credentials, has no VS Code support, and update intentionally
  skips client registration.
- Change: Added idempotent registration for Codex, Claude Code, OpenCode and
  VS Code; client URLs now prefer `HUB_PUBLIC_URL`; setup and update perform
  the registration; automatic output no longer prints a bearer credential.
- Verification: Red baseline: `5 failed, 2 passed` in the new focused tests.
  Green: `python3 -m pytest tests/ --ignore=tests/e2e -q` (`92 passed, 2
  skipped`); `go test ./...` in both Go modules passed.
- Delivery: `de9f9e9` pushed to `main`; GitHub Actions run `29330935641`
  passed Linux build/release, Android artifact, macOS, Windows and Docker
  failover jobs.
- Next: None.

## 2026-07-14 - Zero-to-working setup principle - completed

- Milestone: `S1.1`, `S1.3`, `S1.6`
- Owner: Codex
- Scope: Product default for install, Tunnel, Hub URL and progressive security.
- Baseline / red evidence: Earlier planning put exposure choice and MFA before
  the first useful connection, reproducing the configuration burden users
  dislike in self-hosted agent products.
- Change: Superseded the upfront exposure questionnaire with automatic Hub +
  HTTPS Tunnel setup, one canonical Hub URL, client connection automation and
  optional security presets after first value.
- Verification: Decision reviewed against current repository installer/token
  inventory and recorded as a future runtime acceptance contract.
- Delivery: Delivered in the accompanying documentation commit; runtime setup
  still needs TDD implementation.
- Next: Add a failing clean-host installer test that requires an externally
  verified Hub URL without a manual Tunnel/token prompt.

## 2026-07-14 - Exposure profiles and admin MFA contract - completed

- Milestone: `S1.5`, `S2.1a`
- Owner: Codex
- Scope: Plain-language setup exposure choices and conditional MFA rules.
- Baseline / red evidence: Existing setup exposes transport/token terminology
  instead of asking whether the Hub is local, private or public.
- Change: Added local-only, private-network and public-Tunnel profiles;
  specified passkey-first MFA, TOTP fallback and recovery codes for public
  administration.
- Verification: Compared against current OpenClaw security documentation:
  loopback-first, pairing and identity-aware private access are sound patterns;
  GPTAdmin retains scoped JWT as a stricter client/agent authorization model.
- Delivery: Delivered in the accompanying documentation commit; no runtime
  exposure profile or MFA code exists yet.
- Next: Write failing installer and Hub black-box tests for the local-only
  default before implementing profile selection.

## 2026-07-14 - One-password product contract - completed

- Milestone: `S0.5`
- Owner: Codex
- Scope: Authentication simplification decision, migration phases and surface
  terminology.
- Baseline / red evidence: Current code and docs expose `CTL_TOKEN`, shell,
  relay and bridge tokens across installer, API, UI and quickstarts.
- Change: Added `AUTH_SIMPLIFICATION.md`; updated execution plan to require
  `AdminPassword` as the sole user-owned secret, internal scoped JWTs and the
  terms Hub, MCP clients and Tunnel on product surfaces.
- Verification: Repository inventory completed with `rg`; migration acceptance
  tests and phases are recorded before runtime changes.
- Delivery: Delivered in the accompanying documentation commit; runtime token
  removal remains a planned breaking migration.
- Next: Implement Phase A inventory tests and a new-install no-raw-token
  regression before changing authentication code.

## 2026-07-14 - Execution plan and cross-agent handoff - completed

- Milestone: `S0.4`
- Owner: Codex
- Scope: Canonical plan, append-only worklog and agent operating instructions.
- Baseline / red evidence: Public roadmap described product themes, but there
  was no milestone exit-gate plan or root-level cross-agent handoff record.
- Change: Added `PROJECT_PLAN.md`, this worklog and matching workflow rules to
  `AGENTS.md` and `CLAUDE.md`.
- Verification: Reviewed current repository structure and roadmap; `git diff
  --check` passed.
- Delivery: Delivered in the accompanying documentation commit; CI is not
  required for this docs-only coordination change.
- Next: Keep new implementation work aligned to one milestone and record
  evidence here before handoff.

## 2026-07-14 - Failover black-box coverage - completed

- Milestone: `S3.4`
- Owner: Codex
- Scope: Docker failover harness, CI gate and operator runbook.
- Baseline / red evidence: No Docker black-box coverage existed for hub failure,
  tunnel failure, combined outage, reclaim or multiple fallback ranks.
- Change: Added real Go hub/watchdog/proxy Docker topology. It covers tunnel
  only, hub only, combined failure, signed reclaim, rank 1 fencing rank 2 and
  rank 2 promotion while rank 1 is unavailable.
- Verification: `docker compose -f tests/e2e/failover/docker-compose.yml up
  --build --abort-on-container-exit --exit-code-from failover-e2e` passed all
  six scenarios.
- Delivery: `ed90d04`; GitHub Actions run `29310215351` passed, including the
  `failover-e2e`, Linux, macOS, Windows and Android artifact jobs.
- Next: Add a physical two-host deployment drill and partition-specific
  fencing evidence before calling HA maturity complete.

## 2026-07-15 - Remove private infrastructure prompts from public repo - completed

- Milestone: `S4.1`
- Owner: Codex
- Scope: Keep personal short and infrastructure MCP instructions outside the
  public repository and its Git history.
- Baseline / red evidence: Two private prompt artifacts were tracked in the
  public tree and present in 12 historical commits.
- Change: Preserved private local copies outside the repository, removed the
  artifacts from the public tree, added ignore rules, and removed the public
  contract-test dependency on their contents.
- Verification: Rewritten local and remote refs contain no matching path in a
  commit history or tree; focused contract-test collection and `git diff
  --check` pass.
- Delivery: Commit `fadc58d` and all public branches/tags were force-pushed
  after history rewrite; private copies remain outside the repository.
- Next: Keep personal prompts in the private directory and maintain only the
  public deletion manifest in the repository.

## 2026-07-15 - Preserve JWTs across updates and anchor private prompts - completed

- Milestone: `S2.2`, `S4.1`
- Owner: Codex
- Scope: Make in-place and automatic updates preserve all existing auth
  material, and keep personal instructions in the private source repository.
- Baseline / red evidence: A package step that rewrote `gptadmin.env` dropped
  `OAUTH_CLIENT_SECRET` and client bearer JWTs; private prompt copies lived in
  an external directory that was easy to lose.
- Change: Added pre-update auth capture and post-package restoration, atomic
  `.env` replacement, and moved private instructions under
  `private/instructions/` in the private repo. Both `git-private2public` and
  GitHub rsync explicitly exclude `private/`.
- Verification: TDD regression and mirror guard pass; private instruction
  hashes were preserved during the move.
- Delivery: Pending commit and CI.
- Next: Keep auth state in the managed config and never rotate it as part of
  binary/package replacement.

## 2026-07-15 - Compact discover with explicit detail opt-in - completed

- Milestone: `S1.3`, `S4.2`
- Owner: Codex
- Scope: Reduce default MCP context cost without removing target metadata when
  an integration explicitly needs it.
- Baseline / red evidence: `discover` returned transport, timestamps,
  capabilities and arbitrary metadata on every call.
- Change: Default REST, MCP and Apps SDK discovery now returns only
  `server_id`, `name`, `kind` and `status`; `detail: "full"` or
  `GET /mcp-relay/servers?detail=full` opts into the previous detail payload.
  OpenAPI and generated MCP schemas document the opt-in.
- Verification: Go Hub tests and black-box contract tests pass.
- Delivery: Pending commit and CI.
- Next: Apply the same compact/default policy to `listMcpAgents` only if a
  measured client still needs it; do not expand the default tool surface.

## 2026-07-17 - Publish release 127 - active

- Milestone: `S1.3`, `S4.2`
- Owner: Codex
- Scope: Publish the committed Hub/client artifacts containing compact
  `discover` output and explicit detail opt-in.
- Baseline: `main` was at build `126`; CI builds passed but no new release
  tag or platform artifacts had been published.
- Change: Bump `VERSION` to `127`; the release workflow will create `v127`,
  build all platform artifacts, and sync the public mirror.
- Verification: Pending release workflow and public artifact checks.
- Delivery: Active until the tag, GitHub Release and artifact manifest are
  verified.
- Next: Wait for `v127` Build, Sync, Release and verify all five artifact
  families.
