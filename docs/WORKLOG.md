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

## 2026-07-24 - Final linear acceptance after failover delivery - completed

- Milestone: `S0.1` / `S3.4` / repository acceptance gate
- Owner: `Codex`
- Scope: Verify the current single-branch vertex after the clean-clone failover delivery.
- Baseline / red evidence: The prior failover drill exposed executable-mode, source-checkout path, port-collision, binary-lifetime and reclaim-authentication defects; each now has a recorded regression or black-box scenario.
- Change: No additional runtime change; record final acceptance for the current vertex.
- Verification: Python `200 passed, 3 skipped`; completion matrix `12 passed`; Hub/ShellMCP/ProxyRelay full tests, race and vet passed; Darwin Hub arm64/amd64 cross-builds passed; clean clone `/tmp/gptadmin-final-clean-4nf5uC` at commit `0bd860a91f7d3670e03e8c38ba7e6f3b570cacb4` ran all seven failover black-box scenarios successfully; `git diff --check` passed.
- Delivery: Linear commits `4d26a3e43963c5197d95faca86949da5ddfe637f` and `0bd860a91f7d3670e03e8c38ba7e6f3b570cacb4`; no push, deployment or merge; preserve the unrelated untracked remote-secret-ingress plan.
- Next: Close only external acceptance gates (native hosts, real collector, physical fallback hosts, signed/public release and third-party adapters) when their owning environments provide evidence.

## 2026-07-24 - Extension SDK contract documentation - completed

- Milestone: `S4.4`
- Owner: `Codex`
- Scope: Restore the public extension SDK contract, add a reference adapter, and prove its live Hub/relay path.
- Baseline / red evidence: `docs/EXTENSION_SDK.md` was zero bytes; the new documentation contract test failed with an empty document.
- Change: Populated `docs/EXTENSION_SDK.md`, added `mcp_extension_reference.py`, and extended `tests/test_extension_sdk.py` with manifest, stdio lifecycle, and live `discover -> schema -> execute` forwarding checks.
- Verification: `pytest -q tests/test_extension_sdk.py` passes 5 tests; completion-matrix evidence now names the live relay path.
- Delivery: Commit pending; preserve the unrelated untracked remote-secret-ingress plan.
- Next: Keep external independently authored adapter certification separate from this local reference runner.

## 2026-07-24 - OAuth canonical endpoints and code binding - completed

- Milestone: `S0.5` / `S1.3` / `S2.1a`
- Owner: `Codex`
- Scope: Canonicalize OAuth discovery paths and harden authorization-code PKCE/client/redirect binding.
- Baseline / red evidence: New `TestCanonicalOAuthEndpointsRequirePKCEAndBindClient` failed because metadata advertised root paths, missing PKCE was accepted, and a mismatched client/redirect could exchange a code.
- Change: Added `/oauth/authorize` and `/oauth/token` routes, retained root aliases for migration, required PKCE S256, enforced code client/redirect equality, and aligned public docs.
- Verification: Focused canonical OAuth, dynamic client inventory and OAuth/MCP tests pass; full Hub gate remains required after integration.
- Delivery: Commit pending; preserve the unrelated untracked remote-secret-ingress plan.
- Next: Run full Hub/race/vet and add the canonical OAuth check to the completion matrix.

## 2026-07-24 - Legacy credential onboarding boundary - completed

- Milestone: `S0.5` / `S0.1`
- Owner: `Codex`
- Scope: Remove legacy bearer values from Windows installer completion output and public adapter/install onboarding.
- Baseline / red evidence: `tests/test_install_win.py` failed on `Write-Host "Token: $ShellmcpToken"` and public `CTL_TOKEN`/`SHELLMCP_TOKEN` copy instructions.
- Change: Removed the Windows token echo and rewrote public docs to use `/connect` and OAuth Authorization Code + PKCE; internal compatibility variables remain runtime-only.
- Verification: `pytest -q tests/test_install_win.py` passes 2 tests; no secrets were printed.
- Delivery: Commit pending; preserve the unrelated untracked remote-secret-ingress plan.
- Next: Extend the same product-language boundary to remaining native installer/docs paths and verify on a real Windows host.

## 2026-07-24 - CLI connection-status secret boundary - completed

- Milestone: `S0.5`
- Owner: `Codex`
- Scope: Remove internal credential names and prefixes from normal `gptadmin tokens` output.
- Baseline / red evidence: `test_tokens_hides_internal_credential_names_in_normal_output` failed because status included the bridge credential prefix and internal labels.
- Change: Replaced token-oriented output with Hub URL and hidden connection-state language; explicit inspection mode still never reveals values.
- Verification: `pytest -q tests/test_cli_token_deprecation.py` passes 4 tests.
- Delivery: Commit pending; preserve the unrelated untracked remote-secret-ingress plan.
- Next: Run full Python and security matrix after the current integration commit.

## 2026-07-24 - Process-level file sharing acceptance - completed

- Milestone: `S1.3` / `S3.3`
- Owner: `Codex`
- Scope: Exercise `file_backup` through a real ShellMCP process and include it in the file-sharing matrix.
- Baseline / red evidence: Existing coverage stopped at package-level `httptest`/handler tests; no process-level file-sharing check existed.
- Change: Added backup/list/restore process coverage with an isolated temporary backup root and matrix entry.
- Verification: `pytest -q tests/test_shellmcp_contract.py -k file_backup_round_trip_through_process` passes 1 test.
- Delivery: Commit pending; preserve the unrelated untracked remote-secret-ingress plan.
- Next: Run the full ShellMCP contract and retain native-host evidence as a separate gate.

## 2026-07-24 - HAOS image supply-chain parity - completed

- Milestone: `S4.3`
- Owner: `Codex`
- Scope: Add image-level SBOM/provenance metadata and digest verification to HAOS publication workflow.
- Baseline / red evidence: Workflow audit found no `--sbom`, `--provenance`, metadata capture, or image digest verification in the HAOS publish path.
- Change: Added BuildKit SBOM/provenance flags, metadata file and `imagetools inspect` verification before public artifact export.
- Verification: `pytest -q tests/test_release_workflow_contract.py` passes 3 tests.
- Delivery: Commit pending; preserve the unrelated untracked remote-secret-ingress plan.
- Next: Obtain an immutable tagged CI run with published image attestation.

## 2026-07-24 - Clean-clone failover script mode - completed

- Milestone: `S0.1` / `S3.4`
- Owner: `Codex`
- Scope: Make the documented clean-clone failover drill and directly launched FRP fixture executable.
- Baseline / red evidence: Clean clone `/tmp/gptadmin-clean-audit-hYNrLP` ran 32 focused tests and Go gates, then `tests/e2e/failover/run.sh` returned exit `126`; Git recorded mode `100644`.
- Change: Restored executable modes for `tests/e2e/failover/run.sh` and `tests/e2e/failover/fake-frpc`, added mode regressions, made source checkout execution self-contained, and made test ports configurable.
- Verification: Focused mode tests pass; all seven direct black-box scenarios pass.
- Delivery: Code commit `4d26a3e43963c5197d95faca86949da5ddfe637f`; no push, deployment or merge; preserve the unrelated untracked remote-secret-ingress plan.
- Next: Keep the mode and direct-drill checks in the acceptance gate.

## 2026-07-24 - Failover reclaim authentication - completed

- Milestone: `S3.4`
- Owner: `Codex`
- Scope: Make the signed primary reclaim helper satisfy the Hub's authenticated endpoint contract and preserve a real black-box regression.
- Baseline / red evidence: Direct failover drill reached `scenario_primary_reclaim`, then the fallback Hub returned unauthorized because the helper sent no `Authorization` header.
- Change: Added an optional authorization parameter to the reclaim POST helper and passed the shared control credential as a bearer header without exposing it in output.
- Verification: RED request-header regression now passes; all seven direct black-box scenarios pass.
- Delivery: Code commit `4d26a3e43963c5197d95faca86949da5ddfe637f`; no push, deployment or merge; preserve the unrelated untracked remote-secret-ingress plan.
- Next: Keep the reclaim request test and black-box scenario in the acceptance gate.

## 2026-07-24 - Bug ledger status correction - completed

- Milestone: `S2.2` / `S2.3`
- Owner: `Codex`
- Scope: Align the policy-boundary bug title with its already verified fixed status; no runtime behavior changed.
- Baseline / red evidence: The entry body contained the completed RED/green fix and `Status: fixed`, while its title still ended in `open`.
- Change: Corrected the status label only; retained all historical evidence and next-action text.
- Verification: Hub policy tests and the full current acceptance gates remain green; only the unrelated external Windows legacy task and HAOS Supervisor entries remain actionable/open.
- Delivery: Documentation correction on the current linear vertex; no push, deployment or merge.
- Next: Resolve external blockers only in their owning host/deployment tasks.

## 2026-07-24 - Current-head extended acceptance - completed

- Milestone: `S1.3a` / `S2.1a` / `S4.1-S4.4`
- Owner: `Codex`
- Scope: Re-run the full local acceptance after named profile instructions, MCP conformance, passkey enrollment UI, capability catalog and extension manifest changes.
- Baseline / red evidence: Named profile and MCP conformance RED tests exposed missing runtime instruction selection and the nil-request execute panic; passkey/catalog/extension RED contracts exposed missing user-facing surfaces.
- Change: No additional production change in this evidence entry; record the current linear acceptance result.
- Verification: Python `197 passed, 3 skipped`; completion matrix `12 passed`; Hub/ShellMCP/ProxyRelay full tests, race and vet passed; real Chromium virtual-authenticator enrollment `1 passed`; `git diff --check` passed.
- Delivery: Current linear commits include `9f9769d`, `54a10f0`, `d3e8fdd`, `b650515`, `69d0642` and `2568eef`; no push, deployment or merge. Preserve the untracked remote-secret-ingress plan.
- Next: Obtain external OIDC/verification, public client/Tunnel, real collector, clean-host canary, native CI, signed catalog publication and physical fallback-host evidence before closing remaining roadmap gates.

## 2026-07-24 - S4.4 extension conformance delivery identity - completed

- Milestone: `S4.4`
- Owner: `Codex`
- Scope: Attach the immutable commit to the extension manifest schema/validator slice.
- Baseline / red evidence: The reference extension test first failed because no validator existed.
- Change: No additional production change; record the current linear delivery identity.
- Verification: `git show --no-patch --format='%H %s' 69d0642` identifies `feat: add MCP extension manifest conformance`; focused extension/catalog/MCP tests pass.
- Delivery: Commit `69d0642`; no push, deployment or merge performed.
- Next: Add a real third-party adapter runner before closing S4.4.

## 2026-07-24 - S4.2 capability catalog delivery identity - completed

- Milestone: `S4.2`
- Owner: `Codex`
- Scope: Attach the immutable commit to the curated capability catalog and explicit activation acknowledgement.
- Baseline / red evidence: Catalog tests first failed because `mcp catalog` and catalog-bound activation controls were absent.
- Change: No additional production change; record the current linear delivery identity.
- Verification: `git show --no-patch --format='%H %s' b650515` identifies `feat: add curated MCP capability catalog`; catalog and MCP relay tests pass.
- Delivery: Commit `b650515`; no push, deployment or merge performed.
- Next: Keep signed/public catalog provenance and external extension certification open.

## 2026-07-24 - Extension manifest conformance - active

- Milestone: `S4.4`
- Owner: `Codex`
- Scope: Define and validate a stable `gptadmin.mcp-extension/v1` manifest for third-party MCP adapters without executing untrusted entrypoints or bypassing Hub policy.
- Baseline / red evidence: `tests/test_extension_sdk.py` first failed because no validator or extension contract existed.
- Intended verification: Reference fixture validation, malformed-manifest rejection, CLI validation smoke, completion matrix and full Python suite.
- Delivery: No commit yet; preserve the unrelated untracked remote-secret-ingress plan.
- Next: Complete the conformance validator/docs, then record the delivery identity and keep live third-party extension certification open.

## 2026-07-24 - Curated MCP capability catalog - active

- Milestone: `S4.2`
- Owner: `Codex`
- Scope: Add a bundled, attributed capability catalog and require explicit operator acknowledgement before catalog-bound MCP activation; preserve arbitrary private MCP entries without false provenance.
- Baseline / red evidence: `tests/test_mcp_catalog.py` first failed because no catalog command or catalog-bound install guard existed; `mcp add --install` proceeded directly to service activation.
- Intended verification: Catalog metadata tests, existing MCP relay setup tests, CLI compile, completion matrix and full Python suite.
- Delivery: No commit yet; preserve the unrelated untracked remote-secret-ingress plan.
- Next: Complete the catalog docs/tests, verify no credential leakage, then replace this active entry with delivery identity and keep signed/public catalog attestation open.

## 2026-07-24 - Admin passkey enrollment UI - completed

- Milestone: `S2.1a`
- Owner: `Codex`
- Scope: Expose authenticated WebAuthn/passkey enrollment in the production vanilla-JS admin SPA using the existing Hub ceremony endpoints.
- Baseline / red evidence: `test_admin_security_ui_offers_passkey_enrollment_without_raw_credentials` failed because the security view had no passkey controls or WebAuthn browser call.
- Change: Added a passkey enrollment button/result view and browser-native `navigator.credentials.create` flow with challenge/user/exclusion decoding and attestation encoding.
- Verification: `python3 -m pytest tests/test_admin_ui.py -q` -> 8 passed; `node --check public/admin/app.js` passes. Virtual-authenticator browser smoke remains the next gate.
- Delivery: Pending current linear integration commit; no push, deployment or merge.
- Next: Run browser-backed enrollment against a disposable Hub, then record the delivery identity.

## 2026-07-24 - S4.1 conformance delivery identity - completed

- Milestone: `S4.1`
- Owner: `Codex`
- Scope: Attach the immutable commit to the MCP integration control/conformance slice.
- Baseline / red evidence: The local `discover -> schema -> execute` regression exposed the nil-request execute panic before the fix.
- Change: No additional production change; record the delivery identity for the control contract, support inventory and request-scoped execute fix.
- Verification: `git show --no-patch --format='%H %s' 54a10f0` identifies `test: certify MCP integration control flow`; focused conformance and inventory tests pass.
- Delivery: Commit `54a10f0`; no push, deployment or merge performed.
- Next: Add catalog provenance and extension conformance, while retaining external adapter evidence as open.

## 2026-07-24 - MCP integration conformance and execute safety - completed

- Milestone: `S4.1` / `S2.2`
- Owner: `Codex`
- Scope: Certify the canonical local MCP `discover -> schema -> execute` flow and remove a request-context crash in `execute` dispatch.
- Baseline / red evidence: `TestMCPIntegrationDiscoverSchemaExecuteConformance` panicked in `Server.origin()` because `execute` reached `appsSDKCallMCP` with a nil request.
- Change: Added the conformance test and routed `execute` through the request-scoped Apps SDK executor; request-free internal origin fallback is now safe.
- Verification: Focused conformance test passes; full Hub test/race/vet and completion-matrix inclusion remain the integration gate.
- Delivery: Pending current linear integration commit; no push, deployment or merge.
- Next: Add the conformance test and support-version contract to the matrix, then run the full local acceptance suite.

## 2026-07-24 - Named profile instruction sets - completed

- Milestone: `S1.3a`
- Owner: `Codex`
- Scope: Make profile-selected instruction sets real at runtime: authenticated CRUD, persistence, ETag concurrency, profile validation, MCP initialize/resource selection and restart behavior.
- Baseline / red evidence: `TestNamedInstructionSetCRUDAndProfileInitialize` first failed with `404 page not found`; existing profile normalization rejected all non-default references and `agentMCPJSONRPC` always returned global startup instructions.
- Change: Added `instruction_sets_state.json` with atomic restrictive writes, named instruction-set collection/item endpoints, reference checks and delete protection; MCP Hub/Shell initialize and startup resource now use the bound profile snapshot.
- Verification: Focused named-profile tests pass; `go test ./internal/hub -count=1` passes after updating the obsolete default-only normalization assertion.
- Delivery: Pending current linear integration commit; no push, deployment or merge. Preserve the unrelated untracked remote-secret-ingress plan.
- Next: Run the full Go race/vet and completion matrix, then record the immutable delivery commit.

## 2026-07-24 - S1.1 plan status correction - completed

- Milestone: `S1.1`
- Owner: `Codex`
- Scope: Align the canonical plan with the implemented setup health gate and current from-scratch Docker evidence.
- Baseline / red evidence: `docs/PROJECT_PLAN.md` still labeled S1.1 `Planned` after the setup and installer acceptance work was completed.
- Change: Moved S1.1 to `In progress`; local setup/health/Tunnel proof exists, while native clean-host and public deployment evidence remain open.
- Verification: Current-head Docker installer/Tunnel run exited `0`; full Python and completion matrix gates are green.
- Delivery: Documentation correction on the current linear vertex; no push, deployment or merge.
- Next: Use native clean-host evidence to decide whether S1.1 can move to Complete.

## 2026-07-24 - Current-head repository acceptance - completed

- Milestone: `S0.1` / `S0.2` / `S1.1` / `S3.5`
- Owner: `Codex`
- Scope: Re-run the repository acceptance gates after setup health and installer vocabulary fixes on the single linear integration vertex.
- Baseline / red evidence: The current-head from-scratch Docker run exposed the installer quickstart credential-name regression; it was fixed before this final gate.
- Change: No additional product change; record the final acceptance evidence for the current linear tree.
- Verification: Full Python `188 passed, 2 skipped`; completion matrix `11 passed`; Go Hub, ShellMCP and ProxyRelay full tests plus race/vet passed; Hub Darwin arm64/amd64 cross-builds passed; Docker installer/Tunnel run at `497fac7` exited `0` with `ALL SHELLMCP E2E SCENARIOS PASSED`; `git diff --check` passed.
- Delivery: Current linear commits `ef31e3d`, `497fac7`, `8ce1c25`; no push, deployment or merge performed. Preserve `docs/superpowers/plans/2026-07-24-remote-secret-ingress.md` untouched.
- Next: Obtain the remaining external clean-host/native/CI/collector/physical-fallback evidence before closing the corresponding roadmap gates.

## 2026-07-24 - Installer quickstart delivery identity - completed

- Milestone: `S0.5` / `S1.1`
- Owner: `Codex`
- Scope: Attach the exact commit to the installer quickstart vocabulary fix.
- Baseline / red evidence: `trash/logs/setup-e2e-ef31e3d.txt` contained the legacy credential name before the fix.
- Change: No additional production change; record the immutable delivery identity for the corrected installer help.
- Verification: `git show --no-patch --format='%H %s' 497fac7` identifies `fix: hide legacy auth names from installer help`; current-head Docker installer log `trash/logs/setup-e2e-497fac7.txt` has exit code 0, the expected AdminPassword/OAuth wording and no `CTL_TOKEN`.
- Delivery: Commit `497fac7`; no push, deployment or merge performed.
- Next: Keep the quickstart vocabulary regression in the from-scratch installer gate.

## 2026-07-24 - Installer quickstart auth vocabulary - completed

- Milestone: `S0.5` / `S1.1`
- Owner: `Codex`
- Scope: Remove the legacy credential name from the shell installer completion help and protect the one-password product vocabulary with a regression.
- Baseline / red evidence: Current-head Docker installer log `trash/logs/setup-e2e-ef31e3d.txt` contains `gptadmin tokens # покажет ТОЛЬКО CTL_TOKEN (хаб)`.
- Change: Replaced the legacy credential wording with AdminPassword/OAuth migration-status language and added an installer quickstart regression.
- Verification: RED/green focused installer tests pass; the preceding current-head Docker run captured the offending output and remains the pre-fix evidence.
- Delivery: Pending the current linear integration commit; no push or deployment.
- Next: Add the focused installer contract to the completion matrix and rerun the from-scratch Docker acceptance after commit.

## 2026-07-24 - Setup health acceptance gate - completed

- Milestone: `S1.1`
- Owner: `Codex`
- Scope: Make non-interactive and interactive Hub setup fail closed when the newly started local Hub does not become healthy; add a regression contract without touching the unrelated untracked plan.
- Baseline / red evidence: `setup_interactive()` ignores a false `wait_local_hub_health()` result and can print `Готово` after a failed Hub bootstrap.
- Change: Added `_require_local_hub_health()` and wired setup through the same fail-closed health gate as update; a failed local Hub now aborts instead of printing a successful setup completion.
- Verification: `python3 -m pytest tests/test_setup_semantics.py tests/test_install_scripts.py -q` and `python3 -m py_compile cli.py` pass. Full Python and completion-matrix verification remains the next integration gate.
- Delivery: Pending the current linear integration commit; no push or deployment.
- Next: Run the full repository acceptance gate and preserve clean-host/native gaps separately.

## 2026-07-24 - Safe delivery status delivery identity - completed

- Milestone: `S3.5`
- Owner: `Codex`
- Scope: Attach the exact plan-status commit to the rollback worklog sequence.
- Baseline / red evidence: The plan status was updated after the rollback implementation entry had already been committed.
- Change: No production change; record the immutable documentation commit that moved S3.5 from Planned to In progress.
- Verification: `git show --no-patch --format='%H %s' d5b8152` identifies `docs: mark safe delivery in progress`.
- Delivery: Commit `d5b8152`; no push, deployment or merge performed.
- Next: Prove signed-artifact canary, client reconnection and rollback on a clean host.

## 2026-07-24 - Transactional rollback delivery identity - completed

- Milestone: `S3.5`
- Owner: `Codex`
- Scope: Attach the exact immutable commit to the transactional update rollback entry below.
- Baseline / red evidence: The rollback entry was written before its implementation commit was created.
- Change: No production change; record the current linear delivery identity.
- Verification: `git show --no-patch --format='%H %s' 05cba42` identifies `feat: make updates rollback-safe on health failure`.
- Delivery: Commit `05cba42`; no push, deployment or merge performed.
- Next: Prove clean-host, signed-artifact canary and actual client reconnection before closing S3.5.

## 2026-07-24 - Transactional update rollback contract - completed

- Milestone: `S3.5`
- Owner: `Codex`
- Scope: Make the existing CLI in-place update abort on failed Hub health and restore the pre-update runtime/configuration before restarting services.
- Baseline / red evidence: `tests/test_update_semantics.py` exposed that `cmd_update` ignored a false `wait_local_hub_health` result; no runtime snapshot existed for package replacement failures.
- Change: Added a private bounded runtime snapshot/restore transaction around `cmd_update`, restart of restored services after post-stop failure, and a hard health gate after Hub restart. Added focused rollback regression coverage and BUGS evidence.
- Verification: `python3 -m pytest tests/test_update_semantics.py -q` -> `10 passed`; `python3 -m py_compile cli.py` passes. Clean-host, real client reconnection and signed canary evidence remain separate.
- Delivery: Pending the current linear integration commit; no push, deployment or merge performed. Preserve the unrelated untracked remote-secret-ingress plan.
- Next: Add a successful/failed end-to-end update rehearsal using two verified local artifacts, then keep physical/CI rollout proof separate.

## 2026-07-24 - Real MCP-client handshake delivery identity - completed

- Milestone: `S1.3`
- Owner: `Codex`
- Scope: Attach the exact immutable commit to the real local MCP-client acceptance entry below.
- Baseline / red evidence: The acceptance entry was written before its implementation commit was created.
- Change: No production change; record the current linear delivery identity.
- Verification: `git show --no-patch --format='%H %s' 939797b` identifies `test: verify real MCP client handshakes`.
- Delivery: Commit `939797b`; no push, deployment or merge performed.
- Next: Keep ChatGPT/external Tunnel and fresh-host certification separate from this local proof.

## 2026-07-24 - Real local MCP-client handshake contract - completed

- Milestone: `S1.3`
- Owner: `Codex`
- Scope: Turn the local Codex/Claude Code/OpenCode canonical MCP-client check into an isolated automated acceptance test without touching user configuration.
- Baseline / red evidence: The first test run failed because Codex requires its isolated `CODEX_HOME` directory to exist; the harness was corrected and rerun.
- Change: Added `tests/test_real_mcp_clients.py`: it builds a disposable Hub, configures temporary client homes, verifies Codex registration, Claude Code and OpenCode health connections, and performs the readonly-safe `demo` MCP action. Added the check to `tests/fixtures/completion-matrix.json`.
- Verification: `python3 -m pytest tests/test_real_mcp_clients.py -q` -> `1 passed in 14.33s`; no user config, token or persistent runtime was changed.
- Delivery: Pending the current linear integration commit; no push, deployment or merge performed. Preserve the unrelated untracked remote-secret-ingress plan.
- Next: Add external/fresh-host and ChatGPT/OIDC evidence separately; this local client proof does not close S1.3 by itself.

## 2026-07-24 - OTLP telemetry delivery identity - completed

- Milestone: `S3.1`
- Owner: `Codex`
- Scope: Attach the immutable delivery identity to the OTLP telemetry entry above.
- Baseline / red evidence: The implementation entry was committed after its initial handoff text was written.
- Change: No production change; record the exact linear commit for the local exporter contract.
- Verification: `git show --no-patch --format='%H %s' 807ffe7` identifies `feat: add opt-in OTLP telemetry export`.
- Delivery: Commit `807ffe7`; no push, deployment or merge performed.
- Next: Keep real collector/backend and cross-service production evidence open.

## 2026-07-24 - Opt-in OTLP structured telemetry contract - completed

- Milestone: `S3.1`
- Owner: `Codex`
- Scope: Add a bounded opt-in OTLP/HTTP exporter over the existing Hub audit/trace correlation boundary; keep production collector/backend proof separate.
- Baseline / red evidence: `TestOTLPExporter` initially failed to compile because no exporter/config/flush contract existed. During verification a collector backpressure test deadlocked; the test was corrected to consume all audit records before asserting the policy record.
- Change: Added `GPTADMIN_OTLP_ENDPOINT`, HTTPS-only external endpoint validation (loopback HTTP allowed for development), a bounded asynchronous queue, allowlisted structured event fields, URL/control-character filtering and fail-open delivery. Added API/security documentation and completion-matrix coverage.
- Verification: Focused OTLP tests, full Hub tests, Hub race/vet and completion matrix `11 passed` are green; `git diff --check` passes. Export payload checks prove policy/tool/trace correlation without raw arguments, commands, credentials, URLs or file contents.
- Delivery: Pending the current linear integration commit; no push, deployment or merge performed. Preserve the unrelated untracked remote-secret-ingress plan.
- Next: Prove a real collector/backend and cross-service retention/access policy before marking S3.1 complete.

## 2026-07-24 - WebAuthn browser acceptance and ceremony hardening - completed

- Milestone: `S2.1a`
- Owner: `Codex`
- Scope: Complete the locked-down admin passkey browser flow and its same-origin/CSP boundaries on the single integration branch.
- Baseline / red evidence: The new browser contract first failed because login HTML had no ceremony controls; real Chromium then exposed two runtime defects: login endpoints were inaccessible without internal credentials, and the library's zero ceremony expiry was rejected immediately. CSP also initially blocked same-origin fetches.
- Change: Added the passkey browser ceremony to `/admin/login`, restricted pre-session begin/finish to same-origin browser requests or existing internal/admin credentials, allowed only same-origin API connections in the login CSP, and made zero expiry conditional while retaining one-time server-side ceremony state.
- Verification: Focused WebAuthn/policy tests pass; Chromium virtual-authenticator run completed registration, WebAuthn proof, `locked_down` transition, logout and passkey login to `/admin/`; full Hub, race/vet, Python, matrix, Darwin and Docker gates pass. No secret material was emitted.
- Delivery: Pending the current linear integration commit; no push, deployment or merge performed. Preserve the unrelated untracked remote-secret-ingress plan.
- Next: Obtain external OIDC, real MCP-client, physical-standby, OTEL-backend and CI-publication evidence before closing the remaining roadmap gates.

## 2026-07-24 - Worklog delivery hash correction - completed

- Milestone: `S2.1a` / `S2.2`
- Owner: `Codex`
- Scope: Correct the immutable commit reference for the prior policy/passkey entry without rewriting its historical content.
- Baseline / red evidence: The earlier entry retained pre-amend hash `069bdc4` after the feature commit was amended to `fdb2608`.
- Change: Record the corrected delivery identity here; the historical entry remains append-only.
- Verification: `git show --no-patch --format='%H %s' fdb2608` identifies the policy/passkey feature commit.
- Delivery: Documentation correction in the current linear integration work; no push or deployment.
- Next: Use exact commit IDs for subsequent handoffs.

## 2026-07-24 - Browser passkey delivery identity - completed

- Milestone: `S2.1a`
- Owner: `Codex`
- Scope: Attach the exact delivery identity to the browser passkey acceptance entry above.
- Baseline / red evidence: The implementation entry was committed after its initial handoff text was written.
- Change: No production change; record the immutable linear commit for the completed browser flow.
- Verification: `git show --no-patch --format='%H %s' 985cdc4` identifies `feat: complete browser passkey login flow`.
- Delivery: Commit `985cdc4`; no push, deployment or merge performed.
- Next: Keep external/native/CI gates open until their stated evidence exists.

## 2026-07-24 - Docker install and failover acceptance - completed

- Milestone: `S0.1/S3.4`
- Owner: `Codex`
- Scope: Re-run from-scratch installer/Tunnel scenarios and the Hub failover black-box suite on the current linear vertex.
- Baseline / red evidence: N/A; this was an acceptance rerun after the CLI release-verification change.
- Change: No product code changed; retained the runtime evidence as the gate for the existing install, FRP/Tunnel and failover contracts.
- Verification: `docker compose -f tests/e2e/docker/docker-compose.yml up --build --abort-on-container-exit --exit-code-from shellmcp-e2e` -> exit 0 with `ALL SHELLMCP E2E SCENARIOS PASSED`; `docker compose -f tests/e2e/failover/docker-compose.yml up --build --abort-on-container-exit --exit-code-from failover-e2e` -> exit 0 with `ALL FAILOVER BLACK-BOX SCENARIOS PASSED`, including rank 1/rank 2 promotion and MCP re-registration.
- Delivery: Evidence is local Docker runtime only; no public deployment or second physical fallback host. Preserve the unrelated untracked remote-secret-ingress plan.
- Next: Obtain native client/platform and physical-standby evidence before changing S0.1/S3.4 from In Progress.

## 2026-07-24 - Supply-chain release gates - completed

- Milestone: `S4.3`
- Owner: `Codex`
- Scope: Make normal CLI updates fail closed without release digest metadata; add CI provenance attestation, Go/npm vulnerability checks and operator response policy.
- Baseline / red evidence: `test_update_rejects_missing_manifest_metadata_when_required` failed because the verifier had no strict metadata contract; the new workflow contract failed because the release job had no attestation/vulnerability steps or write permissions.
- Change: Added strict artifact verification for normal updates, retained the explicit diagnostic bypass, added `govulncheck`/`npm audit` and `actions/attest-build-provenance@v2` before publication, and added `docs/SUPPLY_CHAIN.md` plus matrix coverage.
- Verification: Focused update tests -> `9 passed`; release/provenance/policy tests -> `5 passed`; full Python suite -> `181 passed, 2 skipped`; completion matrix -> `11 passed`; full CI workflow execution and public publication remain external evidence.
- Delivery: Commit `3628303` on the single linear branch; no runtime deployment. Preserve the unrelated untracked remote-secret-ingress plan.
- Next: Verify the workflow in GitHub Actions and record the attestation/vulnerability run before changing S4.3 status.

## 2026-07-24 - W3C traceparent relay propagation - completed

- Milestone: `S3.1`
- Owner: `Codex`
- Scope: Carry bounded W3C `traceparent` metadata from Hub HTTP requests through relay and ShellMCP queues, polls, callbacks and durable-result correlation without payloads.
- Baseline / red evidence: `TestTraceParentCrossesRelayQueue` failed because Hub returned no `traceparent` header and queued jobs did not carry the parent.
- Change: Added strict traceparent parsing/replacement, child-span response headers, relay/shell queue fields, ShellMCP `TaskResult` propagation, audit fields and regression coverage; documented the contract in `API_REFERENCE.md`.
- Verification: Hub/ShellMCP focused tests -> pass; all Go tests, race and vet for Hub/ShellMCP/ProxyRelay -> pass; Python suite -> `178 passed, 2 skipped`; completion/release/SLO tests -> `13 passed`; Darwin Hub and ShellMCP arm64/amd64 builds -> pass.
- Delivery: Pending commit on the single linear branch; no runtime deployment. Preserve the unrelated untracked remote-secret-ingress plan.
- Next: Finish the remaining S3.1 exporter/backend and structured-log integration before marking standard telemetry complete.

## 2026-07-24 - ShellMCP bounded metrics endpoint - completed

- Milestone: `S3.1`
- Owner: `Codex`
- Scope: Add authenticated ShellMCP `/metrics` with bounded build/job/transport/heartbeat/audit/storage state; never expose host identity, credentials or command payloads.
- Baseline / red evidence: `TestMetricsEndpointIsAuthenticatedBoundedAndSecretFree` failed with HTTP 404 because ShellMCP had no metrics route.
- Change: Added the authenticated endpoint and completion-matrix coverage.
- Verification: Focused ShellMCP metrics test -> pass; full ShellMCP/race/vet and completion matrix remain the integration gate.
- Delivery: Commit `8eb1333` on the single linear branch; no runtime deployment. Preserve the unrelated untracked remote-secret-ingress plan.
- Next: Keep S3.1 open until a real exporter/backend correlates all runtime metrics and traces.

## 2026-07-24 - Hub bounded metrics endpoint - completed

- Milestone: `S3.1`
- Owner: `Codex`
- Scope: Add the Hub `/metrics` operational probe with aggregate counts only; keep it independent from admin auth and exclude secrets, MCP arguments and file contents.
- Baseline / red evidence: `TestHubMetricsEndpointIsBoundedAndSecretFree` failed with HTTP 404 because the Hub had no metrics route.
- Change: Added bounded agent/queue/job/audit/telemetry/preset aggregates, API documentation and completion-matrix coverage.
- Verification: Focused Hub metrics test -> pass; full Hub/race/vet and completion matrix are the integration gate for this slice.
- Delivery: Commit `d2c7655` on the single linear branch; no runtime deployment. Preserve the unrelated untracked remote-secret-ingress plan.
- Next: Complete standard telemetry export/correlation across remaining services before marking S3.1 complete.

## 2026-07-24 - HTTPS public-origin preset gate - completed

- Milestone: `S1.6`
- Owner: `Codex`
- Scope: Enforce secure external `PUBLIC_ORIGIN` when changing security presets; permit only HTTPS external origins or loopback HTTP used by the local Hub↔Tunnel boundary.
- Baseline / red evidence: `TestSecurityPresetRejectsExternalHTTPOrigin` observed `private_access` accepted with `http://hub.example`.
- Change: Added origin parsing/userinfo rejection, HTTPS requirement for external origins, loopback exceptions, handler regression coverage and completion-matrix entry.
- Verification: `cd go-hub && go test ./...` -> pass; `go test -race ./...` -> pass; `go vet ./...` -> pass; `python3 -m pytest tests/test_completion_matrix.py -q` -> `11 passed`.
- Delivery: Commit `11db6ce` on the single linear branch; no runtime deployment. Preserve the unrelated untracked remote-secret-ingress plan.
- Next: S1.6 still needs real external identity verification and deployment-level TLS proof.

## 2026-07-24 - Hub loopback bind default - completed

- Milestone: `S1.6`
- Owner: `Codex`
- Scope: Close the Hub direct-listener exposure found in the installer/Go environment contract while preserving explicit HAOS/failover host overrides.
- Baseline / red evidence: `TestFromEnvDefaultsHubToLoopbackAndHonorsHubBind` failed because unset host produced `:9001` and `HUB_BIND` was ignored.
- Change: Go Hub now falls back through `HUB_BIND` and fails closed to `127.0.0.1`; configuration documentation and completion matrix cover the contract.
- Verification: `cd go-hub && go test ./...` -> pass; `go test -race ./...` -> pass; `go vet ./...` -> pass; `python3 -m pytest tests/test_completion_matrix.py -q` -> `11 passed`.
- Delivery: Commit `d83c407` on the single linear branch; no runtime deployment. Preserve the unrelated untracked remote-secret-ingress plan.
- Next: S1.6 still needs HTTPS enforcement and external identity verification; this closes only the direct-listener portion.

## 2026-07-24 - Hub auth failure rate limiting - completed

- Milestone: `S1.6`
- Owner: `Codex`
- Scope: Add bounded per-client authentication-failure limiting to Hub admin/MCP/control auth paths; preserve successful auth and existing credential contracts.
- Baseline / red evidence: `TestAuthFailuresAreRateLimitedPerClient` failed to compile because `Config` had no rate-limit policy and no auth-failure limiter.
- Change: Added configurable `GPTADMIN_AUTH_RATE_LIMIT` (default 60 failed attempts/client/minute), bounded client-window storage, `Retry-After` 429 responses and coverage for admin/control/MCP authentication failures. Documented the setting without exposing internal credentials in normal UI vocabulary.
- Verification: `cd go-hub && go test ./...` -> pass; `go test -race ./...` -> pass; `go vet ./...` -> pass; `python3 -m pytest tests/test_completion_matrix.py -q` -> `11 passed`.
- Delivery: Commit `cb190d6` on the single linear branch; no runtime deployment. Preserve the unrelated untracked remote-secret-ingress plan.
- Next: Keep HTTPS/no-public-port and external verification as separate deployment/identity gates; do not claim S1.6 complete from rate limiting alone.

## 2026-07-24 - ProxyRelay bounded metrics - completed

- Milestone: `S3.1`
- Owner: `Codex`
- Scope: Add a secret-free `/metrics` JSON surface and bounded counters for ProxyRelay authentication, active sessions, pairs, resets and queue high-water mark; preserve the existing WebSocket/ticket protocol.
- Baseline / red evidence: `TestMetricsEndpointExposesBoundedRelayCounters` failed with HTTP 404 because ProxyRelay had no metrics endpoint.
- Change: Added secret-free JSON `/metrics` with active sessions, authenticated peers, pairs, resets and queue high-water counters; counters are atomic and do not expose tickets, targets or payloads. Added runtime counter assertions and completion-matrix coverage.
- Verification: `cd go-proxyrelay && go test ./...` -> pass; `go test -race ./...` -> pass; `go vet ./...` -> pass; `python3 -m pytest tests/test_completion_matrix.py -q` -> `11 passed`.
- Delivery: Commit `fd32658` on the single linear branch; no runtime deployment. Preserve the unrelated untracked remote-secret-ingress plan.
- Next: Continue S3.1 with trace/metrics export across the remaining client and relay integration boundary; do not mark OpenTelemetry complete from this local metrics surface.

## 2026-07-24 - Safe request trace correlation - completed

- Milestone: `S3.1`
- Owner: `Codex`
- Scope: Add bounded request correlation to Hub HTTP/MCP policy, queued relay jobs and durable result audit; do not log payloads or credentials.
- Baseline / red evidence: `TestRequestTraceIDIsReturnedAndCorrelatesMCPAudit` failed because the Hub returned no request ID and policy audit had no correlation field.
- Change: Added `X-Request-ID` generation/sanitization middleware, trace context, trace-safe policy/hub/job audit fields, relay/shell job retention and response propagation, plus queued enqueue/result regression coverage. ShellMCP queue contracts now carry the trace into `TaskResult` and bounded audit events.
- Verification: `cd go-hub && go test ./internal/hub -run 'TestRequestTraceID' -count=1` -> pass; `cd go-shellmcp && go test ./internal/server -run 'TestQueueExecutesGenericMCPToolAndPostsResult' -count=1` -> pass; full Hub/race/vet and Python acceptance remain required after this slice.
- Delivery: Commits `ceb036e` and `46ce0f2` on the single linear branch; no runtime deployment. Preserve the unrelated untracked remote-secret-ingress plan.
- Next: Complete the S3.1 OpenTelemetry/metrics/logging span across ShellMCP and ProxyRelay before marking this milestone complete.

## 2026-07-24 - Release provenance workflow contract - completed

- Milestone: `S0.3`
- Owner: `Codex`
- Scope: Protect the existing release manifest/SBOM/digest and installer-link gates in `.github/workflows/build-and-sync.yml` with a repository test; no release or public-repository mutation.
- Baseline / red evidence: The workflow gates existed, but no test failed if a future edit removed them.
- Change: Added `tests/test_release_workflow_contract.py`, asserting manifest/SBOM verification and installer-link checks occur before public release publication.
- Verification: `python3 -m pytest tests/test_release_workflow_contract.py tests/test_completion_matrix.py -q` -> `12 passed`; the full Python suite and release-manifest/SBOM tests also passed in the same integration ladder.
- Delivery: Local workflow contract and plan status are committed in the current linear worktree; no GitHub run or release publication was triggered. Preserve the unrelated untracked remote-secret-ingress plan.
- Next: Treat actual GitHub CI provenance/public release as an external proof gate; do not claim it from the local YAML test alone.

## 2026-07-24 - SLO and alert runbook - completed

- Milestone: `S3.2`
- Owner: `Codex`
- Scope: Add the operator-facing SLO/error-budget contract and alert recovery runbook in `/home/roomhacker/gptadmin/docs/SLO_ALERTS.md`, with a documentation acceptance test; no runtime or deployment changes.
- Baseline / red evidence: `tests/test_slo_docs.py` failed because the canonical SLO/runbook document did not exist.
- Change: Added `docs/SLO_ALERTS.md` with measurable availability/readiness/MCP/job objectives, error-budget policy, machine-readable probe sources, and owner/symptom/diagnosis/recovery steps for Hub, auth/policy, relay/jobs and backup/failover incidents. Added the runbook to the executable completion matrix.
- Verification: `python3 -m pytest tests/test_slo_docs.py -q` -> `1 passed` after the RED test; full matrix verification remains part of the integration ladder.
- Delivery: Documentation and test changes are uncommitted in the current linear worktree; no runtime or deployment change. Preserve the unrelated untracked remote-secret-ingress plan.
- Next: Keep external probe observations separate from the documented target; the next S3 gap is safe delivery/CI provenance rather than inventing current SLO compliance.

## 2026-07-24 - Platform completion gate and single integration vertex - completed

- Milestone: `S0.2` / `S0.3` / acceptance gates for proxy, endpoints, hooks, MCP, file sharing, profiles and security
- Owner: `Codex`
- Scope: `/home/roomhacker/gptadmin`; preserve pre-existing dirty webhook/docs work while building one automated completion matrix and closing only evidence-backed runtime gaps.
- Baseline / red evidence: Canonical `docs/PROJECT_PLAN.md` still lists multiple roadmap milestones as planned/in progress; the pre-fix webhook tree had no durable state, no route CRUD, the matrix was structural-only, and the Docker installer scenario exhausted stale input.
- Change: Added the executable `tests/test_completion_matrix.py` and fixture; completed webhook route CRUD, secret-free metadata, atomic `0600` config/state persistence, restart-safe jobs/idempotency and bounded callback retries; added OpenAPI parity, profile instruction-set fail-closed validation, readonly/file-backup/symlink regressions and policy-denial audit attribution; synchronized disposable installer E2E with the checked-in CLI and current service contracts.
- Verification: `python3 -m pytest tests/ --ignore=tests/e2e -q` -> `157 passed, 2 skipped`; `cd go-hub && go test -race ./...` -> pass; `cd go-shellmcp && go test -race ./...` -> pass; `cd go-proxyrelay && go test -race ./...` -> pass; all modules `go vet` -> pass; Go Hub and ShellMCP Darwin arm64 builds -> pass; Hub/ShellMCP black-box contracts -> `12 passed`; completion matrix -> `9 passed`; Docker failover -> 7 scenarios passed; Docker installer/tunnel -> user install, system/FRP and backend scenarios passed with exit code 0; public/security scan -> `3 passed`.
- Delivery: Final integration commit on `codex/haos-addon-public`; no production restart or external deploy performed. The unrelated pre-existing `docs/superpowers/plans/2026-07-24-remote-secret-ingress.md` remains uncommitted by design.
- Next: Merge the single final commit into `main` after review; external open bug entries still require elevated Windows cleanup and the owning HAOS deployment task.

## 2026-07-24 - Universal Webhook/Event Gateway - completed

- Milestone: `S4.1`
- Owner: `Codex`
- Scope: Add the Hub-owned universal webhook ingress and dispatcher. A configured route authenticates a JSON event, renders configured MCP/prompt/Shell actions, creates a Hub job, deduplicates repeated deliveries, and optionally posts the terminal result to a configured callback. Agent Herder remains an ordinary MCP target.
- Baseline / red evidence: `go test ./internal/hub -run TestWebhookGateway -count=1` initially failed because the webhook route/config/job contract did not exist.
- Change: Added `go-hub/internal/hub/webhook_gateway.go` and focused tests, registered `/webhooks/v1/{route}` and `/webhook-jobs/{job_id}`, loaded operator routes from `GPTADMIN_WEBHOOK_CONFIG_FILE`, and documented the universal MCP/prompt/Shell action contract.
- Acceptance: `go test -race ./...` passes; the focused tests prove token/HMAC auth, raw-body signatures and replay window, JSON template rendering, route-scoped idempotency, asynchronous Shell/MCP dispatch and callback delivery. Python suite passes `148`, skips `2`.
- Delivery: Local source and docs only; no commit, push, deploy, or runtime restart was performed. Pre-existing dirty `docs/BUGS.md`, historical `docs/WORKLOG.md` entries, and `docs/superpowers/plans/2026-07-24-remote-secret-ingress.md` were preserved.
- Known dirty files: Preserve the pre-existing `docs/BUGS.md`, `docs/WORKLOG.md` changes and `docs/superpowers/plans/2026-07-24-remote-secret-ingress.md`; do not merge secret-ingress work into this slice.
- Next: Add admin-managed route CRUD and durable webhook delivery state before enabling production webhook configuration.

## 2026-07-23 - Public HAOS standby migration and physical drill - completed

- Milestone: `S3.4`
- Owner: `Codex`
- Scope: Add the public Apps repository on HAOS, preserve Supervisor options and `/data`, migrate only the standby app to image `1.0.5`, verify Hub/fallback/credential boundaries, and run promotion plus signed reclaim before removing the old local add-on backup.
- Baseline / red evidence: HAOS host `a0d7b954-ssh` ran `local_gptadmin_hub_standby` image `1.0.4`; public image digest and repository commit are recorded in the handed-off entry above. The immutable runtime evidence is `trash/logs/haos-public-drill-20260723-01.txt`.
- Change: Registered public repository slug `21623b8c`, installed public `gptadmin_hub_standby` `1.0.5`, preserved Supervisor options and `/data` through `/backup/gptadmin-hub-standby-migration-20260723T184032Z`, configured direct primary health plus fallback `9101`, preserved only the cross-node reclaim bridge key in protected data, and removed the stopped local add-on after the drill.
- Verification: Public app is `started` on repository `21623b8c`; Hub `:9001` and origin-form fallback `:9101` return `200`; credential-only scan reports `0` exact secret hits with options/internal secrets mode `600`; stopping only `gptadmin-hub.service` promoted public build `1.0.5`; starting Hub yielded `accepted=true` and `reclaimed_primary=true`; restoring primary FRP returned public build `128`. Primary Hub and FRP are active; old local container/image are absent; rollback data and image archives remain present.
- Delivery: Live HAOS migration and full physical drill completed. Source/docs changes are local in `/home/roomhacker/gptadmin`; no new commit or push was created in this operational slice.
- Next: Run the separate second-physical-fallback drill required by `S3.4`.

## 2026-07-23 - Public HAOS app installation and acceptance - handed-off

- Milestone: `S3.4`
- Owner: `Codex` → next operator/agent
- Scope: Migrate the existing HAOS standby from the local operator-built app to the public Home Assistant Apps repository, preserve `/data`, and perform fresh install plus physical failover acceptance.
- Baseline / evidence: Public repository `https://github.com/megamen32/gptadmin-haos-addons` is public at `cb8b4f2`; package `ghcr.io/megamen32/gptadmin-haos-hub-standby:1.0.5` is public and anonymous ARM64 pull passed with digest `sha256:aea6ee350052c90b2ace1de5cb66d2171dd3faeed1383b42df5f10229676de6d`. Live HAOS remains on the previously verified operator deployment `1.0.4`; no migration has been performed.
- Handoff plan: (1) add the public repository URL in HAOS Apps and confirm the `gptadmin_hub_standby` app resolves to `1.0.5`; (2) back up current Supervisor options and `/data` before touching the existing app; (3) configure only `AdminPassword`, public Hub URL, Tunnel/failover endpoints and rank/threshold options, letting the app generate internal credentials; (4) verify container state, Hub `:9001`, fallback proxy `:9101`, and generated runtime state without printing secrets; (5) stop only `gptadmin-hub.service` on `server-100`, verify primary FRP binding plus public `/healthz` and `/version` takeover, restore Hub, and verify signed reclaim; (6) keep the old local add-on source backup until the public app passes the drill.
- Release handoff: Merge draft PR `https://github.com/megamen32/gptadmin/pull/23` before relying on the main-branch release workflow. Configure `HAOS_ADDONS_REPO_TOKEN` only if automatic synchronization to the public Apps repository is desired; otherwise run the allowlist exporter and review its diff for each release.
- Verification already complete: `13` focused public/failover tests, Go Hub and Go ShellMCP tests, YAML/secret/path scan, ARM64 image build, public package visibility and anonymous pull.
- Delivery: Source branch `codex/haos-addon-public` and public Apps repository are pushed; existing credentials and live HAOS runtime were not changed.
- Next: Perform the guarded HAOS migration and fresh physical failover drill; do not delete the old local add-on backup until both promotion and reclaim pass.

## 2026-07-23 - HAOS Apps distribution repository - completed

- Milestone: `S3.4`
- Owner: `Codex`
- Scope: Sanitized Home Assistant Apps repository, public Supervisor options, internal credential generation, ARM64 image packaging and GitHub publication; preserve the existing live HAOS deployment.
- Baseline / red evidence: The current add-on source is an operator-generated context only: it has no root `repository.yaml`, no public `config.yaml`, and its Dockerfile requires generated binaries and instance state. Public export contract initially failed at test collection because the generator/exporter did not exist.
- Change: Added failing/green contract tests, secret-free failover bundle generation, persistent internal credential generation, public app metadata/templates, an allowlist exporter, a GHCR publication workflow and a draft integration PR.
- Verification: Focused public-distribution and failover tests pass `13`; Go Hub and Go ShellMCP tests pass; public repository YAML and secret/path scan pass; local ARM64 image build completes.
- Delivery: Public repository `https://github.com/megamen32/gptadmin-haos-addons` is published at commit `cb8b4f2`; source branch `codex/haos-addon-public` is pushed at commit `a0835ee`; draft PR `#23` is open. ARM64 image `ghcr.io/megamen32/gptadmin-haos-hub-standby:1.0.5` is public and anonymous pull passed with manifest digest `sha256:aea6ee350052c90b2ace1de5cb66d2171dd3faeed1383b42df5f10229676de6d`. No credentials were published and the live HAOS deployment is unchanged.
- Next: Add the public repository URL in Home Assistant Apps and configure the instance-specific Supervisor options; keep the physical failover drill as the runtime acceptance gate.

## 2026-07-23 - Physical Hub failover repair - completed

- Milestone: `S3.4`
- Owner: `Codex`
- Scope: Physical `server-100` Hub failure path, HAOS standby, watchdog, fallback proxy and FRP ingress; repository tests/docs and live verification.
- Baseline / red evidence: Runtime artifact `failover-runtime-20260723-01` shows `gptadmin-hub-watchdog.timer=bad`, no active physical failover proxy/tunnel path, HAOS `:9001` healthy but `:9101` refused, and Docker-only failover coverage is insufficient for the reported outage.
- Change: Added the HAOS systemd-free watchdog/proxy runtime, ARM64 FRP packaging and credential handoff, one valid FRP config/process per endpoint, secret-safe signed reclaim verification, watchdog pipe lifecycle fix, reclaim cooldown reset, primary FRP `BindsTo=gptadmin-hub.service`, deployment version/update flow, regression tests and failover documentation.
- Verification: `python3 -m pytest -q tests/test_haos_failover_runtime.py tests/test_failover_unit_contract.py` passed `7`; Go Hub tests passed during HAOS package build; Docker failover black-box scenarios passed; `frpc verify` passed for all three ARM64 failover configs. Live drill stopped only Hub on `server-100`, systemd stopped FRP, HAOS promoted and public `/healthz`/`/version` returned `200`; primary restart produced automatic `reclaimed_primary`, fallback FRP exited, and public returned primary build `128`. Final HAOS app is `1.0.4` started.
- Delivery: Live HAOS deployment completed; existing credentials preserved and not printed.
- Next: Run the separate second-physical-fallback drill required by S3.4.

## 2026-07-23 - Restore Android 4G LAN proxy reachability - completed

- Milestone: `S0.2` Android 4G proxy external-client acceptance
- Owner: Codex
- Scope: `android-4g-lan-proxy.service`, LAN firewall rules, Mac SOCKS5/HTTP
  smoke; no public Internet bind.
- Baseline / red evidence: The service listened on `192.168.2.100:3126` and
  passed a server-local proxy smoke, but the Mac TCP connection stalled; UFW
  had default-deny input and no TCP/3126 allow rule.
- Change: Added idempotent private-LAN UFW rules to the deployment script and
  applied the TCP/3126 rules on the live host without exposing a public bind.
- Verification: Service is active after restart; an external Windows LAN client
  returned the mobile egress through both SOCKS5 and HTTP CONNECT, while direct
  egress differed. `python3 -m pytest -q tests/test_android_4g_lan_proxy.py`,
  `bash -n deploy/android-4g-lan-proxy.sh`, and `git diff --check` passed.
- Delivery: Live script hash matches the repository source; firewall rules and
  service state are active on the target host. Commit `11e4857` on `main`.
- Next: Mac retries `socks5h://<lan-host>:<port>` or HTTP CONNECT.

## 2026-07-23 - Windows ShellMCP polling remediation and proactive bug register - handed-off

- Milestone: `S0.3` Windows runtime acceptance and operational hygiene
- Owner: Codex
- Scope: BeyondInfinity Windows polling agent; `AGENTS.md`, `CLAUDE.md`,
  `docs/BUGS.md`; no credential rotation.
- Baseline / red evidence: The exact runtime artifact
  `C:\ProgramData\gptadmin\rootd-25900.log` records HTTP 401 responses for
  queue/heartbeat and stale temporary certifi-path failures.
- Change: Added the project bug register and proactive remediation rule; added
  a Windows installer fallback for standard-user Startup launches; replaced
  the active BeyondInfinity user runtime with the current Go ShellMCP binary
  while preserving its existing identity and Hub credential.
- Verification: Authenticated queue probe returned HTTP 200 with `{}`; the
  active Windows log records current Go polling mode with no new 401, certifi,
  or unauthorized entries. Focused Python tests passed 11 tests and Go server
  tests passed.
- Delivery: Commits `e0860fc`, `b4ac1af`, and `2180a8e` on `main`. The old privileged legacy task remains
  inaccessible to the SSH user and needs one elevated cleanup action.
- Next: From an elevated Windows session, disable/remove the old `gptadmin-rootd`
  task and run one post-restart check; then close the remaining legacy-runtime
  entry in `docs/BUGS.md`.

## 2026-07-22 - Windows Network Tunnel packaging - completed

- Milestone: `S0.2` Network Tunnel platform coverage
- Owner: Codex
- Scope: Windows amd64 relay, ticket issuer, connector and agent binaries.
- Baseline / red evidence: The Network Tunnel archive contained only Linux and
  Android variants; Windows cross-builds were not packaged.
- Change: Added Windows `.exe` cross-builds to `tools/build.sh network-tunnel`
  and documented the archive layout.
- Verification: All four Windows targets cross-compiled successfully and the
  archive contains `network-tunnel/windows_amd64/*.exe`; Linux/Android
  packaging remains green.
- Delivery: Commit pending with this packaging change.
- Next: Windows runtime smoke requires a Windows host; no Windows host is
  available in this workspace.

## 2026-07-22 - Android DNS/offer contract correction - completed

- Milestone: `S0.2` Network Tunnel Android edge reliability
- Owner: Codex
- Scope: Stable offer JSON limits and explicit Android DNS fallback discovered
  during the live LTE acceptance.
- Change: Added snake_case JSON tags to edge `Limits` and `RunOfferWithDNS`,
  exposed as `-dns-server` with `1.1.1.1:53` default for delivered offers.
- Verification: Full Hub, relay and edge `go test`, race and vet suites passed;
  the corrected Android LTE run returned `HTTP/1.1 200 Connection Established`
  and target `HTTP/2 520` over `rmnet4`.
- Delivery: Commit `a7f00e0`.
- Next: No code blocker for the v1 vertical slice; live Hub/relay key
  configuration is an operational rollout step.

## 2026-07-22 - Android LTE Network Tunnel acceptance - completed

- Milestone: `S0.2` Network Tunnel Android/4G proof
- Owner: Codex
- Scope: Android arm64 edge binary, public relay on `vpn2`, local connector,
  hostname target and explicit cellular DNS fallback.
- Baseline / red evidence: Android rejected snake_case offer limits, then could
  not resolve the target or relay hostname through its localhost DNS stub.
- Change: Added stable JSON tags for edge limits and `-dns-server` resolution
  through an explicit UDP endpoint; rebuilt the Android package.
- Verification: Agent on `R5CR702SRFP` logged a cellular `rmnet4` source
  (`10.186.8.46`), local connector returned `HTTP/1.1 200 Connection
  Established`, and the public target returned `HTTP/2 520` through the
  Android path. Temporary device/relay processes and files were removed.
- Delivery: Code commit pending with the next focused test run; acceptance
  evidence is ephemeral and contains no credentials.
- Next: Commit the DNS/JSON fix and run the final all-module race/vet pass.

## 2026-07-22 - External relay acceptance - completed

- Milestone: `S0.2` Network Tunnel external data-plane proof
- Owner: Codex
- Scope: Ephemeral external relay/agent/client run through `vpn2`, using a
  free loopback connector port and a public Internet target.
- Baseline / red evidence: Existing `192.168.2.100:3126` was unreachable from
  `vpn2`; direct `vpn2` egress worked, confirming the old LAN proxy was not an
  external endpoint. A first test port was also rejected because it belonged
  to an unrelated ADB forward.
- Change: Re-ran on free `127.0.0.1:3137` with fresh one-time tickets. The
  connector reached the remote relay, the remote agent dialed `1.1.1.1:80`,
  and the target returned `HTTP/1.1 405 Method Not Allowed`; temporary remote
  processes, SSH forwarding and local ports were removed.
- Verification: External process-level path passed; repository Go test/race/
  vet suites remain green.
- Delivery: Acceptance evidence is ephemeral and contains no credentials.
- Next: Configure the shared relay key and revoke URL on the live Hub/relay,
  then run Android 4G and LAN-camera acceptance.

## 2026-07-22 - Relay revoke signaling - completed

- Milestone: `S0.2` Network Tunnel kill path
- Owner: Codex
- Scope: Dedicated Hub-to-relay signed revoke control request.
- Baseline / red evidence: Hub capability revoke changed Hub state but had no
  configured path to reset matching relay sessions.
- Change: Added optional relay revoke URL and asynchronous signed
  `/v1/control/revoke` delivery using the same dedicated relay key; ShellMCP
  control credentials and stream payloads remain out of band.
- Verification: Regression test captured the signed revoke request; Hub proxy
  focused/full tests and vet passed before delivery.
- Delivery: Commit `739f8fa`.
- Next: Run the external-network acceptance matrix with shared key and revoke
  URL configured; then perform one consolidated review.

## 2026-07-22 - Hub relay-ticket binding - completed

- Milestone: `S0.2` Network Tunnel grant/data-plane binding
- Owner: Codex
- Scope: Bind Hub-issued stream grants to the isolated relay ticket format with
  a dedicated key file, without reusing ShellMCP or OAuth credentials.
- Baseline / red evidence: Hub grants were opaque random strings and could not
  authenticate to the WSS relay, which accepts `gpr1` role-bound tickets.
- Change: When `GPTADMIN_NETWORK_PROXY_RELAY_KEY_FILE` is configured, Hub
  grants carry signed `gpr1` claims with role, target, stream, agent, profile,
  expiry and finite limits. Existing no-key controller tests retain opaque
  fallback behavior for compatibility.
- Verification: Relay-compatible ticket regression passed; full Hub tests and
  vet passed.
- Delivery: Commit `1f627ae`.
- Next: Wire relay revocation/control signaling and run external-network
  acceptance with the shared key configured on Hub and relay.

## 2026-07-22 - Semantic Network Access surface - completed

- Milestone: `S0.2` Network Tunnel control and packaging
- Owner: Codex with bounded Hub MCP implementation worker
- Scope: AI-friendly semantic aliases, release packaging target and final
  focused/full Hub verification.
- Baseline / red evidence: The Hub exposed only low-level `network_proxy_*`
  operations; semantic calls returned `unsupported hub tool` and the new
  binaries were not included in the build workflow.
- Change: Added `network_access_plan`, `network_access_enable`,
  `network_access_status`, and `network_access_disable` with explicit
  confirmation for enable/disable, preserved profile ACLs, and added the
  `network-tunnel` build target producing a separate Linux archive.
- Verification: Hub proxy/network focused tests, full Go Hub tests and vet
  passed; `tools/build.sh network-tunnel` produced the archive; relay and edge
  race suites remain green from the preceding vertical-slice delivery.
- Delivery: Commits `347b2dc` (semantic MCP surface) and `be61cea` (packaging).
- Next: Deploy the relay/agent binaries in a controlled external-network
  acceptance test, then run one consolidated review.

## 2026-07-22 - Network Tunnel runnable vertical slice - completed

- Milestone: `S0.2` isolated Network Tunnel data plane
- Owner: Codex with bounded relay-daemon implementation worker
- Scope: Public blackbox coverage, WSS relay daemon, controlled ticket issuer,
  edge offer runner, loopback HTTP CONNECT/SOCKS5 connector, and operator
  instructions. ShellMCP queues and heartbeat remain untouched.
- Baseline / red evidence: The relay core had tests but no runnable daemon or
  external edge connector path; no blackbox test covered a local proxy request.
- Change: Added `go-proxyrelay/cmd/proxyrelay` and `networkticket`, edge
  `networkproxy` and `networkproxy-agent` commands, bounded WebSocket stream
  adapter, local connector, and blackbox tests for relay and local CONNECT.
  The static ticket issuer is explicitly bring-up-only; dynamic Hub issuance
  and the AI-facing MCP surface remain a follow-up.
- Verification: `go test ./...`, `go vet ./...`, and `go build` passed in both
  `go-proxyrelay` and `go-shellmcp`; both `./blackbox` suites passed.
- Delivery: Pending commit in this working tree; no deployment performed.
- Next: Perform one consolidated review after the runnable slice is committed.

## 2026-07-22 - Edge proxy policy and dialer core - completed

- Milestone: `S0.2` Go-only ShellMCP runtime
- Owner: Codex
- Scope: Add the isolated `go-shellmcp/internal/networkproxy` policy and
  dialer core only; no queue/heartbeat, transport, offer-client, agent, CLI or
  deployment changes.
- Baseline / red evidence: `cd go-shellmcp && go test ./internal/networkproxy`
  failed because the desired policy, resolver and dialer contract was undefined.
- Change: Added scope-aware target policy, local resolution with all-answer
  validation and numeric-IP pinning, plus bounded TCP connections enforcing
  dial timeout, total bytes and connection lifetime. No ShellMCP loop,
  transport, current proxy PoC, CLI or deployment code changed.
- Verification: Focused RED then GREEN passed; `go test ./...`,
  `go test -race ./internal/networkproxy`, and `go vet ./...` all passed from
  `go-shellmcp`.
- Delivery: Commit `ad68358` (`Add edge proxy policy and dialer core`);
  detailed handoff is `.superpowers/sdd/task-4a-report.md`.
- Next: Add the separately scoped signed-offer client and agent activation
  flow that supplies this package's local Policy and Limits.

## 2026-07-20 - Retire legacy CLI token - completed

- Milestone: `S0.5`
- Owner: Codex
- Scope: Legacy `CTL_TOKEN` compatibility window, Go Hub enforcement, CLI/admin messaging and migration deadline.
- Baseline / red evidence: `CTL_TOKEN` remained accepted by Hub admin/MCP auth and was exposed by CLI token/doctor/setup messaging; no recorded cutoff date existed.
- Change: Set the fixed 2026-07-27 UTC cutoff; legacy bearer auth now emits deprecation metadata before the cutoff and is rejected afterwards. New setup/update flows do not generate or print it, CLI rotation is removed, Admin UI uses session/OAuth flows, and ShellMCP artifacts use the agent credential after the cutoff.
- Verification: RED tests covered pre/post-deadline Hub auth, ShellMCP artifact access, CLI output/rotation/setup and Admin UI controls. Python `130 passed, 2 skipped`; Go Hub and Go ShellMCP suites passed; local Hub rebuilt, restarted and reports `go-dev/worktree`.
- Delivery: Changes are included in the pushed `main` commit.
- Next: Complete the final legacy credential removal and migration audit after the 2026-07-27 deadline.

## 2026-07-20 - Persist ShellMCP auth across updates - completed

- Milestone: `S0.2`
- Owner: Codex with Sol security review
- Scope: Installer update auth preservation and legacy systemd ShellMCP overrides
- Baseline / red evidence: server-88 Go ShellMCP polls returned repeated `401 unauthorized`; effective service environment came from a legacy Go-primary drop-in instead of the canonical Hub environment.
- Change: Preserve legacy auth aliases during updates; remove all legacy Go-primary systemd overrides; make Linux/Android installers reuse stored credentials; add Hub `awaiting_approval` state and `approve_pending_server` MCP tool that gates queue delivery.
- Verification: RED tests reproduced both token rewrite and approval bypass. Python `126 passed, 2 skipped`; Go Hub `go test ./...` passed. Server-88 canonical env was repaired, legacy overrides removed, current Go ShellMCP rebuilt and restarted; Hub reports server-88 `online` with fresh `last_seen`. Hub MCP `tools/list` exposes `approve_pending_server`; `pending` call returns a structured empty list.
- Delivery: Live server-100 Hub and server-88 ShellMCP restarted with locally built binaries. The changes are pushed to `main`.
- Next: Run CI release verification for the pushed auth/approval lifecycle changes.

## 2026-07-19 - Remove Python MCP transport runtimes - handed-off

- Milestone: `S0.2` Go-only ShellMCP runtime
- Owner: Codex with bounded Mac, Windows and Android audit workers
- Scope: Replace Python generic MCP relay services with direct Go ShellMCP child
  ownership, remove production-capable Python ShellMCP launch surfaces, and
  restore live Linux, Mac, Windows and Android agents.
- Baseline / red evidence: Server-100 has eleven enabled per-agent systemd
  services whose effective command is the Python generic relay; ten are active
  and one failed. The generated Go supervisor registry launches the same Python
  relay instead of the configured child command. Android is stale with no
  ShellMCP process; both Windows identities are stale. Mac and server-100 are
  currently online through Go queue mode with no listener.
- Change: ShellMCP now writes a direct Go child registry, retires Linux and
  macOS orphan relay units, uses the canonical service name, persists the Mac
  ordinary-user identity, terminates child MCP processes during restart, and
  ships no Python relay in release archives. Stale duplicate CLI and public
  Windows installer artifacts were removed or synced.
- Verification: RED regressions covered nested Python relay ownership, legacy
  unit cleanup, Mac orphan plists/default user, Windows installer drift and
  long-poll listeners. Both Go suites passed; Python passed `123 passed, 2
  skipped`. Release builds for ShellMCP, Windows and Android contain no Python
  runtime. Server-100 exposed all seven enabled child schemas through Go;
  a restart with a live child completed in the same second; Mac and Android
  completed real Hub calls with no listener or Python relay.
- Delivery: Server-100, Mac and Android runtime migrations deployed and
  restarted. Release build 128 is prepared for the same commit and push.
- Next: Reinstall one reachable Windows host and record
  `online -> tools/list -> harmless call` acceptance.
- Blocker: Both registered Windows hosts remain stale and this environment has
  no SSH or WinRM route to either host; repository, artifact and CI evidence
  cannot substitute for a live Windows call.

## 2026-07-19 - Audit standalone ShellMCP host completion - completed

- Milestone: `S0.2` standalone ShellMCP contract slice
- Owner: Codex with bounded implementation audit and lifecycle review workers
- Scope: Prove and correct the Go ShellMCP standalone child-host contract for
  stdio, Streamable HTTP, legacy SSE, optional Hub polling, bounded storage and
  Mac/Linux deployment.
- Baseline / red evidence: Runtime tests reproduced duplicate stdio ownership,
  non-functional legacy SSE, non-persistent HTTP sessions, proprietary inbound
  GET/session behavior, queue cancellation delay, unbounded child stderr and
  separate disposable-data allowances. Review then reproduced hung-start and
  stale multi-waiter lifecycle races.
- Change: Child protocol sessions now have one cancellable, generation-checked
  owner; remote transports implement persistent/negotiated sessions and legacy
  SSE; inbound HTTP is stateless Streamable HTTP; queue cancellation is prompt;
  ShellMCP-owned spill, outbox, audit and backup files share one budget. Public
  service/HA templates and standalone documentation now use the installed Go
  binary and opt-in heartbeat policy.
- Verification: `go test ./...`, focused race suites, `go vet ./...`, five-way
  cross-build and Linux standalone black-box smoke passed. Final integrated
  gates passed both Go suites, Python `110 passed, 2 skipped`, React `17 passed`,
  lint, production build, secret scan and diff check. Linux, Darwin arm64 and
  HAOS arm64 runtimes use outbound long-poll with heartbeat disabled and no
  listener; Hub inventory reports their current identities online.
- Delivery: Integrated into the single repository commit for this worktree.
  Hub and local ShellMCP were rebuilt and restarted; the authorized Mac binary,
  CLI and canonical ShellMCP credential were updated over SSH and launchd was
  restarted; the HAOS add-on options, binary and container were rebuilt through
  Supervisor. All four services are active and queue authentication is healthy.
- Next: None.

## 2026-07-17 - Access profiles and enforced client bindings - completed

- Milestone: `S1.3a`
- Owner: Codex orchestrating bounded Luna workers
- Scope: Persist versioned access profiles, bind managed JWT/OAuth clients to a
  profile, filter their effective MCP tools and reject manually constructed
  forbidden calls. Preserve reference-only external workspaces and the current
  transitional authentication path.
- Baseline / red evidence: The delivered instruction set is versioned, but
  access remains a global `full`/`readonly` claim; no persisted profile binds a
  client to explicit targets/tools, and the admin Clients/Auth inventory does
  not provide the complete profile-aware control flow.
- Change: Added atomic versioned access-profile persistence, profile-aware JWT
  and OAuth client bindings, allowlisted target/tool enforcement at discovery
  and execution boundaries, redacted managed-client inventory, and complete
  React profile/client/auth flows. External workspaces remain reference-only.
- Verification: Red persistence, inventory, enforcement and UI tests preceded
  implementation. Final gates passed Hub and ShellMCP Go suites, Python `110
  passed, 2 skipped`, React `17 passed`, lint, production build, secret scan and
  diff check; Hub restart retained the profile contract and current agents
  re-registered successfully.
- Delivery: Integrated into the same single repository commit. The production
  Hub binary was rebuilt and `gptadmin-hub.service` restarted active. React
  remains source-only until its separately documented production parity gate;
  `public/admin/` remains the served admin UI.
- Next: Run the explicit React/public-admin parity gate before replacing the
  production admin bundle.

## 2026-07-17 - Admin profile instruction workspace - handed-off

- Milestone: `S1.3a`
- Owner: Codex orchestrating bounded Luna workers
- Scope: Deliver the first `S1.3a` vertical slice: versioned instructions,
  failover-safe persistence and the CI-gated React admin foundation. Define
  external machine/workspace references without copying private repositories
  into GPTAdmin or Cloud.
- Baseline / red evidence: Regression tests reproduced stale cross-Hub ETag
  writes, cacheable private responses, invalid inline overrides blocking edits,
  a Unix-epoch fallback timestamp and Windows Hub compile failures.
- Change: Added authenticated GET/PUT instruction-set CAS, OS-level file locks,
  cross-Hub read refresh, atomic persistence, nullable fallback timestamps and
  Windows-native locking. Added the React/TypeScript admin source and its exact
  API, stale-write, size, keyboard and build contracts. `public/admin/` remains
  production until the explicit parity gate. `ADMIN_PROFILES.md` defines
  reference-only external workspaces and the Cloud metadata boundary.
- Verification: `go test ./...` in Hub and ShellMCP; Hub race detector; Windows
  amd64 and Darwin arm64 compile; Python `106 passed, 2 skipped`; React 5 tests,
  lint, build and dependency audit; desktop/mobile browser smoke; `git diff
  --check`. Build, Sync, Release run `29587947262` passed all five jobs,
  including admin UI, Windows, macOS and failover E2E.
- Delivery: Commit `39d1931632ba546deb9c3727b67f852d70131af4` pushed to
  `origin/main`; CI run `29587947262` passed. No production admin replacement
  or runtime deployment was performed in this source-foundation slice.
- Next: Implement versioned `AccessProfile`, client binding and target/tool
  policy enforcement with black-box allow/deny tests, then pass the UI parity
  gate before replacing `public/admin/`.

## 2026-07-17 - Make cold Hub contract startup deterministic - completed

- Milestone: `S0.2`
- Owner: Codex
- Scope: Remove first-run Go compilation time from the Hub HTTP contract readiness window in CI.
- Baseline / red evidence: CI run `29583276807` failed only `test_hub_contract_health_and_auth[go]`; the first `go run` never opened its port within 15 seconds, while all subsequent Hub contract tests passed after the Go cache warmed.
- Change: The default Go Hub contract command is now built once per test session with `-buildvcs=false`; every default black-box case starts that binary instead of compiling during a 15-second readiness window. Configured external `HUB_CONTRACT_COMMANDS` remain unchanged.
- Verification: Cold-cache `test_hub_contract_health_and_auth` passed (`1 passed`); Python `104 passed, 2 skipped`; `go test ./...` in `go-hub` and `go-shellmcp` passed.
- Delivery: Pending commit, push and CI.
- Next: None.

## 2026-07-17 - Convert Cloud to a normal private tree - completed

- Milestone: `S0.4`
- Owner: Codex
- Scope: Replace the `private/Cloud` linked worktree with ordinary `main`-tracked private Cloud materials, while preserving unrelated untracked agent state.
- Baseline / red evidence: The previous move retained a linked worktree, contradicting the intended single-tree layout and leaving a 245 MB duplicate checkout under `private/Cloud`.
- Change: Preserved the three private Cloud materials in `private/Cloud/`, removed the linked worktree and its 245 MB duplicate code tree, and restored its unrelated `.juggler/` state under the new directory. Added a narrow ignore rule for that state directory; the root user's `.juggler/` was untouched.
- Verification: `git worktree list --porcelain` no longer contains Cloud; `git status` reports the three intended Cloud paths as ordinary additions; `git check-ignore` matches only `private/Cloud/.juggler/`; `.gitpublic/ignore` excludes all of `private/`.
- Delivery: Pending private-repository commit and push.
- Next: Work on reviewed Cloud materials directly under `private/Cloud` in `main`.

## 2026-07-17 - Move private Cloud worktree - completed

- Milestone: `S0.4`
- Owner: Codex
- Scope: Move the existing private `gptadmin-hubhub` linked worktree under `private/Cloud` without merging its obsolete branch into `main` or exposing it in the public mirror.
- Baseline / red evidence: The private Cloud worktree lives beside the repository root under a legacy folder name, while `private/` is already the sanctioned mirror-excluded location for private materials.
- Change: Renamed and moved the linked worktree from `/home/roomhacker/gptadmin-hubhub` to `private/Cloud` using `git worktree move`. Its private branch, staged files and untracked Cloud document were preserved; no obsolete branch history was merged into `main`. Added a local Git exclusion so the nested worktree does not pollute `main` status.
- Verification: `git worktree list --porcelain` reports `private/Cloud` on `gptadmin-hubhub`; its Git common directory is the root private repository; its staged/untracked state remains present; root `git check-ignore` matches `private/Cloud`; `.gitpublic/ignore` excludes `private/`.
- Delivery: Local workspace organization only; no public release.
- Next: Use `private/Cloud` as the private Cloud workspace; merge only reviewed, current slices into `main`.

## 2026-07-17 - Consolidate active work on main - completed

- Milestone: `S0.4`
- Owner: Codex
- Scope: Merge the currently verified AI-client and private startup-instruction slice into `main`; classify remaining divergent branches so obsolete implementation history is not reintroduced.
- Baseline / red evidence: The default workspace is on a feature branch, while `main` contains release `127`; branch choice is ambiguous for subsequent agents.
- Change: Merged `feature/connect-mcp-additional-ai-clients` into `main`, preserving its private startup-instruction source and AI-client support. Classified all other divergent branches as historical, obsolete or backup work: merging them would reintroduce removed Python ShellMCP code or stale Hub rewrites. Made the root-daemon black-box harness deterministic in isolated worktrees by disabling irrelevant Go VCS stamping during its test-only build.
- Verification: `go test ./...` in `go-hub`; `go test ./...` in `go-shellmcp`; `python3 -m pytest tests/ --ignore=tests/e2e` (`101 passed, 5 skipped`).
- Delivery: Commits `61c9e23`, `63b6640` and the follow-up test-harness commit; push and CI pending.
- Next: Work from `main`; retain old branches as explicit history until a separate deletion decision.

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

## 2026-07-17 - Publish release 127 - completed

- Milestone: `S1.3`, `S4.2`
- Owner: Codex
- Scope: Publish the committed Hub/client artifacts containing compact
  `discover` output and explicit detail opt-in.
- Baseline: `main` was at build `126`; CI builds passed but no new release
  tag or platform artifacts had been published.
- Change: Bumped `VERSION` to `127`; auto-tag created `v127`, and the release
  workflow built and synchronized all platform artifacts.
- Verification: Build, Sync, Release run `29581385996` passed all jobs,
  including Linux/Ubuntu, macOS, Windows and Android artifact checks;
  failover-e2e also passed. Public release `v127` in
  `megamen32/gptadmin_opensource` is published with nine assets, including
  Android arm64, Linux amd64/arm64, macOS amd64/arm64, Windows CLI/Hub/
  ShellMCP and the combined package.
- Delivery: `v127` is published and the public mirror sync completed.
- Next: Verify one real auto-update on each installed platform; no code change
  is required for this release gate.

## 2026-07-17 - GPTAdmin Cloud startup instructions - completed

- Milestone: `S1.4`
- Owner: Codex
- Scope: Provide owner-managed startup instructions through Hub MCP without
  contaminating direct third-party MCP endpoints.
- Baseline / red evidence: No persistent owner instruction channel existed;
  direct and administrative MCP surfaces had no tested separation.
- Change: Added bounded UTF-8 startup instructions from the private config,
  MCP `initialize.instructions`, the
  `gptadmin://startup-instructions` resource, and CLI `instructions path/show/set-file`.
  Hub and Shell surfaces receive GPTAdmin guidance; third-party direct MCP
  surfaces proxy upstream initialize/resources unchanged. Private owner
  instructions remain under `private/instructions/` and are excluded from
  public mirrors.
- Verification: `go test ./...`; `python3 -m pytest tests/ --ignore=tests/e2e`
  (`104 passed, 2 skipped`); `git diff --check`.
- Delivery: Pending commit and CI.
- Next: Publish this feature slice only after review; do not include it in the
  already-running `v127` release.
## 2026-07-21 - Promote React admin with legacy operations bridge - completed

- Milestone: `S1.4`, `S2.2`
- Owner: Codex
- Scope: Make the React admin console the primary `/admin/` surface while
  retaining the complete operational/MCP console at `/admin/legacy/`.
- Baseline / red evidence: The effective `/opt/gptadmin/public/admin` bundle
  differed from the repository React artifact; the React UI did not expose the
  legacy overview, tools/resources manager, jobs/audit, update or failover
  views.
- Change: Added the authenticated `/admin/legacy/` static route, packaged the
  React bundle plus an explicit `admin-legacy` fallback into Hub packages,
  taught CLI updates to install both static trees atomically, and exposed a
  visible `Операции и MCP` link from the React sidebar. Added TDD coverage for
  the route and release/install contracts.
- Verification: React tests `17 passed`, lint and `/admin/` base build passed;
  Python tests `132 passed, 2 skipped`; Go Hub tests `go test ./...` passed;
  browser smoke after admin login showed React instructions/profiles and the
  legacy tools/resources/MCP manager/jobs/audit/failover surface. Hub service
  is active. Deployment backup: `/opt/gptadmin/backups/admin-ui/20260721T050252Z`
  (Hub/static) and `/opt/gptadmin/backups/admin-ui/20260721T050913Z`
  (post-link static bundle).
- Security: The password used for browser smoke was accidentally included in
  tool output. At the user's direction the original `ADMIN_PASSWORD` was
  restored immediately; no replacement value was retained or committed.
- Delivery: Source changes are committed locally; the live static switch and
  release-ldflags Hub binary are active and reversible from the listed
  backups.
- Next: Push the integrated commit and keep the existing admin password stable.

## 2026-07-24 - Public product-auth documentation boundary - completed

- Milestone: `S0.5`, `S1.1`, `S1.3`
- Owner: Codex
- Scope: Public README and onboarding/integration docs; no runtime credential names or deployment secrets changed.
- Baseline / red evidence: New `tests/test_product_auth_language.py` failed for README, Getting Started, Hub, Integrations, ShellMCP and FAQ because normal copy exposed internal credentials.
- Change: Replaced token-copy and bridge-key instructions with Hub URL, AdminPassword, OAuth and managed device-connection flows.
- Verification: `pytest -q tests/test_product_auth_language.py` passes `6 passed`; targeted forbidden-name scan is empty.
- Delivery: Uncommitted local documentation/test slice on the single linear branch; no deployment or push.
- Next: Run the documentation-boundary tests with the full suite and commit with the next integration vertex.

## 2026-07-24 - Request-scoped MCP and file-boundary security - completed

- Milestone: `S0.4`, `S1.6`, `S3.1`
- Owner: Codex
- Scope: Hub Apps SDK/relay policy dispatch, relay job ownership, webhook shell templating, and ShellMCP file/inspect boundaries.
- Baseline / red evidence: Terra-assisted audit produced RED regressions for forbidden Apps SDK inspect, forbidden relay schema, cross-agent/duplicate relay results, shell metacharacter interpolation, and symlink reads.
- Change: Request-scoped facade authorization now covers Apps SDK inspect and relay schema; relay results are owner-bound and single-assignment; webhook event values move to job environment variables; `/file` and read-only inspect reject symlink paths.
- Verification: Hub focused security/webhook tests pass; ShellMCP inspect/server focused symlink tests pass; `git diff --check` passes.
- Delivery: Local uncommitted integration slice on `codex/haos-addon-public`; no deploy or push.
- Next: Run the complete Go/Python/release gates and retain the external-host proof gaps separately.

## 2026-07-24 - Readonly schema regression correction - completed

- Milestone: `S0.5`, `S1.6`
- Owner: Codex
- Scope: Request-scoped Apps SDK schema dispatch for readonly access claims.
- Baseline / red evidence: `TestReadonlyAppsSDKSchemaCannotReachRemoteTargets` initially observed a remote relay job from a readonly JWT after the request-scoped branch was added.
- Change: Restored the fail-closed readonly filter so only `hub` and `shell:*` schema targets are exposed.
- Verification: `cd go-hub && go test ./internal/hub -run '^TestReadonlyAppsSDKSchemaCannotReachRemoteTargets$' -count=1` passes.
- Delivery: Same local integration slice; no deploy or push.
- Next: Include the regression in the final Hub security and race gates.

## 2026-07-24 - Final-head clean-clone acceptance - completed

- Milestone: `S0.2`, `S3.3`, `S3.4`
- Owner: Codex
- Scope: Re-run source acceptance from a fresh clone at commit `89d9234dbd98633f7315dd15ed08801796e33921`.
- Baseline / red evidence: N/A; this is post-commit reproducibility evidence.
- Change: No runtime change; verified the committed tree without relying on the original working directory.
- Verification: Clean clone `/tmp/gptadmin-clean-final-JiNqbi` passed `go test ./...` in Hub, ShellMCP and ProxyRelay, `bash tests/e2e/failover/run.sh` with all scenarios passed, and focused Python integration/security tests `15 passed`.
- Delivery: Local evidence only; no push, deploy or external-host claim.
- Next: Obtain the remaining real macOS/Windows/Android, public client, tagged CI/provenance, collector and physical-fallback evidence.

## 2026-07-24 - Remote secret ingress - completed

- Milestone: `S0.5`
- Owner: Codex
- Scope: Hub-owned encrypted one-time browser secret entry, MCP request/status
  tools, server-side `secret_env` injection, redaction, documentation and
  executable completion-matrix coverage.
- Baseline / red evidence: New exact Apps SDK capability test first rejected
  the two advertised secret tools; a new persistence regression first found
  that pre-existing state/record files were not restored to mode `0600`.
- Change: Added AES-256-GCM records, hashed one-time owner-bound tokens,
  expiry/single-use state, fail-closed recovery validation, secure browser
  ingress headers/form handling, full-access-only MCP tools, shell job
  resolution and response redaction. Added focused Go/Python tests and
  configuration/API/security docs; malformed CSP was recorded and fixed in
  `docs/BUGS.md`.
- Verification: Focused secret gate `2 Go scenarios + 2 Python contract tests`;
  full Python `183 passed, 2 skipped`; Hub and ShellMCP `go test ./...` and
  `go test -race ./...` passed; Hub/ShellMCP `go vet ./...` passed; proxy-relay
  tests/vet passed; Hub Darwin arm64/amd64 builds passed; completion matrix
  `11 passed`; `git diff --check` passed.
- Delivery: Commit `5807739` (`feat: add secure remote secret ingress`);
  no push or deployment performed. The user-owned untracked plan
  `docs/superpowers/plans/2026-07-24-remote-secret-ingress.md` was preserved.
- Next: Exercise the flow on a real deployed Hub/client and record external
  runtime evidence before claiming the remaining platform gates complete.

## 2026-07-24 - Black-box and Docker acceptance re-run - completed

- Milestone: `S0.2` / `S3.4`
- Owner: Codex
- Scope: Re-run the requested proxy, endpoints, hooks, MCP forwarding,
  file-sharing, profiles, policy and failover surfaces from commit `75a25c0`.
- Baseline / red evidence: The first aggregate parallel Compose capture mixed
  expected failover connection-refused probes with the installer output, so it
  was discarded as insufficient evidence and both test projects were cleaned.
- Change: No production code change; reran the authoritative black-box suite
  and isolated Docker acceptance with explicit exit-code capture.
- Verification: `python3 -m pytest tests/test_hub_contract.py
  tests/test_shellmcp_contract.py tests/test_tunnels.py
  tests/test_mcp_relay_setup.py tests/test_android_4g_lan_proxy.py -q` ->
  `47 passed`; Docker installer/tunnel E2E -> exit `0`,
  `ALL SHELLMCP E2E SCENARIOS PASSED`; isolated Docker failover E2E -> exit
  `0`, all 7 scenarios and `ALL FAILOVER BLACK-BOX SCENARIOS PASSED`.
- Delivery: Evidence only; no deployment or push. Test containers and
  networks were removed after the run.
- Next: Obtain real Codex/Claude/ChatGPT client, physical fallback-host,
  CI-publication, WebAuthn/OIDC and OTEL-backend evidence; do not mark those
  roadmap gates complete from this local run.

## 2026-07-24 - Policy boundaries and passkey MFA - completed

- Milestone: `S2.1a` / `S2.2` / `S3.1`
- Owner: Codex
- Scope: Close the review-confirmed write-path policy bypasses and add the
  local WebAuthn/passkey MFA ceremony without changing the single integration
  branch.
- Baseline / red evidence: Review ID
  `019f93df-7148-7033-972a-07c17c13955d` identified bridge, webhook, bulk and
  admin resource queue bypasses plus runtime Actions OpenAPI drift. New RED
  tests failed with queued jobs and missing proxy-control paths. WebAuthn
  registration begin initially returned unauthorized because its route and
  ceremony did not exist.
- Change: Legacy bridge is read-only; webhook actions use explicit
  `approval_mode` automation profiles (default `ask_before_write`, optional
  bounded autonomous mode); bulk/resource routes use the central executor and
  target policy; runtime Actions OpenAPI now includes Network Tunnel paths.
  Added WebAuthn registration/login begin/finish ceremonies, restrictive
  credential state, locked-down MFA recognition and short-lived proof-cookie
  integration with password login/reauth. Added dependency
  `github.com/go-webauthn/webauthn v0.15.0` while retaining Go 1.24.
- Verification: Focused boundary/passkey tests pass; full Hub `go test ./...`
  and `go test -race ./...` pass; Hub `go vet ./...` passes; ShellMCP race/vet
  and ProxyRelay test/vet pass; full Python `183 passed, 2 skipped`; completion
  matrix `11 passed`; `go mod verify` and Hub Darwin arm64/amd64 builds pass;
  `git diff --check` passes.
- Delivery: Commit `069bdc4` (`feat: enforce policy boundaries and add passkey
  MFA`); no push or deployment performed. User-owned untracked remote-secret
  plan preserved.
- Next: Verify WebAuthn with a real browser/authenticator and obtain the
  remaining OIDC, external client, OTEL backend, physical failover and canary
  release evidence.

## 2026-07-24 - Post-commit failover build compatibility - completed

- Milestone: `S3.4`
- Owner: Codex
- Scope: Verify the committed Hub dependency graph and failover runtime after
  adding WebAuthn, using the Go 1.24 Docker build image.
- Baseline / red evidence: The previous failover run predated the new
  WebAuthn dependency and did not prove container compatibility for it.
- Change: No production change; rebuilt and ran the isolated failover Compose
  project from the current integration vertex, then removed its container and
  network.
- Verification: `docker compose -f tests/e2e/failover/docker-compose.yml up
  --build --abort-on-container-exit --exit-code-from failover-e2e` -> exit `0`,
  all 7 scenarios and `ALL FAILOVER BLACK-BOX SCENARIOS PASSED`.
- Delivery: Evidence-only follow-up; no push or deployment. The user-owned
  untracked remote-secret plan remains preserved.
- Next: Replace Docker-only HA proof with the two physical fallback-host
  acceptance run required by `PROJECT_PLAN.md`.

## 2026-07-24 - Clean-host backup restore drill - completed

- Milestone: `S3.3`
- Owner: `Codex`
- Scope: Prove the documented backup workflow through the real CLI process on
  a fresh target directory.
- Baseline / red evidence: The existing backup tests covered Python helpers
  directly but did not execute `gptadmin backup create`, `verify` and `restore`
  as a clean-host sequence or assert ownership/output hygiene.
- Change: Added `test_cli_clean_host_restore_drill_preserves_integrity_and_current_owner`;
  it creates a fresh temporary config, runs all three CLI commands, checks
  bytes/modes/current-user ownership and rejects secret output.
- Verification: `python3 -m pytest tests/test_backup_restore.py -q` passed
  (`3 passed`); CLI backup help remains available.
- Delivery: Pending integration commit on `codex/haos-addon-public`; no deploy,
  push or merge. The unrelated remote-secret-ingress plan remains preserved.
- Next: Execute the same ownership/restart procedure on each real supported
  host when deployment access is explicitly in scope.

## 2026-07-24 - Doctor authenticated readiness probe - completed

- Milestone: `S1.2`
- Owner: `Codex`
- Scope: Close the doctor auth-observability gap without exposing machine
  credentials in human or JSON output.
- Baseline / red evidence: `test_doctor_probes_authenticated_hub_readiness_without_echoing_token`
  failed because doctor checked public `/healthz` only and emitted no
  authenticated readiness result.
- Change: When a configured machine credential exists, doctor probes the
  protected `/admin/api/overview`; it reports only `remote_auth: ok/failed`.
  With no machine credential it reports a warning rather than claiming auth
  was verified.
- Verification: `python3 -m pytest tests/test_doctor_json.py -q` passed
  (`3 passed`); real `python3 cli.py doctor --json` remained valid JSON and
  contained no secret material.
- Delivery: Pending integration commit on `codex/haos-addon-public`; no deploy,
  push or merge. The unrelated remote-secret-ingress plan remains preserved.
- Next: Prove active service state and Tunnel lifecycle on a clean supported
  host before changing S1.2 from In progress to Complete.

## 2026-07-24 - Cross-transport audit incident drill - completed

- Milestone: `S2.4`
- Owner: `Codex`
- Scope: Make one incident query recover both a direct MCP allow and a relay
  deny with actor, target, tool, policy decision/reason, digest and result
  reference, without raw arguments.
- Baseline / red evidence: `TestAuditIncidentDrillRecoversDecisionWithoutRawArguments`
  initially found only the relay deny; direct `/mcp` `demo` allow was absent
  from `tool_policy_decision` events.
- Change: Routed direct `/mcp` `tools/call` allow/deny outcomes through the
  existing digest-only `auditToolDecision` helper and added the incident drill
  to the security acceptance matrix.
- Verification: Incident drill and focused direct-MCP audit regressions pass;
  Hub full/race/vet pass; completion matrix `11 passed`.
- Delivery: Pending integration commit on `codex/haos-addon-public`; no deploy,
  push or merge. The unrelated remote-secret-ingress plan remains preserved.
- Next: Keep the full audit incident drill in the release gate and separately
  obtain external client/target-host evidence for the remaining proof gaps.

## 2026-07-24 - Secret-free client configuration manifest - completed

- Milestone: `S1.3`
- Owner: `Codex`
- Scope: Extend `/connect` and `/connect.json` with machine-readable setup
  descriptors for Codex, Claude-compatible, ChatGPT and custom MCP clients.
- Baseline / red evidence: The canonical page exposed the Hub URL and OAuth
  links but had no `client_configs` payload, so a client could not consume a
  single safe setup contract without parsing prose.
- Change: Added per-client `streamable_http`, `/mcp`, `oauth2_pkce`, resource,
  authorization-server and ChatGPT Action-schema fields; no bearer token,
  password or client secret is returned.
- Verification: Connection page focused Go test passes; public Hub contract
  tests pass (`4 passed`); existing connection matrix row remains green.
- Delivery: Pending integration commit on `codex/haos-addon-public`; no deploy,
  push or merge. The unrelated remote-secret-ingress plan remains preserved.
- Next: Implement/verify local client auto-configuration and real Codex,
  Claude-compatible and ChatGPT harmless-action golden paths separately.

## 2026-07-24 - Automatic local activation telemetry - completed

- Milestone: `S1.5`
- Owner: `Codex`
- Scope: Turn on opt-in funnel counters from real Hub paths without retaining
  request payloads, identities, addresses or command contents.
- Baseline / red evidence: `TestActivationTelemetryRecordsConnectionAndFirstToolWithoutPayload`
  showed that enabling telemetry left counters empty after `/connect` and a
  direct `/mcp` tool call; only manual event POSTs worked.
- Change: Added locked, bounded `recordActivationTelemetry` calls for
  `connection_page_viewed`, `first_tool` and policy `failure`; persistence is
  local and errors do not change the request response.
- Verification: Focused telemetry tests pass; telemetry race test passes; the
  acceptance matrix command now covers both automatic and manual persistence.
- Delivery: Pending integration commit on `codex/haos-addon-public`; no deploy,
  push or merge. The unrelated remote-secret-ingress plan remains preserved.
- Next: Keep remote client golden paths and standard cross-component traces
  separate; this milestone intentionally remains local-only.

## 2026-07-24 - Sensitive security re-authentication - completed

- Milestone: `S2.1a`
- Owner: `Codex`
- Scope: Add a short-lived, browser-bound re-authentication proof for
  security preset and exposure mutations; preserve TOTP/recovery semantics and
  do not expose passwords or MFA material.
- Baseline / pre-fix evidence: Existing security endpoints accepted a valid
  control bearer or admin cookie for preset/heartbeat changes without a fresh
  password/MFA proof after login.
- RED/GREEN evidence: `TestSensitiveSecurityMutationRequiresFreshAdminReauth`
  first failed because preset mutation returned 200 without proof, then passed
  after enforcement; it covers password reauth, missing MFA, fresh TOTP and
  locked-down mutation.
- Change: Added short-lived signed HttpOnly reauth cookie and
  `/admin/api/security/reauth`; preset, heartbeat and OAuth rotation now reject
  stale admin/control access. The admin UI invokes reauth before mutations.
- Verification: Focused security tests, typed endpoint compatibility tests,
  admin UI tests (`13 passed`) and JS syntax check pass.
- Write scope: `go-hub/internal/hub/security_settings.go`, targeted route
  wiring/UI/tests/docs only. Do not touch proxy, relay, or unrelated plans.
- Delivery: Pending integration commit on `codex/haos-addon-public`; no deploy,
  push or merge. The unrelated remote-secret-ingress plan remains preserved.
- Next: Keep passkeys/WebAuthn and OIDC/external verification as separate
  outstanding S2.1a gates; do not infer them from this TOTP reauth slice.

## 2026-07-24 - Doctor service runtime probe - completed

- Milestone: `S1.2`
- Owner: `Codex`
- Scope: Extend machine-readable doctor output from unit-file presence to
  platform service runtime state without leaking command output or secrets.
- Baseline / pre-fix evidence: `_doctor_report()` marked every existing unit
  `ok` with `unit installed` even when the service was inactive or failed.
- RED/GREEN evidence: The doctor regression first lacked
  `service_runtime:Hub`, then passed with mocked active and failed service
  states.
- Change: Added systemd/launchd/Windows-task runtime probes that return
  secret-free ok/error/warning states and count failed services as doctor
  issues; defined the missing `IS_WINDOWS` platform constant.
- Verification: Doctor tests `4 passed`; full Python `175 passed, 2 skipped`;
  Windows contract `1 passed`; completion matrix `11 passed`.
- Write scope: `cli.py`, `tests/test_doctor_json.py`, matrix/docs only. Preserve
  all Go/proxy/relay files and the unrelated secret-ingress plan.
- Delivery: Pending integration commit on `codex/haos-addon-public`; no deploy,
  push or merge. The unrelated remote-secret-ingress plan remains preserved.
- Next: Retain live host service/Tunnel checks as explicit deployment evidence;
  do not infer clean-host runtime health from this mocked/local gate.

## 2026-07-24 - Manifest-driven safe first action - completed

- Milestone: `S1.3` / `S1.4`
- Owner: `Codex`
- Scope: Make the canonical connection manifest consumable for a harmless first
  MCP action, while keeping actual Codex/Claude/ChatGPT runtime acceptance
  separate.
- Baseline / pre-fix evidence: `/connect.json` described the MCP endpoint and
  OAuth mechanism but did not declare a machine-readable first tool call; a
  generic client still had to consult prose to discover `demo`.
- RED/GREEN evidence: The external Hub contract test first failed with missing
  `client_configs.custom.first_action`, then consumed the declared endpoint and
  `demo` call without hard-coded protocol details.
- Change: Added a secret-free `first_action` descriptor to every client config;
  it declares MCP `tools/call` and the read-only `demo` tool.
- Verification: Focused first-action test passes; full Hub contract tests pass
  (`5 passed`).
- Write scope: `go-hub/internal/hub/connection_page.go`,
  `tests/test_hub_contract.py`, focused matrix/docs only. Preserve proxy,
  relay and unrelated plan files.
- Delivery: Pending integration commit on `codex/haos-addon-public`; no deploy,
  push or merge. The unrelated remote-secret-ingress plan remains preserved.
- Next: Verify real Codex, Claude-compatible and ChatGPT clients through the
  public Tunnel; this contract does not claim those live paths.

## 2026-07-24 - Safe demo connection flow - completed

- Milestone: `S1.4`
- Owner: `Codex`
- Scope: Finish the safe demo path from the canonical connection manifest to a
  real direct MCP harmless action.
- Baseline / evidence: The safe `demo` tool and readonly policy existed, but
  the connection manifest did not declare the first action; clients still
  needed prose to find it.
- Change: Added the manifest `first_action` descriptor and an external Hub
  contract test that consumes it and calls `demo` through `/mcp`.
- Verification: Focused first-action test and full Hub contract tests (`5
  passed`) pass; the existing readonly no-argument/credential-leak regression
  remains green.
- Delivery: Pending integration commit on `codex/haos-addon-public`; no deploy,
  push or merge. The unrelated remote-secret-ingress plan remains preserved.
- Next: Keep S1.3 real client/Tunnel acceptance separate; do not treat this
  generic contract as Codex/Claude/ChatGPT live proof.

## 2026-07-24 - Capability policy and autonomy exit gates - completed

- Milestones: `S2.2`, `S2.3`
- Owner: `Codex`
- Scope: Audit the policy/autonomy implementation against the explicit plan
  gates rather than treating individual feature commits as completion.
- Evidence: Access-profile black-box tests cover per-target/per-tool allow and
  deny behavior; network proxy tests cover policy denial attribution; approval
  tests cover canonical relay, pinned-server and legacy-agent aliases; bounded
  autonomous tests cover relay plus both aliases and raw-argument exclusion.
- Verification: These rows run through `tests/test_completion_matrix.py`; the
  latest matrix passed (`11 passed`) and Hub full/race/vet passed.
- Delivery: Pending integration commit on `codex/haos-addon-public`; no deploy,
  push or merge. The unrelated remote-secret-ingress plan remains preserved.
- Next: Keep S2.1 identity migration and S2.1a passkey/OIDC verification as
  separate open gates; do not conflate them with policy/autonomy completion.

## 2026-07-24 - Doctor readiness exit gate - completed

- Milestone: `S1.2`
- Owner: `Codex`
- Scope: Close the doctor milestone after adding local service runtime state
  and authenticated public Hub readiness evidence.
- Evidence: JSON report covers local version, unit presence/runtime,
  AdminPassword, Hub URL/port, public health (Tunnel path), authenticated
  overview, remote clock and env permissions; missing credentials/managers are
  explicit warnings/errors rather than silent success.
- Verification: Doctor tests `4 passed`; full Python `176 passed, 2 skipped`;
  Windows contract `1 passed`; real `gptadmin doctor --json` parses; matrix
  `11 passed`.
- Delivery: Pending integration commit on `codex/haos-addon-public`; no deploy,
  push or merge. The unrelated remote-secret-ingress plan remains preserved.
- Next: Keep fresh-host install and live physical Tunnel takeover as separate
  S1.1/S3.4 evidence; doctor does not claim either.

## 2026-07-24 - Remove internal credential names from auth pages - completed

- Milestone: `S2.1`
- Owner: `Codex`
- Scope: Sanitize normal `/admin/login` and `/authorize` copy while preserving
  the internal migration implementation and scoped JWT/OAuth behavior.
- Baseline / pre-fix evidence: Auth templates visibly referenced `CTL_TOKEN`;
  the existing test required that string in the authorize page.
- RED/GREEN evidence: The updated auth-page regression first failed on the
  login page's `CTL_TOKEN` hint, then passed after both templates changed to
  product-facing OAuth/Hub/scoped-JWT wording.
- Change: Removed internal credential names from `/admin/login` and
  `/authorize`; migration behavior remains unchanged behind the API.
- Verification: Focused Hub auth and admin UI tests pass; full Hub, contract and
  Python gates are the remaining handoff check.
- Write scope: `go-hub/internal/hub/server.go`, targeted auth tests,
  `docs/BUGS.md` and worklog only. Preserve proxy/relay and unrelated plan.
- Delivery: Pending integration commit on `codex/haos-addon-public`; no deploy,
  push or merge. The unrelated remote-secret plan remains preserved.
- Next: Keep internal migration names out of normal setup/status/UI surfaces.

## 2026-07-24 - One-password identity exit gate - completed

- Milestone: `S2.1`
- Owner: `Codex`
- Scope: Close the normal-flow identity hygiene gate after strict JWT claim
  validation, scoped connection tests and auth-page sanitization.
- Evidence: Wrong audience/resource, expired and incomplete scoped JWTs,
  admin-token forwarding and raw-token exclusion are covered by Hub tests;
  `/admin/login`, `/authorize`, `/connect(.json)` and production admin UI now
  use AdminPassword/OAuth/scoped-JWT vocabulary without internal key names.
- Verification: Focused auth-page test passes; Hub package and black-box auth
  contract pass; full Python and matrix gates are green from the current audit.
- Delivery: Pending integration commit on `codex/haos-addon-public`; no deploy,
  push or merge. The unrelated remote-secret plan remains preserved.
- Next: Keep legacy migration expiry and S2.1a passkeys/WebAuthn/OIDC as
  separate security follow-up gates.


## 2026-07-24 - Typed admin security controls and Apps SDK contract - completed

- Milestone: `S2.1` / `S1.4`
- Owner: `Codex`
- Scope: Remove browser-side shell/env mutation from the production admin UI;
  expose heartbeat, local activation telemetry, MFA, security presets and
  approval review through typed Hub endpoints; keep Apps SDK capability
  metadata aligned with the safe readonly demo.
- Baseline / red evidence: `tests/test_admin_ui.py` failed because the UI still
  listed internal environment keys and `setEnvVar` built a `shell_exec` command;
  the focused Hub regression returned 404 for the typed heartbeat endpoint;
  the full Hub suite then caught a stale Apps SDK count of 7 after `demo` was
  intentionally added.
- Change: Added `/admin/api/security/heartbeat`, replaced legacy UI mutation
  controls with typed preset/MFA/telemetry/approval flows, removed the raw
  restart fallback, and changed the Apps SDK test to assert the exact eight
  capability names including `demo`.
- Verification: Focused admin/security tests `13 passed`; JS syntax check
  passed; Hub focused typed-endpoint and Apps SDK tests passed; Hub full,
  race and vet passed; ShellMCP full, race and vet passed; acceptance/golden/
  doctor tests `15 passed`; full Python suite `172 passed, 2 skipped`.
- Delivery: Pending integration commit on `codex/haos-addon-public`; no deploy,
  push or merge performed. The unrelated remote-secret-ingress plan remains
  untracked and preserved.
- Next: Continue the outstanding fresh-host/native runtime and clean-host
  restore acceptance audit; do not mark the overall completion goal done yet.

## 2026-07-24 - Release provenance and golden-path evidence - completed

- Milestone: `S0.1` / `S0.3`
- Owner: Codex
- Scope: Canonical release manifest/checksum verification and executable
  supported-path evidence; preserve the single integration branch and the
  unrelated untracked remote-secret plan.
- Baseline / red evidence: `tools/build.sh` emitted per-component checksums
  only for selected artifacts, and CI had no assertion that every produced
  release artifact had one machine-readable provenance record.
- Change: Added `tools/verify_release_manifest.py`, canonical archive
  provenance (`schema`, build/version/commit/time, platform, architecture,
  size and SHA-256), atomic manifest generation/verification in `build.sh`,
  release-workflow verification and manifest upload, plus JSON `gptadmin
  doctor` output. Fixed the discovered ShellMCP spill-directory alias drift
  and recorded it in `docs/BUGS.md`.
- Verification: RED/green `tests/test_release_provenance.py` (2),
  `tests/test_doctor_json.py` (2), `bash -n tools/build.sh`, focused Go
  alias tests, ShellMCP contract `8 passed`, and completion matrix `9 passed`.
  A full release build/CI run remains intentionally pending because it bumps
  the tracked release version and requires the release runner/toolchain.
- Delivery: Integrated in the final single-vertex branch commit; no deploy was
  performed. The unrelated untracked remote-secret plan is preserved.
- Next: Add the explicit four-platform/client golden-path fixture and wire
  platform-native CI acceptance evidence before changing S0.1 status.

## 2026-07-24 - Supported golden-path matrix - completed

- Milestone: `S0.1`
- Owner: Codex
- Scope: Define executable Linux/macOS/Windows/Android and Codex/Claude/
  ChatGPT-style install/auth/first-tool/uninstall-rollback evidence without
  claiming local proof for platforms unavailable on this host.
- Baseline / red evidence: The completion matrix covered requested functional
  surfaces but had no explicit platform/client path contract or per-path
  install and rollback commands.
- Change: Added `tests/fixtures/golden-paths.json` and an executable test that
  covers Linux/macOS/Windows/Android with Codex/Claude/ChatGPT-style clients
  and install/auth/first-tool/uninstall-rollback commands. Added it to the
  completion matrix. The commands reuse real install, Hub, MCP and update/
  failover tests and are deduplicated per stage/platform.
- Verification: `python3 -m pytest tests/test_golden_paths.py -q` -> `2
  passed`; completion matrix -> `10 passed`. The Linux host executed all
  declared contract commands; native macOS/Windows/Android runtime proof
  remains delegated to their CI/physical lanes.
- Delivery: Integrated in the final single-vertex branch commit; no deploy was
  performed. The unrelated untracked remote-secret plan is preserved.
- Next: wire the golden-path fixture to platform-native CI result checks and
  only then promote S0.1 from In progress to Complete.

## 2026-07-24 - One-password security and autonomy contract - completed

- Milestone: `S0.5` / `S1.6` / `S2.1-S2.4`
- Owner: Codex
- Scope: Black-box authentication hygiene, progressive security presets,
  capability policy, approval/autonomy and operator audit evidence; preserve
  hidden internal credentials and the one integration branch.
- Baseline / red evidence: Existing tests covered individual auth/policy paths,
  but the completion matrix did not assert request-bound JWT claims and
  durable operator audit as one contract.
- Change: Added strict request-bound JWT validation for audience/resource,
  recognized scopes, subject, issued-at and key ID; added key ID to issued
  OAuth/managed tokens; added allow/deny audit events with actor/client/subject/
  JTI, profile, target, tool, policy reason, status, result reference and a
  raw-argument-free digest; persisted audit JSONL with restart loading and
  `0600` permissions.
- Verification: RED/green focused tests cover wrong audience through `/mcp`,
  expiry, missing/unknown claims, audience arrays, admin-token forwarding,
  allow/deny audit metadata and restart persistence. `cd go-hub && go test
  ./internal/hub -count=1` passes. Security presets/MFA and full platform
  native checks remain explicitly open.
- Delivery: Integrated in the final single-vertex branch commit; no deploy was
  performed. The unrelated untracked remote-secret plan is preserved.
- Next: implement the progressive security preset/MFA gate or record it as an
  explicit external product blocker before claiming S1.6 complete.

## 2026-07-24 - Supply-chain SBOM and installer digest gate - completed

- Milestone: `S4.3` / `S0.3`
- Owner: Codex
- Scope: Generate a deterministic SPDX-style SBOM from checked-in dependency
  manifests, bind it to the release manifest and make verification fail closed
  on artifact/SBOM drift; preserve one integration vertex.
- Baseline / red evidence: Release provenance covered archive hashes, but CI
  published no SBOM and CLI update could not consume the canonical manifest
  list or reject a mismatched package.
- Change: Added deterministic SPDX-2.3 SBOM generation from Python/Go/npm
  manifests; bound SBOM path/size/SHA-256 into `build/manifest.json`; added
  CI verification/upload and shipped-target installer-link verification; made
  CLI update read both legacy dict and canonical list manifests and reject
  downloaded size/digest mismatches.
- Verification: `tests/test_sbom.py`, `tests/test_release_provenance.py` and
  `tests/test_update_semantics.py` -> `11 passed`; installer-link verifier ->
  `1 passed`; `bash -n tools/build.sh` passed. Full release build and CI
  publication remain unrun because they mutate release version state and need
  the release runner.
- Delivery: Integrated in the final single-vertex branch commit; no deploy was
  performed. The unrelated untracked remote-secret plan is preserved.
- Next: run the full local suite and then the real CI/native platform lanes;
  only after those results promote S4.3/S0.3 to Complete.

## 2026-07-24 - Doctor remote readiness contract - completed

- Milestone: `S1.1` / `S1.2`
- Owner: Codex
- Scope: Extend machine-readable doctor output with version, Hub/Tunnel
  reachability, remote clock signal and env-file permission checks; keep normal
  output plain-language and never expose credentials.
- Baseline / red evidence: `gptadmin doctor --json` reported local units,
  AdminPassword, Hub URL and port, but not remote readiness, build identity,
  clock drift or config-file permissions.
- Change: Added bounded read-only `/healthz` probe with remote build and Date
  drift reporting, local VERSION identity and restrictive env-file mode check;
  failures are structured without response bodies or credentials. Added the
  doctor contract to the completion matrix.
- Verification: `tests/test_doctor_json.py` and the updated completion matrix
  pass (`13 passed` combined), including a fake Hub response and secret-free
  output. `python3 cli.py doctor --json` remains valid JSON on the local host.
- Delivery: Integrated in the final single-vertex branch commit; no deploy was
  performed. The unrelated untracked remote-secret plan is preserved.
- Next: add authenticated Tunnel/client connection checks and native CI proof
  before promoting S1.2 to Complete.

## 2026-07-24 - Ask-before-write approval boundary - completed

- Milestone: `S2.3` / `S2.4`
- Owner: Codex
- Scope: Add an explicit profile approval mode, in-process approval
  request metadata, admin approve/reject endpoints and one-time execution
  binding across the MCP relay boundary; never persist or return raw args.
- Baseline / red evidence: Existing `AccessProfile` has access mode and
  allowlists but no `ask-before-write`; full-access profiles can invoke
  write-capable tools immediately through `/mcp-relay/call`.
- Change: Added `approval_mode` normalization and persistence, a shared approval
  gate for relay, pinned-server action and legacy-agent aliases, sanitized
  admin list/get/approve/reject endpoints, five-minute expiry, profile/actor/
  target/tool/argument-digest binding and one-time consume. Approval metadata
  never stores or returns raw arguments; restart invalidates in-process requests.
- Verification: Focused red/green tests cover direct relay, pinned-server and
  legacy-agent aliases, admin approval, replay rejection and profile persistence;
  `cd go-hub && go test ./internal/hub -run 'TestAskBeforeWrite|TestManagedClientAccessProfile|TestAccessProfileRejectsUnsupportedInstructionSet' -count=1` passes.
  Full Hub suite, all-module race tests, vet, Python `169 passed, 2 skipped`,
  completion matrix `11 passed` and OpenAPI YAML parsing pass.
- Delivery: Pending integration commit on `codex/haos-addon-public`; no deploy
  or push. The unrelated untracked remote-secret plan remains preserved.
- Next: define and enforce bounded-autonomous quotas, then implement the
  remaining MFA/security-preset and connection-page gates.

## 2026-07-24 - Bounded-autonomous write budget - completed

- Milestone: `S2.3`
- Owner: `Codex`
- Scope: Enforce a bounded autonomous write budget consistently across the
  canonical relay, pinned-server action and legacy-agent MCP surfaces.
- Baseline / red evidence: `bounded_autonomous` was accepted as a profile mode
  but had no execution quota; an allowlisted actor could repeat write calls
  indefinitely while bypassing any per-window autonomy bound.
- Change: Added a shared in-memory five-minute actor/profile budget with a
  32-call write limit, sanitized limit response, audit denial and identical
  enforcement across relay, pinned-server and legacy-agent aliases.
- Verification: Hub full and race suites pass; ShellMCP and ProxyRelay race
  suites plus vet pass; Python `169 passed, 2 skipped`; completion matrix `11
  passed`; OpenAPI YAML parses with approval and quota schemas.
- Delivery: Pending integration commit on `codex/haos-addon-public`; no deploy
  or push. The unrelated untracked remote-secret plan remains preserved.
- Next: implement the remaining connection-page gate and complete passkey,
  recovery-code and external-verification support.

## 2026-07-24 - Progressive security preset and TOTP gate - completed

- Milestone: `S1.6` / `S2.1a`
- Owner: `Codex`
- Scope: Add persisted Working default, Private access and Locked down preset
  state with a fail-closed TOTP fallback for browser admin sessions.
- Baseline / red evidence: No runtime preset or MFA state existed; the plan
  required Locked down to fail closed rather than being a documentation-only
  choice.
- Change: Added restrictive `0600` security state, preset API, one-time
  TOTP enrollment response, code verification and locked admin-login gate.
- Verification: `cd go-hub && go test ./... && go test -race ./... && go vet
  ./...` passes; ShellMCP and ProxyRelay race/vet pass; Python `169 passed, 2
  skipped`; completion matrix `11 passed`; OpenAPI YAML parses. Focused test
  covers preset persistence, fail-closed locked-down selection, TOTP
  enrollment/verification, 0600 state permissions, re-enrollment rejection
  and MFA-required browser login.
- Delivery: Pending integration commit on `codex/haos-addon-public`; no deploy
  or push. The unrelated untracked remote-secret plan remains preserved.
- Next: implement passkey/recovery-code/external-verification support and the
  remaining connection-page gate; TOTP alone is not full MFA completion.

## 2026-07-24 - Canonical connection page - completed

- Milestone: `S1.3`
- Owner: `Codex`
- Scope: Expose one secret-free Hub connection page and JSON contract with
  canonical MCP/OAuth discovery links and named Codex, Claude-compatible,
  ChatGPT and custom-client choices.
- Baseline / red evidence: `/connect` and `/connect.json` were not registered;
  a new client had no single Hub-owned discovery surface to start from.
- Change: Added public no-store HTML/JSON page derived only from the request
  origin and configured resource, with no token or password material.
- Verification: Hub full/race/vet pass; focused connection test passes; public
  Hub contract and golden-path tests pass (`6 passed`); completion matrix
  passes (`11 passed`); `git diff --check` passes.
- Delivery: Pending integration commit on `codex/haos-addon-public`; no deploy
  or push. The unrelated untracked remote-secret plan remains preserved.
- Next: implement local client auto-configuration and connection telemetry;
  this page is the canonical discovery surface, not a claim that those flows
  are complete.

## 2026-07-24 - MFA recovery-code consumption - completed

- Milestone: `S2.1a`
- Owner: `Codex`
- Scope: Extend the TOTP fallback with one-time recovery codes, hashed at rest,
  and encrypt the internal TOTP secret before persistence.
- Baseline / red evidence: TOTP enrollment had no recovery-code contract and
  the internal TOTP secret was plaintext in the restrictive state file; losing
  the authenticator would leave the locked admin session with no tested
  recovery path.
- Change: Generate recovery codes once during enrollment, return them
  only in that explicit enrollment response, persist hashes only and consume a
  matching hash atomically. Encrypt the TOTP seed with AES-GCM derived from the
  existing AdminPassword/internal secret before writing the restrictive state
  file; legacy plaintext is accepted only for migration reads.
- Verification: Hub full/race/vet pass; ShellMCP and ProxyRelay race/vet pass;
  Python `169 passed, 2 skipped`; completion matrix `11 passed`; recovery
  enrollment, re-use rejection and locked login consumption are covered by
  `TestSecurityPresetAndTOTPFailClosedUntilEnrollment`.
- Delivery: Pending integration commit on `codex/haos-addon-public`; no deploy
  or push. The unrelated untracked remote-secret plan remains preserved.
- Next: implement passkey/OIDC external verification before claiming the full
  S2.1a exit gate.

## 2026-07-24 - Local activation telemetry - completed

- Milestone: `S1.5`
- Owner: `Codex`
- Scope: Add opt-in, local-only activation event counters without retaining
  command contents, URLs, tokens or user-agent data.
- Baseline / red evidence: No telemetry preference or event-summary endpoint
  existed; activation funnel failures could not be inspected as an aggregate.
- Change: `0600` persisted opt-in state, allowlisted event names,
  bounded counters and admin GET/PUT/POST endpoints.
- Verification: Hub full/race/vet pass; ShellMCP and ProxyRelay race/vet pass;
  Python `169 passed, 2 skipped`; completion matrix `11 passed`; focused test
  covers opt-in denial, payload exclusion and restart persistence.
- Delivery: Pending integration commit on `codex/haos-addon-public`; no deploy
  or push. The unrelated untracked remote-secret plan remains preserved.
- Next: wire only explicitly selected funnel events and add cross-process
  telemetry aggregation if the product needs it; no remote telemetry is sent.

## 2026-07-24 - Safe read-only demo capability - completed

- Milestone: `S1.4`
- Owner: `Codex`
- Scope: Add a built-in Hub MCP `demo` tool for a harmless connection check;
  enforce readonly authorization across global MCP and Apps surfaces.
- Baseline / red evidence: The safe-demo plan row was still `Planned`; readonly
  clients had discovery and inspection but no single no-shell/no-credential
  connection check.
- Change: Added `demo` tool advertisement, readonly policy allowlist and a
  real `/mcp` JSON-RPC regression proving no argument echo or secret exposure.
- Verification: Hub full/race/vet pass; ShellMCP and ProxyRelay race/vet pass;
  Python `169 passed, 2 skipped`; completion matrix `11 passed`; public Hub
  contract/golden tests `6 passed`; focused readonly `/mcp` test proves the
  safe response contains connection/build/access facts but neither argument
  input nor OAuth material.
- Delivery: Pending integration commit on `codex/haos-addon-public`; no deploy
  or push. Both untracked plan files remain preserved.
- Next: wire the demo into the connection-page client flows and keep the
  destructive-action boundary enforced through every alias.

## 2026-07-24 - Searchable operator audit trail - completed

- Milestone: `S2.4`
- Owner: `Codex`
- Scope: Make the existing durable audit surface searchable and bounded for
  incident review without changing the digest-only argument contract.
- Baseline / red evidence: `/admin/api/audit` returned the entire in-memory
  tail and exposed no filters, pagination metadata or query contract.
- Change: Exact name/actor/target filters, case-insensitive `q`, bounded
  limit/offset pagination and durable-jsonl response metadata.
- Verification: Hub full/race/vet pass; ShellMCP and ProxyRelay race/vet pass;
  Python `169 passed, 2 skipped`; completion matrix `11 passed`; public Hub
  contract/golden tests `6 passed`; focused test covers search, filters,
  pagination and digest-without-raw-arguments behavior.
- Delivery: Pending integration commit on `codex/haos-addon-public`; no deploy
  or push. Both untracked plan files remain preserved.
- Next: add retention/export controls only if the incident runbook requires
  them; current durable JSONL tail remains bounded and queryable.

## 2026-07-24 - Versioned configuration backup and restore - completed

- Milestone: `S3.3`
- Owner: `Codex`
- Scope: Add a manifest/digest-backed CLI backup, verifier and atomic restore
  drill for the GPTAdmin configuration directory.
- Baseline / red evidence: No `gptadmin backup` command or scripted restore
  contract existed; a clean-host recovery could not be automated from the repo.
- Change: `gptadmin.backup/v1` archive manifest, SHA-256 verification,
  restrictive modes and traversal/symlink-safe extraction into a new target.
- Verification: focused backup tests pass (`2 passed`); full Python suite is
  `171 passed, 2 skipped`; completion matrix `11 passed`; CLI help exposes all
  three backup commands; golden-path/doctor tests `4 passed`; diff check passes.
  The clean temporary round trip preserves bytes/modes and rejects traversal.
- Delivery: Pending integration commit on `codex/haos-addon-public`; no deploy
  or push. Both existing untracked plan files remain preserved.
- Next: run the restore drill on each supported host and verify service
  ownership/restart behavior; the CLI intentionally does not chown or restart.

## 2026-07-22 - Isolated Network Tunnel proxy relay - completed

- Milestone: `S2.2`
- Owner: Codex
- Scope: Create the separate `go-proxyrelay` core and ticket verifier without
  modifying Hub or ShellMCP transport, queues, jobs or service lifecycle.
- Baseline / red evidence: The relay regression suite first failed because the
  production relay and ticket packages were absent. A follow-up RED caught
  per-direction bandwidth doubling for one stream.
- Change: Added signed role-bound tickets, replay cache, bounded WSS pairing,
  FIN/RESET forwarding, capability revoke, per-agent/profile limits, byte,
  frame, queue, idle, lifetime and shared bandwidth limits, and metadata-only
  audit logging. The relay is an isolated module with no Hub/ShellMCP imports.
- Verification: `cd go-proxyrelay && go test ./internal/relay -count=1`,
  `go test ./...`, `go test -race ./...`, `go vet ./...` and the local TCP echo
  integration test passed.
- Delivery: Commit `4752e06` (`feat: add isolated bounded proxy relay`); no
  push, public listener deployment or edge-agent dialer in this slice.
- Next: Implement the edge proxy agent and enforce target dial timeout/ACL at
  the LAN side before adding service packaging.

## 2026-07-22 - Repair outbound ShellMCP polling and FRP origin - completed

- Milestone: `S0.2`
- Owner: Codex
- Scope: External Custom GPT relay, FRP origin migration, and ShellMCP agents on
  `roomhacker-server-100`, `roomhacker-server-88`, and `server-44`.
- Baseline / red evidence: Server-100 root ShellMCP rejected ordinary commands
  without `SHELLMCP_DEFAULT_USER`; server-44 ran an older listener build with
  `queue=false` and then returned queue `401 unauthorized`; live FRP installs
  could retain a private `HUB_PUBLIC_URL` after update. The new origin regression
  test failed before the code change.
- Change: `sync_oauth_origin_env()` now derives the canonical HTTPS origin from
  enabled FRP settings, preventing private-origin drift. Live server-100 env was
  repaired with the external origin and `roomhacker` default user. Server-44 was
  migrated to the working Go ShellMCP binary, canonical shared token, outbound
  long-poll queue, and heartbeat disabled; server-88 was aligned to the same
  queue/heartbeat policy. Existing per-host identities were preserved.
- Verification: Focused Python `30 passed`; full Python `133 passed, 2 skipped`;
  Go Hub and Go ShellMCP `go test ./...` passed. External Custom GPT OpenAPI
  returned HTTP 200; relay inventory showed 29 records and 100/88/44 online;
  authenticated `hostname && uptime` completed with returncode 0 on all three.
  All three ShellMCP services are active and recent logs show no queue 401 or
  heartbeat failure.
- Delivery: Live runtime changes applied and restarted; server-44 binary backup
  retained at `/opt/gptadmin/bin/rootd-go.bak.codex-20260722`. Source changes in
  `cli.py` and `tests/test_update_semantics.py` remain uncommitted locally.
- Next: Commit the source regression fix when publication is requested.

## 2026-07-22 - Share Android 4G as a dual LAN proxy - completed

- Milestone: `S0.2`
- Owner: Codex
- Scope: Android S21 4G egress, ADB transport, and LAN proxy exposure from
  `roomhacker-server-100`.
- Baseline / red evidence: Android shell traffic could ping over `rmnet4`, but
  the existing ShellMCP queue route to the private Hub address was unreachable;
  no proxy daemon existed and LAN port 3126 was free. The first proxy smoke
  returned 502 because Android shell DNS was empty.
- Change: Added a TDD-tested Go proxy supporting SOCKS5 CONNECT and HTTP
  CONNECT on one TCP listener, with explicit DNS fallback for Android. Added a
  systemd bridge that keeps the Android process alive through ADB, forwards an
  internal port, and selects a free LAN port from 3126 downward. The live
  service is enabled on the LAN bind address with port 3126; heartbeat is not
  involved. UDP is explicitly unsupported because ADB forward is TCP-only.
- Verification: Proxy unit tests passed; full Go ShellMCP `go test ./...`,
  Android arm64 build, `bash -n`, and `git diff --check` passed. Internal ADB
  tests returned HTTP 200 through both protocols. From a LAN client, HTTP
  CONNECT and SOCKS5 both returned HTTP 200; both produced the same masked
  mobile egress while direct server egress was different.
- Delivery: Live Android binary, ADB forward, LAN systemd service, and state
  file are active; selected LAN port is recorded in
  `/etc/gptadmin/android-4g-proxy.env`. Source changes are uncommitted.
- Next: Use a TUN/tun2socks design only if LAN UDP is required.

## 2026-07-22 - Hub Network Tunnel controller - completed

- Milestone: `S2.2`
- Owner: Codex
- Scope: Implement only the Hub network proxy capability controller, dedicated
  proxy-control HTTP routes and Hub tools covered by
  `go-hub/internal/hub/network_proxy_test.go`; do not touch command/relay/shell
  queues, execution, heartbeat liveness, relay sockets, agents/connectors,
  child MCP servers or deployment.
- Baseline / red evidence: Focused RED first failed on the incomplete controller
  wiring and then on missing OpenAPI contract entries.
- Change: Added the Hub-side capability controller with explicit `lan` and
  `internet_egress` scopes, finite leases, profile/agent authorization,
  target-bound one-time client/agent grants, revoke signalling, fail-closed
  persistence and dedicated proxy-control routes/tools. Internet egress also
  rejects CGNAT, TEST-NET, benchmark, documentation and reserved ranges, and
  normalizes IPv4-mapped IPv6 literals before policy checks. The command queues
  and heartbeat liveness remain untouched.
- Verification: `cd go-hub && go test ./internal/hub -run 'Proxy|Network' -count=1`
  and `cd go-hub && go test ./...` passed. `public/openapi.yaml` parsed and
  included all proxy-control paths/schemas; `git diff --check` passed.
- Delivery: Commits `383af8b` (`feat: add Hub Network Tunnel controller`),
  `848cd9a` (reserved-range security fix) and the IPv4-mapped IPv6 policy fix;
  no push, relay deployment or external proxy data-plane in scope.
- Next: Implement the isolated proxy relay and edge-agent transport; keep the
  controller API as the only control-plane dependency.
## 2026-07-22 - Normalize unscoped OAuth client inventory - completed

- Milestone: `S1.4`
- Owner: Codex
- Scope: Keep the React Clients inventory usable when OAuth registrations are
  present alongside managed MCP clients.
- Baseline / red evidence: `oauthClientInventory()` emitted
  `access_mode:""` for OAuth registrations; React rejected that value and
  showed `Сервер вернул некорректный режим доступа клиента.`.
- Change: Hub omits `access_mode` for unscoped OAuth inventory rows and the UI
  normalizes the legacy empty-string response to `null`.
- Verification: React `18 passed`, lint/build passed; Go `go test ./...`
  passed; focused red/green tests cover both the old response and the new
  serialization contract.
- Delivery: Commit `0b921c7` is deployed with build `128`; live Hub inventory
  now omits the empty OAuth mode and the service is active.
- Verification: Authenticated local API smoke returned seven clients, with the
  OAuth row's `access_mode` omitted and managed/legacy rows retaining valid
  modes. Existing admin password was not changed.
- Next: Push the integrated commit and keep the existing admin password stable.

## 2026-07-24 - Public product-auth documentation boundary correction - completed

- Milestone: `S0.5`, `S1.1`, `S1.3`
- Owner: Codex
- Scope: Public README/onboarding/integration docs and CLI setup/help surfaces.
- Baseline / red evidence: `tests/test_product_auth_language.py` failed for six product documents and then failed on `setup --help` exposing an internal relay credential name.
- Change: Replaced credential-copy instructions and CLI help/error wording with Hub URL, AdminPassword, OAuth and managed connection language.
- Verification: `pytest -q tests/test_product_auth_language.py tests/test_integration_certification.py tests/test_secret_ingress_contract.py tests/test_cli_token_deprecation.py` passes `15 passed`; targeted forbidden-name scan is empty.
- Delivery: Local uncommitted slice on the single linear branch; no deployment or push.
- Next: Run the full Python suite and commit this documentation/security correction.

## 2026-07-24 - Legacy dashboard credential boundary correction - completed

- Milestone: `S0.5`, `S1.6`
- Owner: Codex
- Scope: `public/admin_dashboard.html` legacy admin surface.
- Baseline / red evidence: Product-language regression failed because the dashboard displayed internal credential names and used `shell_exec` to read/write the Hub environment file.
- Change: Replaced the legacy security panel with typed `/admin/api/security/preset` state and a Hub connection-page link; removed client-side credential generation, export and shell-based environment mutation.
- Verification: `pytest -q tests/test_product_auth_language.py tests/test_admin_dashboard_js.py tests/test_admin_ui.py` passes `20 passed`.
- Delivery: Local uncommitted slice on the single linear branch; no deployment or push.
- Next: Run the full Python suite once more, then commit the combined documentation/UI security correction.

## 2026-07-24 - Browser extension OAuth connection - completed

- Milestone: `S1.3`, `S4.1`
- Owner: Codex
- Scope: Browser extension OAuth client and Hub same-origin callback.
- Baseline / red evidence: `tests/test_browser_extension_oauth.py` failed because the userscript stored a manual bridge key and called legacy query-key prompt endpoints.
- Change: Added `/connect/callback`, same-origin redirect validation, browser-safe code handoff, PKCE registration/exchange, and OAuth JSON-RPC calls to `/mcp`; removed manual credential entry and legacy prompt calls from the userscript.
- Verification: `cd go-hub && go test ./internal/hub -run '^TestBrowserExtensionOAuthUsesSameOriginCallbackWithoutCredentialCopy$' -count=1`, `pytest -q tests/test_browser_extension_oauth.py`, and `node --check public/mcp-bridge.user.js` pass.
- Delivery: Local uncommitted slice on the single linear branch; no deployment or push.
- Next: Run Hub full/race/vet and full Python acceptance, then commit this browser-connection slice.

## 2026-07-24 - Browser OAuth final-head clean-clone proof - completed

- Milestone: `S0.2`, `S1.3`, `S3.4`, `S4.1`
- Owner: Codex
- Scope: Reproducibility from fresh clone at commit `b6cae04bc32bea8c5a8c003b0e5a87bc41282367`.
- Baseline / red evidence: N/A; post-commit acceptance.
- Change: No additional runtime change; validated the committed browser OAuth and security surfaces without the source worktree.
- Verification: Clone `/tmp/gptadmin-clean-oauth-KaynGY` passed Hub `go test ./...`, browser/product contracts `11 passed`, completion matrix `12 passed`, userscript `node --check`, and `bash tests/e2e/failover/run.sh` with all scenarios passed.
- Delivery: Local evidence only; no push, deploy or external-host claim.
- Next: Obtain real client/host, tagged CI, collector and physical fallback evidence before declaring the 1.0 gate complete.

## 2026-07-24 - Live runtime acceptance blocker - open

- Milestone: `S1.1`, `S3.4`
- Owner: Codex / deployment owner
- Scope: Read-only runtime probes for server-100 Hub/Tunnel and server-88 ShellMCP.
- Baseline / red evidence: `trash/logs/live-runtime-smoke-20260724.md` records server-100 Hub `:9001` unreachable with Hub inactive/Tunnel failed and server-88 `:25900` unreachable with user ShellMCP inactive.
- Change: No runtime mutation; preserved external state and kept local source acceptance separate.
- Verification: SSH `BatchMode` probes completed without credentials or service changes.
- Delivery: External blocker; no deploy or push from this branch.
- Next: Authorized deployment session must restore the supported current release and rerun endpoint/proxy/MCP/file/profile smoke.

## 2026-07-24 - Release and delivery contract hardening - completed locally

- Milestone: `S4.3`, `S3.4`
- Owner: Codex
- Scope: Immutable GitHub Action references, release ProxyRelay coverage and failover harness lifecycle.
- Baseline / red evidence: `tests/test_supply_chain_policy.py` failed on mutable `@vN` action tags; the new release contract failed because `go-proxyrelay` was absent; `tests/test_failover_harness.py` failed because the runner used fixed resources and had no orphan guard.
- Change: Pinned all workflow actions to verified commit SHAs, added the `go-proxyrelay` release test and cache input, and made failover runs use isolated roots/free port ranges, process groups and parent-watched fake FRP children.
- Verification: supply-chain/release focused tests pass (`17 passed`); `go-proxyrelay` tests pass; failover harness contract passes; all seven `bash tests/e2e/failover/run.sh` scenarios pass; post-run process check finds no repository-owned fake FRP orphan.
- Delivery: Local source and CI contract only; no tagged GitHub run, public attestation or host deployment claim.
- Next: Run the next tagged release through GitHub and preserve its immutable run ID.

## 2026-07-24 - Reference deployment and documentation product contracts - completed locally

- Milestone: `S5.1`, `S5.2`, `S5.3`
- Owner: Codex
- Scope: Small-team/home-lab/production blueprints, canonical docs map, docs CI link checks and evidence-first feedback intake.
- Baseline / red evidence: New RED tests failed because the three deployment blueprints, documentation map, feedback-loop process and required issue-template fields were absent.
- Change: Added `docs/DEPLOYMENT_BLUEPRINTS.md`, `docs/DOCUMENTATION_MAP.md`, `docs/FEEDBACK_LOOP.md`, docs CI execution and issue-template fields for reproduction, activation/support/incident signal and immutable evidence.
- Verification: deployment, docs and feedback contracts pass (`10 passed`); local Markdown links resolve; failover and release references point to tracked files.
- Delivery: Local documentation/process contract only; no real design-partner metrics, physical deployment, external client certification or commercial offering claim.
- Next: Obtain one consented external design-partner/host evidence artifact and link it from the worklog.

## 2026-07-24 - Live runtime blocker confirmation - open

- Milestone: `S1.1`, `S3.4`
- Owner: Codex / deployment owner
- Scope: Fresh direct-IP read-only probes for the known Hub/Tunnel and ShellMCP hosts.
- Baseline / red evidence: `trash/logs/live-runtime-smoke-20260724-rerun.md` records server-100 Hub inactive/Tunnel failed with `:9001` unreachable and server-88 ShellMCP inactive with `:25900` unreachable.
- Change: No runtime mutation; direct IPs were used after the SSH aliases failed DNS resolution, and existing service state was preserved.
- Verification: Both SSH probes completed in BatchMode and returned the same inactive/unreachable runtime result.
- Delivery: External blocker; no deploy, restart or credential change.
- Next: In an explicitly authorized deployment session, restore the supported release and run the authenticated endpoint/proxy/MCP/file/profile smoke.

## 2026-07-24 - Secret-safe live acceptance runner - completed locally

- Milestone: `S0.1`, `S1.1`, `S4.1`
- Owner: Codex
- Scope: One bounded runner for deployed Hub health/version, connection/OAuth discovery, OpenAPI and authenticated safe MCP call.
- Baseline / red evidence: `tests/test_live_acceptance.py` initially failed during collection because the runner did not exist.
- Change: Added `tests/e2e/live_acceptance.py`, its in-process HTTP regression, documentation and completion-matrix entry; bearer values and response bodies are excluded from summaries/errors.
- Verification: runner/docs tests pass (`4 passed`); completion matrix passes (`12 passed`); live execution remains opt-in and was not falsely claimed against inactive hosts.
- Delivery: Local acceptance tooling only; no service restart or public endpoint mutation.
- Next: Run the runner in the authorized deployment session, then append authenticated endpoint/proxy/MCP/file/profile evidence.

## 2026-07-24 - Open-core boundary contract - completed locally

- Milestone: `S5.4`
- Owner: Codex
- Scope: Self-hosted AGPL core, optional hosted/support convenience, data portability and no forced lock-in.
- Baseline / red evidence: `tests/test_sustainable_offering.py` failed because README references pointed to a missing `docs/OPEN_CORE_PLAN.md` and the canonical map omitted the offering boundary.
- Change: Added `docs/OPEN_CORE_PLAN.md`, linked it from the documentation map and added the boundary to the completion matrix.
- Verification: sustainable-offering/docs tests pass (`5 passed`); the document explicitly avoids claiming a currently available hosted service or commercial support.
- Delivery: Local product contract only; no pricing, hosted deployment or user-outcome claim was invented.
- Next: Keep any future hosted proposal behind a separate reviewed portability/security decision.

## 2026-07-24 - Integration control contract alignment - completed locally

- Milestone: `S4.1`
- Owner: Codex
- Scope: Documentation of the implemented `discover -> schema -> execute` Hub flow and its remaining schema-version boundary.
- Baseline / red evidence: `tests/test_docs_product_contract.py::test_integration_control_contract_matches_current_hub_scope` failed because the contract called the implemented path `Planned` and claimed no implementation.
- Change: Updated `docs/INTEGRATION_CONTROL_CONTRACT.md` to identify the current request-scoped Hub mapping, queued identity and retry/idempotency semantics; retained schema version/digest as the explicit remaining gap.
- Verification: contract and integration certification tests pass (`3 passed`); the canonical docs map now exposes the contract.
- Delivery: Local documentation alignment only; no new session-oriented adapter or external certification was claimed.
- Next: Obtain a real third-party/session adapter certification after the external client/host gate is available.

## 2026-07-24 - Process-level live runner verification - completed locally

- Milestone: `S0.1`, `S1.1`, `S4.1`
- Owner: Codex
- Scope: Run the secret-safe live acceptance runner against an actual disposable Go Hub binary.
- Baseline / red evidence: `tests/test_live_acceptance.py::test_live_runner_checks_actual_go_hub_process` failed because the runner expected `connect.json.mcp` instead of the authoritative `mcp_endpoint` field.
- Change: Corrected the runner and mock contract, and added process startup/readiness/cleanup coverage using a real Hub binary.
- Verification: `python3 -m pytest tests/test_live_acceptance.py -q` passes (`2 passed`); the full smoke covers health, version, connection/OAuth discovery, OpenAPI and authenticated safe MCP call.
- Delivery: Disposable local process evidence only; it does not certify the inactive external hosts, public Tunnel or third-party clients.
- Next: Run the same runner against the authorized live Hub URL and attach the redacted result artifact.

## 2026-07-24 - Disposable real-Hub canary gate - completed locally

- Milestone: `S3.5`
- Owner: Codex
- Scope: Real Go Hub binary version swap, MCP reconnect and bad-candidate rollback on one disposable endpoint.
- Baseline / red evidence: `tests/test_canary_acceptance.py` initially failed because the process-level canary harness was absent, then exposed a dynamic-module loader defect before implementation was complete.
- Change: Added `tests/e2e/canary_acceptance.py` and `docs/CANARY_ACCEPTANCE.md`; the harness builds `canary-old`/`canary-new`, swaps real processes, exercises the live runner and restores the known-good candidate after an invalid executable.
- Verification: `python3 -m pytest tests/test_canary_acceptance.py -q` passes (`1 passed`); no signed artifact, host service or public Tunnel claim is made.
- Delivery: Local process-level canary only; external clean-host/client reconnect and signed release evidence remain separate.
- Next: Run a signed canary on an authorized clean host and preserve its immutable artifact/run ID.

## 2026-07-24 - Process-level webhook ingress and callback gate - completed locally

- Milestone: `S4.1`, `S0.1`
- Owner: Codex
- Scope: Real Go Hub process route CRUD, authenticated webhook ingress, MCP demo dispatch, durable job polling and callback receiver.
- Baseline / red evidence: New process contract first exposed asynchronous callback completion (`status=completed` before `callback_status=delivered`); the test was corrected to poll the complete delivery state.
- Change: Added `tests/test_hub_contract.py::test_hub_contract_webhook_route_job_and_callback_through_process` and included it in the hooks completion matrix.
- Verification: The process-level webhook test passes (`1 passed`); route token is absent from callback payload and the callback receiver observes the job ID.
- Delivery: Local Go Hub process evidence only; public Tunnel ingress and external sender certification remain separate.
- Next: Run the same webhook path through the authorized public Tunnel with an external sender and callback receiver.

## 2026-07-24 - Process-level ProxyRelay acceptance gate - completed locally

- Milestone: `S3.1`, `S4.1`
- Owner: Codex
- Scope: Built ProxyRelay process metrics, signed stream tickets, client-agent pairing and frame forwarding.
- Baseline / red evidence: The existing blackbox covered an in-process HTTP server only; the new process contract initially caught test-harness API/configuration mistakes before the focused gate passed.
- Change: Added `go-proxyrelay/blackbox/process_blackbox_test.go` and registered it in the completion matrix.
- Verification: `cd go-proxyrelay && go test ./blackbox -run TestProxyRelayProcessMetricsAndStreamRoundTrip -count=1` passes; the test builds and launches the actual binary, checks `/metrics`, and verifies a signed frame round trip.
- Delivery: Local process evidence only; public Tunnel routing and authorized external client proof remain separate.
- Next: Run the same relay path through the authorized public Tunnel during the live deployment session.

## 2026-07-24 - Current-head clean-clone acceptance - completed locally

- Milestone: `S0.1`, `S3.5`, `S4.1`
- Owner: Codex
- Scope: Reproduce the current linear vertex from a fresh local clone without worktree modifications.
- Baseline / red evidence: Previous clean-clone evidence referenced an earlier OAuth checkpoint; current process and matrix gates needed proof at the latest ProxyRelay commit.
- Change: No source change; cloned branch `codex/haos-addon-public` at commit `32aa0a6d1c4982d9d3fdc9a13dc78bac232d05ec` into `/tmp/gptadmin-clean-current.eL0qRp` and ran the complete Go module tests plus completion matrix.
- Verification: Clone status was clean; `go test ./...` passed in `go-hub`, `go-shellmcp`, and `go-proxyrelay`; `python3 -m pytest tests/test_completion_matrix.py -q` passed (`12 passed`).
- Delivery: Reproducible local source evidence only; signed CI artifacts, deployed hosts, public Tunnel and real client proof remain external gaps.
- Next: Use the authorized deployment/CI session to obtain immutable external evidence rather than inferring it from this clean clone.

## 2026-07-24 - Installed MCP client golden-path acceptance - completed locally

- Milestone: `S0.1`, `S1.1`
- Owner: Codex
- Scope: Isolated real Codex, Claude and OpenCode CLI registration against a disposable canonical Hub, followed by a safe `demo` MCP call.
- Baseline / red evidence: Earlier acceptance classified real client applications as an unverified lane; the current host now exposes all three CLIs and the process test exercises each one without user configuration.
- Change: No runtime source change; reran the existing real-client contract and golden-path fixture at the current worktree.
- Verification: `python3 -m pytest tests/test_golden_paths.py tests/test_real_mcp_clients.py -q` passes (`3 passed`); no client test was skipped.
- Delivery: Local installed-client evidence only; ChatGPT/browser, native macOS/Windows/Android, public Tunnel and external OAuth evidence remain separate.
- Next: Attach the same canonical manifest and first-tool probe to authorized external client/runtime sessions.

## 2026-07-24 - Canonical observability contract - completed locally

- Milestone: `S3.1`, `S5.3`
- Owner: Codex
- Scope: Document Hub request correlation, ShellMCP propagation, ProxyRelay bounded metrics and optional OTLP export.
- Baseline / red evidence: The telemetry implementation and focused tests existed, but the documentation map had no canonical observability page; the docs contract failed on the missing surface.
- Change: Added `docs/OBSERVABILITY.md`, linked it from `docs/DOCUMENTATION_MAP.md`, and registered its link/contract check in the completion matrix.
- Verification: RED then GREEN `python3 -m pytest tests/test_docs_product_contract.py -q` (`4 passed`); Hub trace/OTLP, ShellMCP propagation and ProxyRelay process gates also pass.
- Delivery: Local documentation and redaction evidence only; retained production OTLP trace, collector availability and cross-host relay span remain external.
- Next: Capture an authorized collector/runtime artifact before closing the full S3.1 exit gate.

## 2026-07-24 - Post-observability full acceptance - completed locally

- Milestone: `S0.1`, `S3.1`, `S4.1`, `S5.3`
- Owner: Codex
- Scope: Re-run the evidence index and complete Python suite after the observability documentation and client-golden-path updates.
- Baseline / red evidence: The combined runner exceeded the tool RPC limit; no pytest child remained, so the two gates were rerun separately with bounded commands.
- Change: No further runtime change; preserved the exact split command evidence.
- Verification: `python3 -m pytest tests/test_completion_matrix.py -q` passed (`12 passed` in `117.05s`); `python3 -m pytest tests/ --ignore=tests/e2e` passed (`234 passed, 3 skipped` in `183.59s`).
- Delivery: Current local acceptance is green; external hosts, public Tunnel, native clients, retained collector trace and signed CI remain unproven.
- Next: Obtain the authorized external artifacts, then rerun the live acceptance runner against the exact deployed commit.

## 2026-07-24 - Process-level access-profile enforcement - completed locally

- Milestone: `S2.1`, `S4.1`
- Owner: Codex
- Scope: Real Hub HTTP process profile CRUD, managed-token issuance/binding, allowed `discover` and denied `execute` MCP behavior.
- Baseline / red evidence: The new process regression first returned HTTP 500 because the generic Hub contract fixture omitted the OAuth signing configuration required by managed-token issuance; this was recorded as `PROFILE-PROCESS-FIXTURE-OAUTH-20260724` in `docs/BUGS.md`.
- Change: Added optional request headers to the black-box helper, the profile process regression, deterministic fixture signing configuration and a completion-matrix row.
- Verification: `python3 -m pytest tests/test_hub_contract.py -k profile_binding_enforces_mcp_tool_policy -q` passes (`1 passed`); forbidden `execute` is filtered/denied while `discover` succeeds.
- Delivery: Local Hub process evidence only; external identity/profile and public client evidence remain separate.
- Next: Run the same profile-bound token through the authorized public client/Tunnel smoke.

## 2026-07-24 - Profile-gate full acceptance - completed locally

- Milestone: `S2.1`, `S4.1`
- Owner: Codex
- Scope: Re-run the executable evidence index and Python suite after adding process-level profile enforcement.
- Baseline / red evidence: The new profile contract was first red on missing fixture signing configuration; after the fixture correction the full Hub contract passed.
- Change: No additional runtime change; retained the focused, matrix and full-suite evidence.
- Verification: `python3 -m pytest tests/test_hub_contract.py -q` passed (`7 passed`); completion matrix passed (`12 passed`); full Python passed (`235 passed, 3 skipped`, 237 collected).
- Delivery: Local current-tree evidence only; public identity/Tunnel and native-host profile proof remain external.
- Next: Re-run the profile-bound smoke against the authorized deployed commit when runtime access is available.

## 2026-07-24 - Process-level read-only symlink boundary - completed locally

- Milestone: `S2.2`, `S4.1`
- Owner: Codex
- Scope: ShellMCP public MCP `system_inspect(read_file)` rejection of a symbolic-link escape from the configured inspection root.
- Baseline / red evidence: The repository had unit regressions for symlink escape, but no process-level file-sharing/security contract.
- Change: Added the process test, configured only the temporary inspection root in the test fixture and registered the gate in the file-sharing matrix.
- Verification: `python3 -m pytest tests/test_shellmcp_contract.py -k symlink_escape_through_process -q` passes (`1 passed`); the process returns a structured MCP error and never returns the outside file content.
- Delivery: Local ShellMCP process evidence only; native-host file-sharing and public client evidence remain external.
- Next: Retain this gate in the full ShellMCP and completion-matrix runs.

## 2026-07-24 - File-security full acceptance - completed locally

- Milestone: `S2.2`, `S4.1`
- Owner: Codex
- Scope: Re-run the full executable matrix and Python suite after adding process-level symlink protection coverage.
- Baseline / red evidence: The new public-process security test initially asserted `symlink` while the runtime correctly returned `symbolic links`; the assertion was aligned to the existing fail-closed error contract.
- Change: No production behavior change; retained the exact process fixture and matrix evidence.
- Verification: `python3 -m pytest tests/test_shellmcp_contract.py -q` passed (`10 passed`); completion matrix passed (`12 passed`); final Python suite passed (`236 passed, 3 skipped`, 238 collected / 1 collection skip).
- Delivery: Local current-tree evidence only; native host and public Tunnel file-security proof remain external.
- Next: Run the same file/profile/policy probes against the authorized deployed commit.

## 2026-07-24 - Project-plan status reconciliation - completed locally

- Milestone: `S3.1`, `S4.1`, `S5.1`, `S5.3`, `S5.4`
- Owner: Codex
- Scope: Reconcile canonical `docs/PROJECT_PLAN.md` statuses with current authoritative local evidence without closing external gates.
- Baseline / red evidence: The plan still labeled the policy/observability/ecosystem baseline `Not started`, S5.1 `In progress` and S5.4 `Planned` despite their checked contracts and completion-matrix evidence.
- Change: Marked the bounded policy/observability/ecosystem baseline `In progress`, S5.1 reference deployments `Complete` and S5.4 sustainable offering `Complete`; retained S5.2 and external runtime/CI/native gates as open or in progress.
- Verification: `python3 -m pytest tests/test_docs_product_contract.py tests/test_deployment_blueprints.py tests/test_feedback_loop_contract.py tests/test_sustainable_offering.py -q` passes (`10 passed`).
- Delivery: Documentation/status reconciliation only; no external adoption, deployment, collector or hosted-service claim was added.
- Next: Obtain immutable external evidence for the remaining in-progress rows before changing their status.

## 2026-07-24 - Executable documentation snippet contract - completed locally

- Milestone: `S4.1`, `S4.2`, `S5.3`
- Owner: Codex
- Scope: Make canonical integration, capability-catalog, observability, extension, backup and live-acceptance pages expose executable verification commands checked by docs CI.
- Baseline / red evidence: `test_canonical_docs_include_executable_verification_snippets` failed because `INTEGRATION_CONTROL_CONTRACT.md` had no fenced verification command.
- Change: Added the missing integration and catalog verification snippets and expanded `tests/test_docs_product_contract.py` to require the commands across supported contract pages.
- Verification: RED then GREEN docs contract `5 passed`; the referenced integration conformance command passes; catalog command passes (`4 passed`).
- Delivery: Local docs/command evidence only; translation review and real external usage outcomes remain open.
- Next: Keep snippet and link checks in the release workflow while external S5.2 evidence is gathered.

## 2026-07-24 - Documentation snippet full acceptance - completed locally

- Milestone: `S5.3`
- Owner: Codex
- Scope: Re-run the completion matrix and complete Python suite after the executable-snippet contract change.
- Baseline / red evidence: The docs contract had been updated after the previous full-suite run, so its matrix and full-suite evidence needed refresh.
- Change: No additional product change; preserved the separate bounded test runs.
- Verification: Completion matrix passed (`12 passed` in `127.85s`); full Python passed (`237 passed, 3 skipped`, 239 collected / 1 collection skip).
- Delivery: Current local docs acceptance is green; translation review and external adoption evidence remain open.
- Next: Use the docs snippets in the authorized deployment/client evidence session.

## 2026-07-24 - Schema version and digest control contract - completed locally

- Milestone: `S4.1`
- Owner: Codex
- Scope: Bind `discover -> schema -> execute` to a deterministic effective-schema version/digest across MCP and REST relay paths.
- Baseline / red evidence: The canonical integration contract explicitly listed schema version/digest as a future gap; new Go and process regressions first failed because metadata was absent, then exposed a digest mismatch caused by action hints.
- Change: Added `gptadmin.mcp-schema/v1`, SHA-256 metadata, optional execute freshness validation with `409 schema_mismatch`, OpenAPI/Apps SDK fields, docs and matrix coverage.
- Verification: Go conformance passes; `python3 -m pytest tests/test_hub_contract.py -k relay_and_openapi -q` passes (`1 passed`); stale digest is rejected before tool execution.
- Delivery: Local Hub/relay contract evidence only; external adapter certification and public client/Tunnel evidence remain separate.
- Next: Add the schema identity to the authorized external adapter/client smoke when deployment access is available.

## 2026-07-24 - Schema contract full acceptance - completed locally

- Milestone: `S4.1`, `S3.5`
- Owner: Codex
- Scope: Re-run Hub full/race/vet, Darwin cross-builds, completion matrix and Python suite after the schema identity change.
- Baseline / red evidence: The initial process endpoint run found and fixed the action-hint digest mismatch before the final acceptance run.
- Change: No further source change after `33609eb`; retained the exact bounded verification results.
- Verification: Hub `go test ./...`, `go test -race ./...`, `go vet ./...` pass; Darwin arm64/amd64 builds pass; completion matrix `12 passed`; full Python `237 passed, 3 skipped` (239 collected / 1 collection skip).
- Delivery: Current local schema/endpoint evidence is green; external adapter/client/Tunnel certification remains open.
- Next: Run schema-bound execute through the authorized public adapter/client smoke.

## 2026-07-24 - Schema metadata argument-boundary regression - completed locally

- Milestone: `S2.2`, `S4.1`
- Owner: Codex
- Scope: Ensure schema freshness metadata remains control-plane data and is never forwarded as downstream tool arguments.
- Baseline / red evidence: The new conformance assertion showed `schema_version` and `schema_digest_sha256` in an unsupported Hub tool's argument echo when top-level `arguments` was omitted; recorded as `MCP-SCHEMA-METADATA-ARG-20260724`.
- Change: Added both fields to the reserved top-level routing set.
- Verification: `go test ./internal/hub -run TestMCPIntegrationDiscoverSchemaExecuteConformance -count=1` passes; the test asserts no schema metadata leak.
- Delivery: Local Hub boundary evidence only; external adapter/client evidence remains separate.
- Next: Preserve the regression in the full Hub and completion-matrix gates.

## 2026-07-24 - Schema metadata boundary full acceptance - completed locally

- Milestone: `S2.2`, `S4.1`, `S3.5`
- Owner: Codex
- Scope: Prove the schema-control metadata fix across the complete local acceptance ladder.
- Baseline / red evidence: The focused conformance regression caught top-level schema fields crossing into downstream tool arguments; the reserved-field fix was applied before this gate.
- Verification: `go test ./...`, `go test -race ./...`, and `go vet ./...` pass; completion matrix `12 passed in 126.87s`; full Python suite `237 passed, 3 skipped` from `239 collected / 1 collection skip` in `182.18s`.
- Delivery: Local Hub, endpoint, MCP forwarding, policy and security regression gates are green; external runtime, public adapter/client and native-platform evidence remain open.
- Next: In an authorized external session, run the same schema-bound execute and argument-boundary smoke through the public Tunnel/client path.

## 2026-07-24 - Cross-component schema boundary acceptance - completed locally

- Milestone: `S0.2`, `S0.3`, `S4.1`
- Owner: Codex
- Scope: Recheck the unaffected ShellMCP implementation and supported Darwin build targets after the Hub schema-boundary change.
- Verification: `cd go-shellmcp && go test ./...` passes; `go-hub` Darwin arm64 and amd64 cross-builds pass.
- Delivery: Both Go components and local cross-platform compilation are green; real macOS runtime authorization and external deployment evidence remain open.
- Next: Attach the same evidence to the authorized native/public acceptance run when those hosts are available.

## 2026-07-24 - Completion audit continuation - local gates green, external runtime still open

- Milestone: `S0.1`, `S1.3`, `S4.1`
- Owner: Codex
- Scope: Reconcile the remaining plan rows with the current executable evidence and recheck installed client handshakes and external runtime availability.
- Verification: completion matrix has 63 rows with command/evidence metadata; real installed `codex`, `claude` and `opencode` disposable-Hub handshake passes (`1 passed in 18.53s`); focused secure secret-ingress tests pass; current server-100 probe reports Hub `inactive`, Tunnel `failed`, health `000`, and server-88 reports ShellMCP `inactive`, health `000`.
- Delivery: Local proxy, endpoints, hooks, MCP forwarding, file sharing, profiles, security policy, secret ingress and client-CLI evidence are green. The repeated runtime observation remains `LIVE-RUNTIME-INACTIVE-20260724`; no deployment or restart was performed.
- Next: Use an explicitly authorized deployment session to restore the supported release, then run the existing live acceptance runner through the public Tunnel and named clients.

## 2026-07-24 - Canonical runtime contract diagnosis - handed off

- Milestone: `S1.1`, `S0.1`
- Owner: Codex
- Scope: Reconcile the expected external service names with the actual read-only systemd and port state before any deployment action.
- Evidence: `trash/logs/live-runtime-canonical-unit-rerun-20260724.md` and BUG `LIVE-RUNTIME-INACTIVE-20260724`.
- Confirmed: server-100 has an inactive Hub, a stale/unattributed `:9001` listener and Tunnel `router config conflict`; server-88 lacks the expected user unit, while the root `shellmcp.service` is active but polls its queue with HTTP 401 and exposes no expected listener.
- Delivery: Corrected the BUG record so it no longer mislabels server-88's canonical system service as inactive; no remote state was changed.
- Next: Deployment owner must reconcile units/artifacts/auth and rerun the authenticated live acceptance runner.

## 2026-07-24 - Doctor detects legacy ShellMCP deployment drift - completed locally

- Milestone: `S1.2`, `S1.1`
- Owner: Codex
- Scope: Prevent an active legacy `rootd-go` unit from being reported as the supported ShellMCP runtime.
- Baseline / red evidence: `test_doctor_reports_legacy_shellmcp_binary_in_canonical_unit` failed because `doctor` only checked service-manager state.
- Change: Added a secret-free unit contract check for `rootd-go` and `rootd-go-canary`; the check reports an actionable update message and increments doctor issues.
- Verification: RED then GREEN focused regression; `python3 -m pytest tests/test_doctor_json.py -q` -> `5 passed`.
- Delivery: Local diagnosis is now fail-closed for the observed deployment drift; the remote unit and credentials remain unchanged.
- Next: Run `gptadmin doctor --json` on server-88 after the authorized deployment repair and require no `shellmcp_unit` error.

## 2026-07-24 - Doctor rejects stale Hub listeners - completed locally

- Milestone: `S1.2`, `S1.1`
- Owner: Codex
- Scope: Ensure an occupied Hub TCP port is not treated as a healthy Hub when the listener does not serve `/healthz`.
- Baseline / red evidence: `test_doctor_rejects_tcp_listener_without_hub_health` failed because the doctor checked only `connect_ex`.
- Change: For an explicitly configured `HUB_HOST`/`HUB_BIND`, doctor now performs a bounded local `/healthz` JSON probe and reports `hub_local_health` as an error on stale or foreign listeners.
- Verification: RED then GREEN regression; `python3 -m pytest tests/test_doctor_json.py -q` -> `6 passed`.
- Delivery: The observed server-100 stale `:9001` listener will now be diagnosable by the product without exposing response data or credentials.
- Next: Re-run `gptadmin doctor --json` during the authorized external repair.

## 2026-07-24 - Secret-safe deployment runtime runner - completed locally

- Milestone: `S1.1`, `S3.5`
- Owner: Codex
- Scope: Automate read-only diagnosis of canonical Hub/Tunnel and ShellMCP deployment state before any external repair.
- Baseline / red evidence: No service-level runner existed; new tests initially failed on the missing parser/SSH probe API.
- Change: Added `tests/e2e/deployment_runtime.py` with fixed remote scripts, whitelist-only parsing and redacted JSON; added the operation to `tests/fixtures/completion-matrix.json` and documented it in `docs/LIVE_ACCEPTANCE.md`.
- Verification: runner/docs tests `8 passed`; completion matrix `12 passed`; actual probes report server-100 issues `hub_service_not_running`, `hub_health_failed`, `tunnel_router_conflict` and server-88 issues `legacy_shellmcp_binary`, `queue_auth_failed`.
- Delivery: The remaining external repair now has a repeatable, non-mutating evidence command; no credentials, stderr or remote state are returned or changed.
- Next: After authorized repair, rerun both probes and `tests/e2e/live_acceptance.py` with a short-lived scoped connection.

## 2026-07-24 - Deployment probe honors configured Hub ports - completed locally

- Milestone: `S1.1`, `S3.5`
- Owner: Codex
- Scope: Keep the new deployment runtime runner correct for non-default Hub ports.
- Baseline / red evidence: `test_hub_probe_accepts_a_configured_non_default_port` failed because parser logic was fixed to `9001`.
- Change: Parser now evaluates the one reported `port|<configured>|<http-code>` observation and requires `200`.
- Verification: RED then GREEN; `python3 -m pytest tests/test_deployment_runtime.py -q` -> `4 passed`.
- Delivery: Custom `HUB_PORT` deployments are now diagnosed without a false failure or relaxed health requirement.
- Next: Re-run the runner after external repair on the actual configured ports.

## 2026-07-24 - Final Go acceptance after runtime probe hardening - completed locally

- Milestone: `S0.2`, `S0.3`
- Owner: Codex
- Scope: Re-run both Go components and supported Hub cross-builds after the deployment probe correction.
- Verification: `go-hub go test ./...`, `go test -race ./...`, `go vet ./...`, `go-shellmcp go test ./...`, and Darwin arm64/amd64 Hub builds all pass.
- Delivery: Source and cross-platform local evidence remain green; external deployment/Tunnel/client evidence remains separate.
- Next: Attach this baseline to the authorized deployment repair and public live acceptance.

## 2026-07-24 - Primary WebUI login outage diagnosed - handed off

- Milestone: `S1.1`, `S1.3`
- Owner: Codex
- Scope: Verify the user's WebUI login failure from the public origins without submitting credentials.
- Evidence: `trash/logs/webui-login-probe-20260724.md` and BUG `WEBUI-LOGIN-OUTAGE-20260724`.
- Confirmed: Primary origin returns nginx `502` for every Hub/WebUI/OAuth path; personal Tunnel serves an older `1.0.5` Hub with a password form but missing current `/connect.json`.
- Delivery: Login failure is an upstream availability/deployment issue, not a browser password validation issue; no remote state changed.
- Next: Authorized owner must restore the current backend and verify the primary WebUI login end-to-end.

## 2026-07-24 - Primary WebUI backend restored - completed

- Milestone: `S1.1`, `S1.3`
- Owner: Codex
- Scope: Restore the primary Hub backend and verify the unauthenticated WebUI login path without handling credentials.
- Baseline / red evidence: `trash/logs/webui-login-probe-20260724.md` recorded HTTP 502 from the primary origin; server-100 Hub was inactive and Tunnel was failed.
- Change: Created private rollback artifact `/var/backups/gptadmin/server100-webui-20260724T214223Z` and started only `gptadmin-hub.service` once; nginx, Tunnel, DNS, credentials, configs and standby were left unchanged.
- Verification: `trash/logs/webui-login-repair-20260724.md` records Hub direct/LAN/public HTTP 200 responses for `/healthz`, `/version` and `/admin/login`, OAuth discovery HTTP 200, and a real Playwright login-page snapshot with password field and `Войти` button. No password was submitted.
- Delivery: Primary WebUI availability restored in the current linear worktree; remote mutation is rollback-preserved and separately evidenced.
- Next: User signs in manually with the existing AdminPassword; then run authenticated live acceptance and continue the remaining Tunnel/ShellMCP runtime gates.

## 2026-07-24 - Deployment probe rejects historical Tunnel noise - completed

- Milestone: `S1.1`, `S3.5`
- Owner: Codex
- Scope: Keep the secret-safe Hub runtime probe truthful after the primary Hub recovery.
- Baseline / red evidence: Real server-100 probe reported `tunnel_router_conflict` although Hub was active and healthy; the runner searched an unbounded historical journal tail.
- Change: Anchored Tunnel conflict detection to the service `ExecMainStartTimestamp`; added regression coverage before implementation.
- Verification: RED then GREEN; `python3 -m pytest tests/test_deployment_runtime.py -q` -> `5 passed`; real server-100 probe -> `status=passed`, Hub active/running, port 9001 HTTP 200, `router_conflict=false`.
- Delivery: Commit `366290a`; no additional remote mutation.
- Next: Run the completion matrix, then perform the remaining authenticated and non-WebUI runtime acceptance.

## 2026-07-24 - Deployment probe requires live Tunnel state - completed

- Milestone: `S1.1`, `S3.5`
- Owner: Codex
- Scope: Ensure the Hub/Tunnel deployment probe cannot report success while the canonical Tunnel unit is failed.
- Baseline / red evidence: Real server-100 probe reported `status=passed` with Hub healthy but `gptadmin-tunnel-frpc.service=failed`.
- Change: Added Tunnel unit state to the fixed remote protocol and made `active/running` a Hub probe requirement; added a failing regression before implementation.
- Verification: RED then GREEN; `python3 -m pytest tests/test_deployment_runtime.py -q` -> `6 passed`; real server-100 probe now truthfully reports `tunnel_service_not_running`.
- Delivery: Commit `a1c5b66`; no additional remote mutation.
- Next: Run completion-matrix verification, then repair Tunnel only after signed reclaim/lease evidence is available.

## 2026-07-24 - Personal Tunnel route outage diagnosed - handed-off

- Milestone: `S1.1`, `S3.4`
- Owner: Codex
- Scope: Diagnose the user-reported personal Tunnel `/admin/` 404 without touching credentials or edge configuration.
- Baseline / red evidence: `trash/logs/personal-tunnel-route-probe-20260724.md` records a real browser FRP 404 page and HTTP 404 for all tested Hub paths across all observed DNS addresses.
- Change: No remote mutation; compared the edge response with the healthy server-100 Hub and reran the secret-safe deployment probe.
- Verification: Hub is `active/running` with port 9001 HTTP 200; canonical Tunnel is `failed/failed` and the probe reports `tunnel_service_not_running`.
- Delivery: Root cause is narrowed to the missing/failed Tunnel route; preserve the current healthy Hub.
- Next: After manual WebUI authentication, perform the backup-first signed-reclaim Tunnel repair and repeat public/browser acceptance.

## 2026-07-24 - Personal Tunnel route restored - completed

- Milestone: `S1.1`, `S3.4`
- Owner: Codex
- Scope: Restore the missing personal Tunnel route without changing credentials, configs, nginx, DNS, standby or Hub.
- Baseline / red evidence: `trash/logs/personal-tunnel-route-probe-20260724.md` recorded the browser FRP 404 page and failed server-100 Tunnel unit.
- Change: Verified private backup/hash continuity and HAOS reclaim ownership, then started `gptadmin-tunnel-frpc.service` exactly once at `2026-07-24T22:52:21Z`.
- Verification: `trash/logs/personal-tunnel-route-repair-20260724.md` records 3 successful proxy registrations, 0 conflicts/failures/restarts, HTTP 200 on all observed personal-edge addresses, and build `128` / commit `fdca78d` on personal and primary origins.
- Delivery: Personal Tunnel and primary Hub are healthy; HAOS has no fallback `frpc` process.
- Next: User refreshes `/admin/` and signs in manually; then run authenticated live acceptance.

## 2026-07-25 - Admin login session-loop repair - completed

- Milestone: `S1.3`, `S2.1`
- Owner: Codex
- Scope: Reproduce the reported password-loop through a real Hub process, redirect, cookie jar and admin API refresh without recording the password.
- Baseline / red evidence: `trash/logs/admin-login-session-loop-20250725.md` records the direct HTTP Secure-cookie loop and the browser-specific hypothesis; the duplicate-cookie black-box failed before implementation.
- Change: `go-hub/internal/hub/server.go` now validates all same-name session cookies and accepts a valid signed value even when a stale duplicate precedes it; `tests/test_live_acceptance.py` contains the non-optional process-level black-box regression.
- Verification: RED then GREEN; `python3 -m pytest tests/test_live_acceptance.py -q` -> `3 passed`; focused Hub auth test passes; live HTTPS duplicate-cookie flow passes after deployment.
- Delivery: Commit `3c4baf4`; binary build `128` deployed with rollback backup `/var/backups/gptadmin/server100-admin-auth-20250725T005926Z`.
- Next: User refreshes and signs in; then run authenticated live acceptance and continue the remaining server-88 runtime gate.

## 2026-07-25 - Legacy admin CSS asset-path repair - completed locally

- Milestone: `S1.1`
- Owner: `Codex`
- Scope: Restore CSS/JavaScript loading for the packaged `/admin/legacy/` static console without changing auth or API routes.
- Baseline / red evidence: Live authenticated screenshot shows the legacy console unstyled; source `public/admin/index.html` points at `/admin/style.css` and `/admin/app.js`, while release packaging copies that payload to `/admin-legacy/`.
- Change: Added a release-contract regression, changed `public/admin/index.html` to use document-relative `style.css` and `app.js`, and extended the Go static-handler regression to verify the CSS response.
- Verification: RED then GREEN; focused UI/admin contracts `17 passed`; full Python suite `247 passed, 3 skipped`; `go test ./...` passed; `git diff --check` passed.
- Delivery: Atomically deployed only `admin-legacy/index.html` to server-100 at `20260725T020209Z`; rollback backup and hashes are recorded in `trash/logs/admin-legacy-css-deploy-20260725T020209Z.md`; no restart or binary change.
- Next: Hard-refresh the supplied browser tab and confirm the styled console; leave the separate clean `v129` release integration for a dedicated release task.

## 2026-07-27 - Codex OAuth refresh and widget resource recovery - completed

- Milestone: `S0.1`, `S0.5`
- Owner: Codex
- Scope: Preserve existing Hub bearer connections, add durable OAuth refresh rotation, and prove authenticated Apps SDK widget resource delivery.
- Baseline / red evidence: Live build `134` / `beed6963404ee2c1978ea2246cf5c46db5757ce9` advertised only `authorization_code`; its redacted existing-credential matrix returned `401` on admin, MCP and relay. The TDD regression failed because OAuth discovery omitted `refresh_token`.
- Change: Added digest-only five-year OAuth refresh records with client/resource binding and rotation, advertised refresh support, retained existing deprecated bearer credentials until explicit rotation/removal, and aligned CLI connection-status copy.
- Verification: Focused RED/GREEN Hub OAuth/widget and legacy-bearer tests passed; Hub suite and affected Python contract/live/canary/extension tests passed locally. Release run `30301756447` passed. After standard Hub-only update, build `136` / `6d3446228d5125efda31b27120b6e86b52bd952e` returned post-deploy `200` on the redacted admin/MCP/relay matrix; a real authorization-code -> refresh -> widget `resources/read` lifecycle passed, including old-refresh rejection.
- Delivery: Public release `v136`; standard updater deployed the Hub without printing or rotating credentials.
- Next: Reauthorize the existing GPTADMIN Codex connection once, then record a real `discover -> schema -> execute` client acceptance.
