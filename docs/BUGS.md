# Bug tracker

This is the project’s append-only working register for bugs found during
development, diagnostics, deployment, or live verification.

Rules:

- Record a bug as soon as it is observed, before implementing a fix.
- Use an immutable evidence path, commit, test name, or runtime artifact ID;
  never paste secrets, private URLs, customer data, or raw logs.
- Keep actionable bugs open until a focused verification proves the fix.
- At the end of the current goal, resolve every actionable open entry before
  final handoff, unless a concrete external blocker is recorded.

## 2026-07-27 - LEGACY-BEARER-DEADLINE-20260727 - Existing Hub connections expired by a fixed date - fixed

- Component: `go-hub/internal/hub/server.go` legacy bearer acceptance and `cli.py` connection-status copy.
- Evidence: immutable GitHub Actions run `30301216345` for release `v135` failed its real Hub process fixtures on 2026-07-27 with `401` and `legacy token expired; use OAuth connection` after the fixed migration deadline.
- Confirmed facts: Existing bearer-backed process, canary and extension checks were rejected even though the release must preserve already issued connections. New installs do not create this deprecated credential.
- Root cause: `FromEnv` applied a calendar cutoff while compatibility fixtures and retained deployments still used the existing bearer.
- Fix / verification: Build `136` / commit `6d3446228d5125efda31b27120b6e86b52bd952e` keeps existing credentials valid until explicit rotation/removal and removes the deadline from normal CLI copy. Release run `30301756447` passed; the redacted post-deploy matrix returned `200` for admin, MCP and relay, and live OAuth refresh plus widget resource acceptance passed.
- Next action: Reauthorize the existing GPTADMIN Codex connection once, because its pre-fix session has no refresh token to rotate.

## 2026-07-24 - UPDATE-HEALTH-IGNORED-20260724 - Failed update health did not abort - fixed

- Component: `cli.py:cmd_update` in-place update flow.
- First observed: 2026-07-24, immutable RED evidence from the new transactional update test and inspection of the `wait_local_hub_health` call site.
- Confirmed fact: The update called `wait_local_hub_health` but ignored its false result, so a failed Hub health gate did not abort the update or restore the previous runtime.
- Root cause: Health was treated as an informational warning instead of the canary acceptance gate; package replacement had no transaction snapshot.
- Fix / verification: Added a private runtime snapshot/restore transaction, restored services after failure, and made a false Hub health result raise and trigger rollback. `tests/test_update_semantics.py` passes `10` tests.
- Status: fixed.
- Next action: Prove the same transaction with a clean-host update and real client reconnection before closing S3.5.

## 2026-07-24 - SETUP-HEALTH-IGNORED-20260724 - Failed setup health did not abort - fixed

- Component: `cli.py:setup_interactive` Hub bootstrap.
- First observed: 2026-07-24, source inspection after the update health-gate fix.
- Confirmed fact: Setup starts the Hub and calls `wait_local_hub_health`, but ignores a false result and continues to report a completed installation.
- Root-cause hypothesis: Install and update paths evolved separately and only the update path treats health as an acceptance gate.
- Fix / verification: Added `_require_local_hub_health()` and wired setup through the fail-closed helper; focused setup semantics tests pass and the installer contract remains green.
- Status: fixed.
- Next action: Retain the setup health gate in the from-scratch installer acceptance run.

## 2026-07-24 - PROFILE-INSTRUCTION-IGNORED-20260724 - Profile instruction reference was not applied - fixed

- Component: `go-hub/internal/hub` access profiles and MCP initialization.
- First observed: 2026-07-24, RED regression `TestNamedInstructionSetCRUDAndProfileInitialize`; the named instruction-set PUT returned 404 and profile-bound initialize had no runtime selection path.
- Confirmed fact: Profiles persisted `instruction_set_id`, but only the default instruction document existed and Hub initialize/resource responses always used the global default text.
- Root cause: The profile schema was added ahead of named instruction-set CRUD; normalization rejected every non-default ID and request dispatch ignored the profile context.
- Fix / verification: Added authenticated persistent named instruction-set CRUD with atomic `0600` state, profile reference validation, request-scoped initialize/resource selection and restart coverage. Focused named-profile tests and full Hub package pass.
- Status: fixed.
- Next action: Retain the named profile flow in the profiles acceptance matrix and public API documentation.

## 2026-07-24 - MCP-EXEC-NIL-REQUEST-20260724 - Canonical execute crashed without request context - fixed

- Component: `go-hub/internal/hub/server.go` Apps SDK `execute` dispatch.
- First observed: 2026-07-24, RED regression `TestMCPIntegrationDiscoverSchemaExecuteConformance`; the `/mcp` execute path panicked in `origin()` after passing a nil request to `appsSDKCallMCP`.
- Confirmed fact: `discover` and `schema` worked, but the canonical `discover -> schema -> execute` flow could crash when executing the safe Hub `demo` tool.
- Root cause: `appsSDKCallForRequest` routed `execute` through the legacy request-free helper, while the Hub demo result requires request origin/access context.
- Fix / verification: Route `execute` through the request-scoped executor and make request-free internal origin calculation loopback-safe. The conformance test and full Hub package pass.
- Status: fixed.
- Next action: Keep the conformance regression in the MCP forwarding/completion matrix.

## 2026-07-24 - MFA-PASSKEY-UI-MISSING-20260724 - Admin SPA could not enroll a passkey - fixed

- Component: `public/admin/index.html` and `public/admin/app.js` security view.
- First observed: 2026-07-24, read-only security audit and RED regression `test_admin_security_ui_offers_passkey_enrollment_without_raw_credentials`.
- Confirmed fact: Hub WebAuthn registration endpoints existed, but the shipped admin SPA exposed only TOTP enrollment; operators could not create the first passkey from the production admin surface.
- Root cause: Browser login support was added without wiring the authenticated enrollment ceremony into the admin dashboard.
- Fix / verification: Added browser-native passkey enrollment with base64url conversion, typed begin/finish requests and secret-free result rendering. `tests/test_admin_ui.py` passes 8 tests and `node --check public/admin/app.js` passes.
- Status: fixed.
- Next action: Retain a virtual-authenticator browser smoke in the security acceptance lane.

## 2026-07-24 - CLEAN-CLONE-FAILOVER-MODE-20260724 - Failover drill fixtures were not executable - fixed

- Component: `tests/e2e/failover/run.sh` and `tests/e2e/failover/fake-frpc` tracked file modes.
- First observed: 2026-07-24, clean-clone audit in `/tmp/gptadmin-clean-audit-hYNrLP`; 32 focused tests and Go gates passed, then the documented direct script invocation returned exit `126`.
- Confirmed fact: Git mode was `100644` for both the drill and its directly launched FRP fixture, so a fresh clone could not execute the failover path as documented.
- Fix / verification: Restored both executable bits, added mode regressions, and ran all seven black-box scenarios successfully.
- Status: fixed.
- Next action: Keep script mode in the repository acceptance gate.

## 2026-07-24 - FAILOVER-DRILL-READY-20260724 - Direct failover drill was not self-contained - fixed

- Component: `tests/e2e/failover/run.sh` disposable Hub startup/readiness and port allocation path.
- First observed: 2026-07-24, current-tree direct invocation after restoring executable mode; the script repeatedly received connection refused from `127.0.0.1:9001/healthz` and exited before the failover scenarios.
- Confirmed fact: The script deleted its locally built Hub binary during topology reset and hard-coded ports already owned by unrelated local services.
- Fix / verification: Keep the temporary binary until process teardown, resolve source-local scripts/binaries, support `GPTADMIN_FAILOVER_E2E_PORT_BASE`, and run all seven black-box scenarios successfully.
- Status: fixed.
- Next action: Retain the direct drill in the clean-clone acceptance gate.

## 2026-07-24 - FAILOVER-RECLAIM-AUTH-20260724 - Reclaim helper omitted Hub authentication - fixed

- Component: `scripts/gptadmin_failover_reclaim_push.py` and the signed reclaim endpoint.
- First observed: 2026-07-24, direct black-box failover scenario `scenario_primary_reclaim`.
- Confirmed fact: The fallback Hub rejected the reclaim POST as unauthenticated before validating its signed payload.
- Root cause: The helper used the shared signing secret to create the body signature but did not send the same operator credential as the required bearer authorization.
- Fix / verification: Added a request-header regression, sent the bearer credential without logging it, and ran all seven black-box scenarios successfully.
- Status: fixed.
- Next action: Retain the reclaim request test and black-box scenario in the acceptance gate.

## 2026-07-24 - EXTENSION-SDK-DOCS-20260724 - Extension SDK document was empty - fixed

- Component: `docs/EXTENSION_SDK.md` and S4.4 acceptance.
- First observed: 2026-07-24, read-only S4.4 audit and RED regression `test_extension_sdk_documentation_describes_reference_contract`.
- Confirmed fact: The tracked SDK document had zero bytes although the repository exposed a versioned manifest validator.
- Root cause: The implementation slice created the validator and fixture but never delivered the public reference contract document.
- Fix / verification: Added the manifest/lifecycle/policy contract, a deterministic reference adapter, and a live Hub/generic-relay `discover -> schema -> execute` regression; focused extension tests pass.
- Status: fixed.
- Next action: Retain the local adapter runner while external third-party certification remains separate evidence.

## 2026-07-24 - OAUTH-PKCE-BINDING-20260724 - OAuth authorization codes were not fully bound - fixed

- Component: `go-hub/internal/hub/server.go` OAuth authorize/token handlers and public endpoint documentation.
- First observed: 2026-07-24, Terra-assisted security audit and RED regression `TestCanonicalOAuthEndpointsRequirePKCEAndBindClient`.
- Confirmed fact: Runtime metadata advertised root `/authorize` and `/token` while public docs advertised `/oauth/*`; authorization accepted missing PKCE, and token exchange did not require the original `client_id` and `redirect_uri`.
- Root cause: Legacy compatibility routes were treated as canonical, and the authorization-code record fields were stored but not checked during exchange.
- Fix / verification: Added canonical `/oauth/*` routes with documented root aliases, required PKCE S256 at both authorization entry points, enforced client/redirect binding, and passed focused OAuth tests plus the existing OAuth/MCP tests.
- Status: fixed.
- Next action: Keep the canonical endpoint/PKCE regression in the Hub acceptance matrix and obtain external client OAuth evidence.

## 2026-07-24 - PUBLIC-TOKEN-ONBOARDING-20260724 - Windows installer and adapter docs exposed legacy credentials - fixed

- Component: `deploy/install_win.ps1`, `docs/INSTALL_PATHS.md`, and `docs/ADAPTERS.md`.
- First observed: 2026-07-24, native/client acceptance audit and RED regression `tests/test_install_win.py`.
- Confirmed fact: The Windows installer printed the generated ShellMCP bearer, and public onboarding instructed users to copy internal bearer names into MCP/Action/browser configuration.
- Root cause: Legacy compatibility credentials were treated as user-facing setup inputs after the AdminPassword/OAuth product contract had moved connection setup to `/connect`.
- Fix / verification: Removed normal-output token echo and rewrote public onboarding around the Hub connection/OAuth PKCE flow; focused installer/docs tests pass. Runtime compatibility variables remain internal.
- Status: fixed.
- Next action: Complete the remaining migration on native installers and obtain real Windows/MCP/ChatGPT client evidence.

## 2026-07-24 - CLI-TOKEN-STATUS-20260724 - Connection status exposed internal credential names - fixed

- Component: `cli.py` `gptadmin tokens` status output.
- First observed: 2026-07-24, security migration audit and RED regression `test_tokens_hides_internal_credential_names_in_normal_output`.
- Confirmed fact: Normal status printed internal credential labels and a prefix of configured bearer values.
- Root cause: The legacy token inspection command was not migrated to the one-password product vocabulary.
- Fix / verification: Status now reports only Hub URL and configured/hidden connection state; it never prints internal names or credential prefixes, including with the explicit inspection flag.
- Status: fixed.
- Next action: Keep the output boundary in the CLI and installer acceptance matrix.

## 2026-07-24 - FILE-BACKUP-PROCESS-EVIDENCE-20260724 - File sharing lacked process-level acceptance - fixed

- Component: ShellMCP `file_backup` MCP surface and completion matrix.
- First observed: 2026-07-24, native/client audit found only package-level handler coverage for backup/restore.
- Confirmed fact: A real ShellMCP process contract exercised MCP resources and shell tools but not `file_backup` through HTTP/MCP.
- Root cause: The file-sharing feature had unit/handler tests without a process-level acceptance entry.
- Fix / verification: Added a real process test for backup/list/restore and a bounded temporary artifact root; the focused contract passes.
- Status: fixed.
- Next action: Retain the process check in the file-sharing matrix and obtain native-host file-sharing evidence separately.

## 2026-07-24 - HAOS-IMAGE-PROVENANCE-20260724 - HAOS image workflow lacked image SBOM/provenance gate - fixed

- Component: `.github/workflows/publish-haos-addon.yml` ARM64 image publication.
- First observed: 2026-07-24, release/supply-chain audit.
- Confirmed fact: The workflow checksum-verified the FRP archive but published the assembled image without BuildKit SBOM/provenance metadata or a digest verification step.
- Root cause: Archive release provenance controls were not carried over to the HAOS container path.
- Fix / verification: Added BuildKit SBOM/provenance flags, metadata capture, and pre-artifact-export digest verification; workflow contract test passes.
- Status: fixed.
- Next action: Run a tagged GitHub workflow and retain its immutable image digest/attestation evidence.

## 2026-07-24 - SECURITY-DISPATCH-TARGET-20260724 - Apps SDK inspect could lose profile target context - fixed

- Component: Hub Apps SDK dispatcher and profile policy context.
- First observed: 2026-07-24, independent security audit of request-scoped MCP dispatch.
- Confirmed fact: The request-free `inspect` path could bypass the request profile context and attempt a forbidden shell target.
- Root cause: Apps SDK dispatch routed `inspect` through a legacy helper that accepted no `*http.Request`.
- Fix / verification: Added request-scoped Apps SDK schema/inspect dispatch and profile authorization; `go test ./internal/hub -run 'TestAppsSDKInspectCannotBypassProfileTargetPolicy' -count=1` passes.
- Status: fixed.
- Next action: Retain the request-scoped regression in the Hub security gate.

## 2026-07-24 - SECURITY-SCHEMA-TARGET-20260724 - Remote MCP schema lacked target policy check - fixed

- Component: Hub relay schema/tools-list dispatch.
- First observed: 2026-07-24, independent security audit.
- Confirmed fact: Remote target existence was checked before schema enqueue, but profile `AllowedTargets` was not enforced for the selected target.
- Root cause: Schema/list used registry validation without the common request profile authorization gate.
- Fix / verification: Added the common request-scoped facade authorization before relay schema enqueue; `go test ./internal/hub -run 'TestMCPRelaySchemaCannotBypassProfileTargetPolicy' -count=1` passes.
- Status: fixed.
- Next action: Retain the relay schema policy regression in the Hub security gate.

## 2026-07-24 - SECURITY-RELAY-OWNERSHIP-20260724 - Relay result ownership and duplicate completion were not explicit - fixed

- Component: Hub relay poll/result job lifecycle.
- First observed: 2026-07-24, independent security audit.
- Confirmed fact: The shared relay credential is not agent-specific, so result paths must enforce job owner and immutable completion; those RED cases were absent.
- Root cause: Result handling trusted the URL agent ID and permitted a later result to overwrite an already completed job.
- Fix / verification: Result submission now requires the job owner and rejects terminal jobs before mutation; `go test ./internal/hub -run 'TestMCPRelayResultRequiresJobOwnerAndIsSingleAssignment' -count=1` passes.
- Status: fixed.
- Next action: Retain ownership and single-assignment checks in the relay acceptance gate.

## 2026-07-24 - SECURITY-WEBHOOK-TEMPLATE-20260724 - Webhook shell event values lacked literal-safe substitution - fixed

- Component: Hub webhook shell action templating.
- First observed: 2026-07-24, independent security audit.
- Confirmed fact: External event values were interpolated into shell command text; shell metacharacter behavior was not covered by a regression.
- Root cause: Template rendering treated event values as command fragments rather than data.
- Fix / verification: Shell templates now reference generated environment variables while event values remain in the job environment; `go test ./internal/hub -run '^TestWebhookShellTemplateValuesCannotBecomeShellSource$' -count=1` passes.
- Status: fixed.
- Next action: Retain the metacharacter regression in the webhook security gate.

## 2026-07-24 - SECURITY-FILE-SYMLINK-20260724 - File reads could follow links outside the managed root - fixed

- Component: ShellMCP `/file` and read-only inspection path.
- First observed: 2026-07-24, independent security audit.
- Confirmed fact: String-prefix/canonical checks were separate from the final open/ServeFile operation, so a symlink could point outside the allowed root.
- Root cause: File access validated a path string but did not enforce no-symlink access.
- Fix / verification: `/file` rejects symlink entries and canonical path changes; inspect rejects any symlink path before opening; `go test ./internal/inspect ./internal/server -run 'TestReadFileRejectsSymlinkEscapeAndCredentialDirectories|TestFileEndpointRejectsSymlinkEvenWhenLinkIsInsideSpillRoot' -count=1` passes.
- Status: fixed.
- Next action: Retain both process-level symlink regressions in the ShellMCP security gate.

## 2026-07-24 - SECURITY-READONLY-SCHEMA-20260724 - Request-scoped schema path could bypass readonly remote-target filtering - fixed

- Component: Hub Apps SDK request-scoped schema dispatch.
- First observed: 2026-07-24, post-fix regression review while adding the profile-policy security suite.
- Confirmed fact: The new request-scoped schema branch returned remote target tools for a readonly access claim instead of the established empty list.
- Root cause: The branch returned before the legacy readonly target filter, which intentionally permits only `hub` and `shell:*` schema results.
- Fix / verification: Restored the readonly target filter before request-scoped dispatch and added `TestReadonlyAppsSDKSchemaCannotReachRemoteTargets`; focused Hub security tests pass.
- Status: fixed.
- Next action: Retain the readonly remote-target regression with the profile-policy gate.

## 2026-07-24 - INSTALL-QUICKSTART-INTERNAL-AUTH-20260724 - Installer help exposed legacy credential name - fixed

- Component: `deploy/install.sh` post-install quickstart.
- First observed: 2026-07-24, immutable artifact `trash/logs/setup-e2e-ef31e3d.txt` from the current-head from-scratch Docker installer run.
- Confirmed fact: The completion help tells operators that `gptadmin tokens` shows `CTL_TOKEN`, exposing an internal legacy credential name in normal setup output.
- Root-cause hypothesis: The shell installer quickstart was not updated when the AdminPassword/OAuth product contract replaced user-managed bearer credentials.
- Fix / verification: Replaced the legacy credential wording with AdminPassword/OAuth migration-status language and added `test_install_completion_uses_product_auth_vocabulary`; focused installer tests pass.
- Status: fixed.
- Next action: Keep the from-scratch installer output contract in the completion matrix.

## 2026-07-24 - WEBAUTHN-CEREMONY-EXPIRY-20260724 - Ceremony sessions rejected immediately - fixed

- Component: `go-hub/internal/hub/webauthn.go` WebAuthn registration/login ceremony store.
- First observed: 2026-07-24, immutable browser evidence from the Playwright session `gptadmin-webauthn` and the `go-webauthn` v0.15.0 `SessionData.Expires` contract.
- Confirmed fact: A begin request returns 200 and stores a ceremony, but the matching finish request returns `WebAuthn registration ceremony is missing or expired` immediately.
- Root-cause hypothesis: The Hub checks `time.Now().After(session.Session.Expires)` without allowing the library's zero expiry sentinel, so a valid session is rejected before credential validation.
- Fix / verification: Expiry validation now treats the library's zero expiry as unset; the focused Hub test passes, and Chromium completed registration, passkey verification, and locked-down browser login.
- Status: fixed.
- Next action: Retain the browser-backed ceremony regression evidence in the release acceptance gate.

## 2026-07-24 - POLICY-BOUNDARY-BYPASS-20260724 - Legacy write entrypoints skip central policy - fixed

- Component: `go-hub/internal/hub/server.go` bridge/prompt, bulk execution and
  admin MCP resource routes; `go-hub/internal/hub/webhook_gateway.go` action
  dispatch.
- First observed: 2026-07-24, immutable review ID
  `019f93df-7148-7033-972a-07c17c13955d`.
- Confirmed fact: `mcpPromptCall` and webhook actions can invoke write-capable
  execution with a nil request/profile, while `bulkExec` and admin resource
  routes enqueue work directly; these paths do not consistently pass through
  approval/autonomy policy and the central policy audit boundary.
- Root-cause hypothesis: privileged legacy entrypoints predate the shared
  `executeMCPTool` policy executor and retain direct queue calls.
- Fix / verification: Added RED regressions in
  `go-hub/internal/hub/policy_boundary_test.go`; bridge ingress is read-only,
  webhook writes use explicit approval-mode automation profiles, bulk and
  resource routes use the central executor, and runtime Actions OpenAPI now
  advertises the Network Tunnel paths. Focused policy tests and the full Hub
  suite pass.
- Status: fixed.
- Next action: retain the boundary regressions in the completion matrix and
  repeat them after future legacy-route changes.

## 2026-07-24 - SECRET-INGRESS-CSP-20260724 - Secret input CSP header malformed - fixed

- Component: `go-hub/internal/hub/secret_ingress.go` browser input response.
- First observed: 2026-07-24, immutable RED evidence `TestSecretIngressPageConsumesTokenWithoutReturningSecret` after adding the exact CSP contract.
- Symptom / evidence: The response emitted `style-src 'unsafe-inline` without the closing quote, weakening the intended browser policy syntax.
- Root cause: The CSP literal omitted the closing single quote around the inline-style source expression.
- Fix / verification: Corrected the header and added exact Cache-Control, Referrer-Policy and CSP assertions; focused secret-ingress tests pass.
- Status: fixed.
- Next action: Keep the browser ingress contract in the full Hub and secret-ingress test gates.

## 2026-07-24 - HUB-PUBLIC-ORIGIN-HTTP-20260724 - Security preset accepted external HTTP origin - fixed

- Component: `go-hub/internal/hub/security_settings.go` preset mutation.
- First observed: 2026-07-24, immutable RED test `TestSecurityPresetRejectsExternalHTTPOrigin`.
- Symptom / evidence: `private_access` accepted `PUBLIC_ORIGIN=http://hub.example`, allowing a non-TLS public identity to be selected for a hardened preset.
- Root cause: Preset validation checked only the preset name and MFA enrollment; it did not validate the configured public origin transport.
- Fix / verification: External origins now require HTTPS and reject userinfo; loopback HTTP remains allowed only for internal Hub↔Tunnel transport. Full Hub test/race/vet and completion matrix pass.
- Status: fixed.
- Next action: Prove TLS and external identity behavior at the real Tunnel/deployment boundary.

## 2026-07-24 - HUB-BIND-LOOPBACK-20260724 - Go Hub ignored installer loopback bind - fixed

- Component: `cli.py` Hub service environment and `go-hub/internal/hub.FromEnv`.
- First observed: 2026-07-24, immutable regression evidence `TestFromEnvDefaultsHubToLoopbackAndHonorsHubBind` added before the fix.
- Symptom / evidence: The installer writes `HUB_BIND=127.0.0.1`, but Go Hub only reads `GPTADMIN_HUB_HOST`/`HUB_HOST`; with neither set it builds `:9001`, exposing the service listener beyond the Tunnel boundary.
- Root cause hypothesis: The Go runtime renamed the bind variable without retaining the installer’s canonical `HUB_BIND` compatibility input, and its empty-host default is not fail-closed.
- Fix / verification: `FromEnv` now honors `GPTADMIN_HUB_HOST`, `HUB_HOST`, then installer-compatible `HUB_BIND`, and defaults to `127.0.0.1`. `TestFromEnvDefaultsHubToLoopbackAndHonorsHubBind`, full Hub test/race/vet and the completion matrix pass. HAOS runtime keeps its explicit `GPTADMIN_HUB_HOST` override.
- Status: fixed.
- Next action: Keep public ingress at the Tunnel/HAOS boundary; do not expose the Hub listener directly.

## 2026-07-24 - ADMIN-ENV-SHELL-20260724 - Legacy admin env mutation bypass - fixed

- Component: `public/admin/index.html` and `public/admin/app.js:setEnvVar`.
- First observed: 2026-07-24, immutable evidence ID `admin-ui-security-audit-20260724-01` from the tracked source inspection and the missing typed-endpoint regression in `tests/test_admin_ui.py`.
- Symptom / evidence: The production admin view exposes internal environment key names and constructs a `shell_exec` command containing an operator-supplied value to rewrite `/etc/gptadmin/gptadmin.env`; this bypasses the typed Hub security API and can put sensitive values into command/audit paths.
- Root cause: The legacy UI retained an operator shell-editing fallback after the redacted `/admin/api/security/env` metadata endpoint was introduced.
- Fix / verification: Replaced shell mutation and restart fallback with typed preset, MFA, telemetry, heartbeat and approval controls; removed internal key names from normal UI copy. `python3 -m pytest tests/test_admin_ui.py tests/test_shellmcp_heartbeat_config.py -q` passed (13 tests), `node --check public/admin/app.js` passed, and `go test ./internal/hub -run 'TestSecurityHeartbeatUsesTypedAdminEndpoint' -count=1` passed.
- Next action: None for this bug; retain the full-suite run as the release gate.

## 2026-07-24 - HUB-APPS-SDK-COUNT-20260724 - Apps SDK capability count drift - fixed

- Component: `go-hub/internal/hub/server_test.go:TestAppsSDKMetadataAndWidget`.
- First observed: 2026-07-24, immutable test evidence from `cd go-hub && go test ./...` after the safe readonly `demo` capability was added.
- Symptom / evidence: The runtime advertised 8 Apps SDK tools while the regression asserted the previous count of 7, so the full Hub suite failed even though the new capability was intentional and readonly.
- Root cause: The test encoded a stale aggregate count instead of asserting the capability names and safe metadata contract.
- Fix / verification: Updated the regression to assert the exact eight capability names, including `demo`, while preserving widget metadata checks. `go test ./...`, `go test -race ./...` and `go vet ./...` in `go-hub` all pass; the focused Apps SDK test also passes.
- Next action: None for this bug.

## 2026-07-24 - AUDIT-MCP-DECISION-20260724 - Direct MCP allow missing policy audit - fixed

- Component: Hub `/mcp` Apps SDK `tools/call` path and durable operator audit.
- First observed: 2026-07-24, immutable RED evidence from
  `TestAuditIncidentDrillRecoversDecisionWithoutRawArguments`: the `/mcp`
  allow for `demo` produced no `tool_policy_decision`, while the equivalent
  `/mcp-relay/call` deny did.
- Symptom / evidence: An incident query over `/admin/api/audit` could recover
  the denied relay decision but not the successful direct MCP decision, so the
  audit trail was not transport-complete.
- Root cause: Direct Apps SDK dispatch used a separate call path that returned
  the typed result without passing through `auditToolDecision`.
- Fix / verification: Direct `/mcp` allow and deny decisions now use the same
  digest-only audit helper as relay calls. The incident drill and focused
  direct-MCP audit regressions pass; Hub `go test ./...`, `go test -race ./...`
  and `go vet ./...` plus completion matrix `11 passed` are green.
- Next action: None for this bug.

## 2026-07-24 - DOCKER-SETUP-PROMPT-20260724 - ShellMCP installer E2E input drift - fixed

- Component: `tests/e2e/docker/scenarios/user-public-hub-shellmcp.sh` and the interactive setup contract in `cli.py`.
- First observed: 2026-07-24, immutable evidence ID `docker-shellmcp-e2e-20260724-01` from `docker compose -f tests/e2e/docker/docker-compose.yml up --build --abort-on-container-exit --exit-code-from shellmcp-e2e`.
- Symptom / evidence: The disposable scenario reaches the new “How will ShellMCP connect to the Hub?” prompt and then exits with `EOFError`; scripted stdin is exhausted before setup completes.
- Root cause: The scenario's prompt/answer sequence is stale relative to the current installer flow and does not provide the connection-mode answer.
- Fix / verification: Synchronized the scenario with transport and auto-update prompts, added a deterministic local `/version` health stub, used the checked-in CLI, and reran the compose suite; all user, system/FRP and tunnel-backend scenarios passed with exit code 0.
- Next action: None for this bug.

## 2026-07-24 - DOCKER-SETUP-SECRET-20260724 - Installer E2E prints generated bearer credential - fixed

- Component: Interactive `cli.py` setup completion output.
- First observed: 2026-07-24, immutable evidence ID `docker-shellmcp-e2e-20260724-02` from the repaired disposable installer scenario.
- Symptom / evidence: Setup completion output includes a generated bearer credential in the terminal transcript; the value is intentionally omitted from this register.
- Root cause: The E2E bootstrap downloaded a stale remote CLI that still printed a raw API-key line; the checked-in CLI already uses the AdminPassword/OAuth completion copy.
- Fix / verification: The E2E image now runs the checked-in `cli.py` via a local file URL; the final compose run exited 0 and its completion output contains no raw bearer value.
- Next action: None for this bug.

## 2026-07-23 - ANDROID-LAN-PROXY-FW-20260723 - LAN proxy blocked by host firewall - fixed

- Component: `android-4g-lan-proxy.service` on roomhacker-server-100.
- First observed: 2026-07-23.
- Symptom / evidence: Runtime probe `mac-curl-3126-20260723` stalled connecting to the LAN listener. The service was listening on `192.168.2.100:3126`, and a server-local proxy request completed successfully, while the UFW user rules had no TCP/3126 allow entry and the INPUT policy was deny.
- Root cause: The deployment created the LAN listener but did not install a matching UFW allow rule.
- Fix / verification: Added UFW TCP rules limited to the private LAN ranges and
  made the deployment script install the matching rule idempotently for the
  selected port. An external Windows LAN client returned the same mobile IPv6
  through both SOCKS5 and HTTP CONNECT, while direct egress returned a different
  address; the service remained active after restart.
- Next action: Have the Mac retry the exact command; no code-side blocker remains.

## Entry format

```text
## BUG-ID - short title - <open|in_progress|fixed|wont_fix>
- Component:
- First observed:
- Symptom / evidence:
- Root cause:
- Fix / verification:
- Next action:
```

## 2026-07-23 - WIN-ROOTD-AUTH-20260723 - Windows polling authentication - fixed

- Component: Windows ShellMCP polling agent on BeyondInfinity.
- First observed: 2026-07-23.
- Symptom / evidence: The exact runtime artifact `C:\ProgramData\gptadmin\rootd-25900.log` records queue and heartbeat requests returning HTTP 401. The same artifact records polling mode with the HTTP listener intentionally disabled, so a closed local port is not evidence that polling is disabled.
- Root cause: The legacy launcher hard-coded `ROOTD_TOKEN=srv_secret` instead of loading the Hub credential, and its runtime was not the supported Go ShellMCP package.
- Fix / verification: Replaced the active user-mode runtime with the current Go Windows binary, copied the existing identity, loaded the existing Hub credential without rotation, and verified an authenticated queue poll returns HTTP 200 with an empty job response.
- Next action: Remove or disable the inaccessible legacy system task from an elevated Windows session so it cannot resurrect the old runtime.

## 2026-07-23 - WIN-ROOTD-CERT-20260723 - Windows bundled CA path drift - fixed

- Component: Legacy Windows Python/PyInstaller ShellMCP runtime.
- First observed: 2026-07-23.
- Symptom / evidence: The exact runtime artifact `C:\ProgramData\gptadmin\rootd-25900.log` contains repeated failures referencing the removed temporary bundle path `C:\Temp\_MEI107362\certifi\cacert.pem`.
- Root cause: An old packaged runtime retained a PyInstaller-extracted certifi path instead of using a stable bundled or system CA location.
- Fix / verification: The active user-mode path now uses the Go binary and no longer uses the stale PyInstaller bundle. A fresh current Go log entry exists and the active log set contains zero `401`, `certifi`, or `unauthorized` matches.
- Next action: Keep the legacy artifact isolated until the separate privileged-task cleanup entry is handled.

## 2026-07-23 - WIN-ROOTD-REDIRECT-20260723 - Hub URL redirect drops agent auth - fixed

- Component: Windows ShellMCP Hub URL configuration.
- First observed: 2026-07-23.
- Symptom / evidence: Runtime probe `win-rootd-redirect-20260723` showed the generic configured Hub host redirects to another host; a cross-host redirect can drop the Authorization header and produce 401 responses.
- Root cause: The agent was configured with a generic web host instead of the instance-specific public Hub origin.
- Fix / verification: The active user-mode config now uses the canonical instance origin; an authenticated queue probe returns HTTP 200.
- Next action: Keep installer input explicit for deployments where the generic host redirects; do not infer a private Hub origin in the repository.

## 2026-07-23 - WIN-USER-TASK-20260723 - Standard user Task Scheduler fallback - fixed

- Component: `deploy/install_win.ps1` and `public/install_win.ps1`.
- First observed: 2026-07-23.
- Symptom / evidence: User-mode Task Scheduler registration on BeyondInfinity returned access denied, while the user could execute the agent.
- Root cause: The installer treated user Task Scheduler ACLs as universally available.
- Fix / verification: Added a per-user Startup launcher fallback and kept Task Scheduler as the preferred backend; `python3 -m pytest -q tests/test_install_win.py` passes 3 tests.
- Next action: Validate the fallback installer on a clean non-admin Windows account in CI or the next Windows acceptance run.

## 2026-07-23 - WIN-ROOTD-LEGACY-TASK-20260723 - Privileged legacy task cleanup - in_progress

- Component: Old Windows scheduled task `gptadmin-rootd` under the system install.
- First observed: 2026-07-23.
- Symptom / evidence: `schtasks /query /tn gptadmin-rootd` returns access denied for the authenticated non-admin SSH user; the task and its old ProgramData runtime remain outside that user’s ACL.
- Root cause: The historical system installation was not removed when the supported Go user-mode runtime was installed.
- Fix / verification: The active agent is now the supported Go process and remains alive after the SSH connection closes; full deletion of the stale task requires an elevated Windows session.
- Next action: From an elevated local Windows session, disable/remove `gptadmin-rootd`, then verify one clean post-logon Go ShellMCP process and close this entry.

## 2026-07-23 - FAILOVER-PHYSICAL-INGRESS-20260723 - Physical Hub failover has no active takeover path - fixed

- Component: `server-100` Hub watchdog/FRP path and HAOS standby.
- First observed: 2026-07-23.
- Symptom / evidence: Runtime artifact `failover-runtime-20260723-01` shows `gptadmin-hub-watchdog.timer=bad` on `server-100`, no active failover watchdog/proxy unit, HAOS reachable on `:9001`, and HAOS `:9101` refusing connections. The public route therefore has no verified takeover owner when the primary Hub is stopped.
- Root cause: Physical deployment wiring is incomplete or invalid even though the repository Docker failover harness passes; the HAOS standby is only a local Hub and the public tunnel/proxy promotion path is not active.
- Fix / verification: Added the systemd-free HAOS watchdog/proxy runtime, ARM64 FRP packaging, one valid FRP config/process per endpoint, secret-safe reclaim key fallback, dead-child pipe fix, reclaim cooldown reset, and primary FRP `BindsTo=gptadmin-hub.service`. `7` focused regressions passed; physical drill stopped only the Hub, systemd showed Hub inactive and FRP failed, public fallback `/healthz` and `/version` returned `200` with the standby build, automatic reclaim logged `reclaimed_primary`, and final public responses returned primary build `128` with both primary units active.
- Next action: Keep the second physical fallback host disabled until its own deployment drill is run under S3.4.

## 2026-07-23 - HAOS-SUPERVISOR-JOB-20260723 - Stale unrelated app job blocks failover add-on update - open

- Component: Home Assistant OS Supervisor Job Manager, `local_bezrabotnyi_recovery_haproxy` and `local_gptadmin_hub_standby` app jobs.
- First observed: 2026-07-23.
- Symptom / evidence: Runtime artifact `haos-supervisor-job-20260723-01` shows `addon_restart` for `local_bezrabotnyi_recovery_haproxy` and dependent GPTAdmin update jobs at `progress=0`, `done=false`; Supervisor logs report the recovery HAProxy app exited with code 1. The GPTAdmin standby is stopped while the `1.0.3` update waits.
- Root cause: Hypothesis is a stale/failing recovery HAProxy app job serializing the Supervisor app job group; this is outside the GPTAdmin add-on image but blocks its normal update/start path.
- Fix / verification: GPTAdmin update eventually completed without resetting Job Manager state; add-on `1.0.4` is started and the failover drill passed. The unrelated recovery HAProxy app remains in `error` with a separate missing `mgmt_auth` userlist and certificate-rate-limit errors.
- Next action: Repair `local_bezrabotnyi_recovery_haproxy` in its owning deployment task; it no longer blocks GPTAdmin failover acceptance.

## 2026-07-24 - AUTH-UI-INTERNAL-NAMES-20260724 - Auth pages expose internal credential name - fixed

- Component: Hub `/admin/login` and `/authorize` HTML pages.
- First observed: 2026-07-24, immutable source evidence from
  `go-hub/internal/hub/server.go` and the auth-page regression contract.
- Symptom / evidence: Normal browser-facing copy names `CTL_TOKEN`, exposing an
  internal credential vocabulary even though the one-password product contract
  requires OAuth/AdminPassword/scoped JWT language.
- Root cause: Legacy migration hint was retained in the login and OAuth consent
  templates after the public admin UI was sanitized.
- Fix / verification: Replaced the legacy hints with OAuth/Hub/scoped-JWT
  wording; the auth-page regression now rejects `CTL_TOKEN`, bridge, OAuth
  secret and ShellMCP token names. Focused Hub auth and admin UI boundary tests
  pass; full Hub/contract is the remaining handoff gate.
- Next action: None for this bug; internal migration support remains hidden from
  normal auth-page copy until its documented deadline.

## 2026-07-24 - CLI-PLATFORM-CONSTANT-20260724 - Missing Windows platform constant - fixed

- Component: `cli.py` platform detection and cross-platform service helpers.
- First observed: 2026-07-24, immutable RED evidence from
  `tests/test_doctor_json.py` after adding the service runtime probe: the
  existing `IS_WINDOWS` reference raised `NameError` during doctor execution.
- Symptom / evidence: Windows-specific setup branching and the new doctor
  runtime branch could not evaluate the platform guard.
- Root cause: `IS_MACOS` and `IS_USER_INSTALL` were defined at module scope,
  but `IS_WINDOWS` was referenced without a declaration.
- Fix / verification: Defined the explicit `sys.platform == 'win32'` constant;
  doctor runtime tests `4 passed`, full Python `175 passed, 2 skipped`, and
  Windows Hub contract `1 passed`.
- Next action: None for this bug.

## 2026-07-24 - FAILOVER-E2E-RESTART-20260724 - Failover E2E output looked like a restart - fixed

- Component: `tests/e2e/failover/docker-compose.yml` and
  `tests/e2e/failover/run.sh`.
- First observed: 2026-07-24, immutable Docker command evidence from
  `docker compose -f tests/e2e/failover/docker-compose.yml up --build --abort-on-container-exit --exit-code-from failover-e2e`.
- Symptom / evidence: Unbounded Docker output made expected public-down curl
  errors appear after the success line, and context extraction split the same
  invocation into multiple sections that looked like a second cycle.
- Root cause: Interleaved Docker stdout/stderr plus query-section rendering;
  there was no restart policy and no second container invocation in the
  captured serial command.
- Fix / verification: Serial capture under a unique compose project returned
  `rc=0`, exactly one `ALL FAILOVER BLACK-BOX SCENARIOS PASSED` line and all
  seven scenario lines. No harness change was required.
- Next action: Use captured exit-code/count evidence for future failover runs;
  do not interpret expected public-down curl stderr as a failure.

## 2026-07-23 - HAOS-PUBLIC-FALLBACK-PROXY-20260723 - Forward-proxy probe used the wrong listener contract - wont_fix

- Component: Public HAOS `gptadmin_hub_standby` `1.0.5`, fallback listener `:9101`.
- First observed: 2026-07-23.
- Symptom / evidence: Immutable probe artifact `trash/logs/haos-public-fallback-probe-20260723-01.txt` records Hub `:9001/healthz` and `/version` success, TCP `:9101` open, but an HTTP request routed through `:9101` returned `502`.
- Root cause: The `:9101` listener expects origin-form requests from the FRP/reverse-proxy path, while `curl -x` sent an absolute-form forward-proxy request that the tiny proxy concatenated into an invalid upstream URL.
- Fix / verification: No runtime fix required; artifact `trash/logs/haos-public-fallback-probe-20260723-02.txt` records TCP open and direct origin-form `/healthz` returning `200`.
- Next action: Use the origin-form probe for the physical drill and keep forward-proxy semantics out of the acceptance command.

## 2026-07-23 - HAOS-PUBLIC-CREDENTIAL-SCAN-20260723 - Initial scan counted public values as credentials - wont_fix

- Component: Public HAOS `gptadmin_hub_standby` `1.0.5` persisted/build/output surface.
- First observed: 2026-07-23.
- Symptom / evidence: Artifact `trash/logs/haos-public-credential-scan-20260723-01.txt` recorded one match, but the follow-up identified only public `public_origin` and numeric `hub_port` values in `failover_state.json`.
- Root cause: The initial scanner treated every option value as a credential instead of using the explicit credential-key allowlist.
- Fix / verification: Artifact `trash/logs/haos-public-credential-scan-20260723-02.txt` records zero exact matches for all seven credential keys; protected files remain mode `600` and recent logs contain zero sensitive-keyword lines.
- Next action: Keep the credential-key allowlist in future acceptance probes; no runtime leak remains.

## 2026-07-23 - SERVER100-PRIMARY-BASELINE-20260723 - Primary Hub baseline was already down - fixed

- Component: `roomhacker-server-100` primary Hub/FRP units and listeners.
- First observed: 2026-07-23.
- Symptom / evidence: Immutable artifact `trash/logs/server100-primary-baseline-20260723-01.txt` records `gptadmin-hub.service=inactive`, `gptadmin-tunnel-frpc.service=failed`, watchdog timer `bad`, port `9001` owned by nginx, and port `7000` owned by frps; no `gptadmin_hub` process is present.
- Root cause: Under investigation; likely stale edge ownership after the previous physical drill, with nginx retaining the Hub port while the systemd Hub unit is dead.
- Fix / verification: Restored only the Hub and its dependent primary FRP unit; artifact `trash/logs/server100-primary-baseline-20260723-02.txt` records both units active and public primary build `128` before the drill.
- Next action: Keep the primary units under normal service supervision; the promotion/reclaim drill is complete.

## 2026-07-23 - SIGNED-RECLAIM-PUBLIC-20260723 - Public migration initially blocked signed reclaim - fixed

- Component: Server-100 `gptadmin-failover-reclaim-push` and public HAOS standby `1.0.5` reclaim path.
- First observed: 2026-07-23.
- Symptom / evidence: Immutable artifact `trash/logs/server100-signed-reclaim-20260723-01.txt` records primary Hub/FRP active, reclaim push HTTP `401` with missing authorization header, public `/version` still returning standby build `1.0.5` instead of primary build `128`.
- Root cause: Public FRP was mapped to `9001` instead of the fallback proxy `9101`, the watchdog health check used the public route and could race promotion, and generated standby internal credentials did not share the primary bridge key.
- Fix / verification: Set the instance failover port to `9101`, moved health checking to the direct primary LAN endpoint, preserved only the shared bridge key in protected `/data`, and verified artifacts `trash/logs/server100-signed-reclaim-20260723-02.txt` and `trash/logs/haos-public-drill-20260723-01.txt`.
- Next action: Keep this compatibility contract in the release/runbook before the next public app version.

## 2026-07-24 - SHELLMCP-SPOOL-PERM-20260724 - Installer spill directory alias ignored - fixed

- Component: `go-shellmcp/internal/server.FromEnv` and the Go ShellMCP contract runner.
- First observed: 2026-07-24, immutable evidence ID `completion-matrix-shellmcp-spool-20260724-01` while running `tests/test_completion_matrix.py::test_completion_matrix_commands_execute[endpoints]`.
- Symptom / evidence: The Go contract daemon ignored `SHELLMCP_SPILL_DIR`, fell back to `/tmp/shellmcp-go-spool`, and returned `500`/`returncode=-1` with a permission error when that directory was owned by another user.
- Root cause: `FromEnv` accepted `SHELL_SPOOL_DIR` and `SHELLMCP_SPOOL_DIR` but not the installer-emitted `SHELL_SPILL_DIR`/`SHELLMCP_SPILL_DIR` aliases.
- Fix / verification: Added `TestFromEnvUsesInstallerSpillDirectoryAliases` and made all four installer spellings converge on the configured directory. The focused Go test and `python3 -m pytest tests/test_shellmcp_contract.py -q` both pass (`8 passed`).
- Next action: Keep the alias contract in installer/runtime changes; no code-side blocker remains.

## 2026-07-24 - SBOM-PYTHON-TOMLLIB-20260724 - SBOM tool assumed unavailable stdlib module - fixed

- Component: `tools/generate_sbom.py`.
- First observed: 2026-07-24, immutable evidence ID `sbom-test-20260724-01` from `tests/test_sbom.py`.
- Symptom / evidence: The first deterministic SBOM implementation failed at startup because the repository's supported Python runtime did not provide `tomllib`.
- Root cause: The tool assumed a Python 3.11-only standard-library parser despite the project supporting Python 3.10 environments used by the test runner.
- Fix / verification: Replaced the parser dependency with a bounded manifest parser for the checked-in dependency arrays; `tests/test_sbom.py` passes and output remains byte-for-byte deterministic.
- Next action: Keep the release tool compatible with the oldest supported Python runtime; no code-side blocker remains.

## 2026-07-24 - PUBLIC-AUTH-DOCS-20260724 - Product onboarding docs taught internal credentials - fixed

- Component: public README, Getting Started, Hub, Integrations, ShellMCP and FAQ documentation, CLI setup/help surfaces, and the legacy admin dashboard.
- First observed: 2026-07-24, completion audit against `docs/AUTH_SIMPLIFICATION.md`; RED regression `tests/test_product_auth_language.py` failed for all six product documents.
- Confirmed fact: Normal onboarding copy and the legacy dashboard instructed operators to copy legacy bearer, agent, bridge or signing-secret names; the dashboard also read and mutated environment files through `shell_exec`.
- Root cause: Earlier credential cleanup covered the production SPA and installer but not the public documentation set or legacy dashboard.
- Fix / verification: Replaced credential-copy instructions with AdminPassword, OAuth and managed connection language; removed internal names from setup/tokens CLI help and normal error messages; made the legacy dashboard read only typed security preset state and removed shell-based credential mutation/export. Product/UI regressions pass (`20 passed`).
- Status: fixed.
- Next action: Keep advanced implementation names confined to security/configuration reference docs and test new onboarding pages through the product-language regression.

## 2026-07-24 - BROWSER-EXTENSION-MANUAL-CREDENTIAL-20260724 - Browser bridge required manual credential entry - fixed

- Component: `public/mcp-bridge.user.js` and the Hub OAuth redirect surface.
- First observed: 2026-07-24, product-surface audit; RED regression `tests/test_browser_extension_oauth.py` found the extension stored a bridge key and called `/mcp-prompt` with it.
- Confirmed fact: The browser extension asked users to paste an internal connection credential and sent it as a URL query parameter.
- Root cause: The legacy extension predated the Hub connection page and OAuth PKCE flow.
- Fix / verification: Added same-origin `/connect/callback` OAuth handoff, PKCE client registration/exchange in the extension, and OAuth JSON-RPC calls to `/mcp`; removed manual key storage and legacy prompt endpoints. Hub focused OAuth regression, `pytest -q tests/test_browser_extension_oauth.py` (`2 passed`) and `node --check public/mcp-bridge.user.js` pass.
- Status: fixed.
- Next action: Keep browser extension OAuth callback and message-origin checks in the client acceptance gate.

## 2026-07-24 - LIVE-RUNTIME-INACTIVE-20260724 - Known live Hub and ShellMCP runtimes are unavailable - open

- Component: `roomhacker-server-100` Hub/Tunnel and `roomhacker-server-88` ShellMCP deployment contract.
- First observed: 2026-07-24, read-only SSH smoke; immutable evidence `trash/logs/live-runtime-smoke-20260724.md`, `trash/logs/live-runtime-smoke-20260724-rerun.md`, and canonical-unit rerun `trash/logs/live-runtime-canonical-unit-rerun-20260724.md`.
- Confirmed fact: Server-100 Hub `:9001` health/version remain unreachable with the Hub unit inactive and a stale/unattributed listener; Tunnel is failed with `router config conflict`. Server-88's expected user unit is absent, while the root `shellmcp.service` is active but has no expected listener and repeatedly receives queue-poll `401 unauthorized` responses.
- Root-cause hypothesis: External deployment drift has split the expected unit/artifact contract and invalidated the Hub/Tunnel/ShellMCP credentials or routing configuration; this is not a source-test failure.
- Local guard / verification: `gptadmin doctor` now rejects a canonical ShellMCP unit whose `ExecStart` uses the legacy `rootd-go` or `rootd-go-canary` binary; RED then GREEN coverage is in `tests/test_doctor_json.py`.
- Local guard / verification: `gptadmin doctor` also probes an explicitly configured local Hub bind with `/healthz`, so a stale TCP listener is not reported as a healthy Hub; RED then GREEN coverage is in `tests/test_doctor_json.py`.
- Status: open.
- Next action: In an explicitly authorized deployment session, reconcile the canonical units and supported artifacts/configuration, repair Tunnel router ownership and auth, then rerun authenticated endpoint/proxy/MCP/file/profile smoke.

## 2026-07-24 - SUPPLY-CHAIN-MUTABLE-ACTIONS-20260724 - Release workflows used mutable action tags - fixed

- Component: `.github/workflows/*.yml` release, deployment and website workflows.
- First observed: 2026-07-24, supply-chain audit; RED regression `tests/test_supply_chain_policy.py::test_workflows_pin_third_party_actions_to_immutable_commits` rejected `@vN` action references.
- Confirmed fact: Workflow execution selected moving major-version tags for checkout, language setup, Docker setup, artifact upload and provenance attestation.
- Root cause: The workflow source had version comments but no immutable commit pinning policy.
- Fix / verification: Pinned every third-party action to the verified full commit SHA and added the immutable-reference regression; supply-chain/release focused tests pass (`17 passed`).
- Status: fixed.
- Next action: Refresh pinned SHAs only through a reviewed dependency-update change that preserves the regression.

## 2026-07-24 - FAILOVER-HARNESS-ORPHAN-FRPC-20260724 - Timed-out failover drills left orphan fake FRP processes - fixed

- Component: `tests/e2e/failover/run.sh` and `tests/e2e/failover/fake-frpc`.
- First observed: 2026-07-24, deployment-blueprint verification; immutable process evidence showed multiple `fake-frpc` children reparented to PID 1 after the verification tool timed out.
- Confirmed fact: Re-running the drill can inherit stale `/tmp/gptadmin-failover-e2e` listeners and hang while waiting for an expected route transition.
- Root cause: The harness used fixed default resources and did not propagate a parent identity into fake FRP children, so an externally terminated wrapper could leave listeners behind.
- Fix / verification: Added per-run roots, dynamic free-port selection, process-group cleanup and an `E2E_RUNNER_PID` parent-watch in `fake-frpc`; `tests/test_failover_harness.py` passes, all seven failover scenarios pass, and a post-run process check finds no repository-owned fake FRP orphan.
- Status: fixed.
- Next action: Keep the parent-watch contract whenever the failover runner launches a new long-lived test double.

## 2026-07-24 - LIVE-RUNNER-CONNECTION-FIELD-20260724 - Live runner expected the wrong connection manifest field - fixed

- Component: `tests/e2e/live_acceptance.py`.
- First observed: 2026-07-24, process-level smoke against a real disposable Go Hub; RED evidence `tests/test_live_acceptance.py::test_live_runner_checks_actual_go_hub_process`.
- Confirmed fact: The runner looked for `connect.json.mcp`, while the Hub contract publishes `mcp_endpoint`; the mock test concealed the mismatch.
- Root cause: The new runner's fixture used an invented shorthand instead of the authoritative connection manifest field.
- Fix / verification: Aligned the runner and mock with the authoritative `mcp_endpoint` field; `python3 -m pytest tests/test_live_acceptance.py -q` passes (`2 passed`) against both the in-process surface and a real Go Hub process.
- Status: fixed.
- Next action: Keep the live runner contract synchronized with `connection_page.go` when discovery fields change.

## 2026-07-24 - PROFILE-PROCESS-FIXTURE-OAUTH-20260724 - Profile process contract lacked signing configuration - fixed

- Component: `tests/test_hub_contract.py` process fixture.
- First observed: 2026-07-24, RED regression `test_hub_contract_profile_binding_enforces_mcp_tool_policy`.
- Confirmed fact: The real Hub process returned HTTP 500 from `/admin/api/mcp/issue-token` because its test environment omitted the required OAuth signing configuration.
- Root cause: The existing generic Hub contract fixture covered health and MCP calls but did not provide the deterministic test-only `OAUTH_CLIENT_SECRET` required by managed-token issuance.
- Fix / verification: Added the isolated fixture value; the process profile test passes (`1 passed`) and no production credential or source fallback was introduced.
- Status: fixed.
- Next action: Keep managed-token profile binding in the process acceptance matrix.

## 2026-07-24 - MCP-SCHEMA-DIGEST-HINT-20260724 - Fresh schema digest was rejected by relay execute - fixed

- Component: Hub `schema`/`execute` integration-control contract.
- First observed: 2026-07-24, RED process regression `tests/test_hub_contract.py::test_hub_contract_relay_and_openapi` after adding schema version/digest binding.
- Confirmed fact: A digest copied directly from `/mcp-relay/tools` was rejected with `409 schema_mismatch` by `/mcp-relay/call`.
- Root cause: The schema endpoint hashed policy-filtered tools after adding Action shortcut hints, while execute recomputed the digest from the underlying tool schema without those transport hints.
- Fix / verification: Digest calculation now covers the stable policy-filtered tool schema and adds transport hints afterward; Go conformance and Hub process endpoint regressions pass.
- Status: fixed.
- Next action: Retain schema version/digest freshness checks in the integration and endpoint acceptance matrix.

## 2026-07-24 - MCP-SCHEMA-METADATA-ARG-20260724 - Schema metadata leaked through top-level argument extraction - fixed

- Component: Hub legacy top-level MCP argument extraction.
- First observed: 2026-07-24, RED assertion in `TestMCPIntegrationDiscoverSchemaExecuteConformance` after adding schema-bound execute fields.
- Confirmed fact: When `arguments` was omitted, `schema_version` and `schema_digest_sha256` were copied into the downstream tool argument map.
- Root cause: `toolArgsFromTopLevel` reserved routing and idempotency fields but did not yet know the new schema-control fields.
- Fix / verification: Added both schema fields to the reserved set; the conformance regression passes and metadata is absent from the downstream unsupported-tool response.
- Status: fixed.
- Next action: Keep schema-control metadata out of tool arguments in the MCP security regression.

## 2026-07-24 - DEPLOYMENT-RUNTIME-PORT-20260724 - Runtime probe hard-coded the default Hub port - fixed

- Component: `tests/e2e/deployment_runtime.py` Hub parser.
- First observed: 2026-07-24, RED regression `test_hub_probe_accepts_a_configured_non_default_port`.
- Confirmed fact: A healthy probe line for configured port `9101` was reported as failed because parsing looked only for `port:9001`.
- Root cause: The parser encoded the development default instead of evaluating the single reported port observation emitted by the remote script.
- Fix / verification: Hub parsing now accepts the reported configured port while still requiring exactly one HTTP `200`; focused runner tests pass (`4 passed`).
- Status: fixed.
- Next action: Preserve non-default port coverage in the deployment runtime matrix.

## 2026-07-24 - WEBUI-LOGIN-OUTAGE-20260724 - Primary WebUI origin returns 502 - fixed

- Component: Primary public Hub origin and WebUI login path.
- First observed: 2026-07-24, user report that WebUI login is unavailable; immutable evidence `trash/logs/webui-login-probe-20260724.md`.
- Confirmed fact: `https://gptadminmcp.bezrabotnyi.com` returns nginx HTTP `502` for `/admin/login`, `/healthz`, OAuth discovery and `/connect.json`. A personal Tunnel returns an old `1.0.5` Hub with `/connect.json` `404` and is not a current-build replacement.
- Root-cause hypothesis: Primary origin has no healthy current Hub backend, consistent with the server-100 Hub inactive and Tunnel router conflict evidence.
- Fix / verification: Immutable evidence `trash/logs/webui-login-repair-20260724.md` records a private server-100 rollback artifact, one Hub start, direct/LAN/public HTTP 200 responses for the login surface, and a real browser snapshot showing the password field and `Войти` button. No password was entered or submitted.
- Status: fixed for primary-origin availability and the unauthenticated browser login surface; authenticated acceptance remains pending.
- Next action: Sign in manually with the existing AdminPassword, then run the authenticated live acceptance runner; keep the separate Tunnel/ShellMCP runtime drift under `LIVE-RUNTIME-INACTIVE-20260724`.

## 2026-07-24 - DEPLOYMENT-PROBE-HISTORICAL-JOURNAL-20260724 - Runtime probe reports stale Tunnel conflicts - fixed

- Component: `tests/e2e/deployment_runtime.py` Hub probe.
- First observed: 2026-07-24, immediately after the authorized server-100 Hub start; immutable evidence `trash/logs/webui-login-repair-20260724.md` records the Hub as healthy while the fresh probe reported `tunnel_router_conflict`.
- Confirmed fact: The probe searches the last 200 Tunnel journal lines without anchoring them to the current Tunnel process start, so an old conflict can fail an otherwise healthy Hub check.
- Root-cause hypothesis: The fixed-size journal tail was added as a convenient failure detector but has no temporal boundary.
- Fix / verification: Added the RED regression `test_hub_probe_anchors_tunnel_conflict_to_current_service_start`, anchored the remote journal query to `ExecMainStartTimestamp`, and reran the real server-100 probe; it now passes with Hub active/running, port 9001 HTTP 200, and `router_conflict=false`.
- Status: fixed.
- Next action: Preserve the start-anchored probe in the completion-matrix acceptance run.

## 2026-07-24 - DEPLOYMENT-PROBE-TUNNEL-STATE-20260724 - Hub probe can pass with Tunnel failed - fixed

- Component: `tests/e2e/deployment_runtime.py` Hub probe.
- First observed: 2026-07-24, immediately after the primary Hub recovery; the real probe returned `status=passed` while `gptadmin-tunnel-frpc.service` remained failed and intentionally untouched.
- Confirmed fact: The Hub probe checks Hub health and conflict text but does not include the Tunnel unit state in its readiness decision.
- Root-cause hypothesis: The probe contract treated Tunnel conflict text as the only proxy signal, so a failed Tunnel with no current conflict was indistinguishable from a healthy one.
- Fix / verification: Added the RED regression `test_hub_probe_rejects_failed_tunnel_even_when_hub_is_healthy`, made the probe include Tunnel unit state, and reran server-100; it now fails truthfully with `tunnel_service_not_running` while Hub remains active and port 9001 returns HTTP 200.
- Status: fixed.
- Next action: Preserve the explicit Tunnel-state gate in the completion-matrix acceptance run; repair the external Tunnel separately under the approved reclaim safety sequence.

## 2026-07-24 - PERSONAL-TUNNEL-ROUTE-20260724 - Personal Tunnel has no Hub route - fixed

- Component: Personal Tunnel edge and server-100 `gptadmin-tunnel-frpc.service`.
- First observed: 2026-07-24, user report for `/admin/`; immutable evidence `trash/logs/personal-tunnel-route-probe-20260724.md`.
- Confirmed fact: Browser and HTTP probes receive the FRP 404 page for `/admin/`; all tested Hub paths return 404 across the hostname's observed DNS addresses. Server-100 Hub is healthy, while the canonical Tunnel service is failed.
- Root-cause hypothesis: The personal Tunnel route is absent because the canonical FRP client is not running; the edge response is therefore not reaching Hub.
- Fix / verification: After private backup and HAOS reclaim checks, started the canonical Tunnel exactly once. Immutable evidence `trash/logs/personal-tunnel-route-repair-20260724.md` records three successful proxy registrations, zero conflicts/failures, all observed personal-edge addresses returning HTTP 200, and build `128` / commit `fdca78d` on both origins.
- Status: fixed for the personal Tunnel route and unauthenticated WebUI availability; authenticated acceptance remains pending.
- Next action: Refresh the supplied `/admin/` page and sign in manually with the existing AdminPassword, then run the authenticated live acceptance runner.

## 2026-07-25 - ADMIN-LOGIN-SESSION-LOOP-20260725 - Admin password appears not to persist in browser - fixed

- Component: Browser admin session cookie and the HTTPS/HTTP proxy boundary.
- First observed: 2026-07-25, user report that entering the password returns to the password form; immutable evidence `trash/logs/admin-login-session-loop-20260725.md`.
- Confirmed facts: The real deployed password flow sets a `Secure` session cookie. A direct HTTP client reproduces the loop because it cannot send that cookie over HTTP; the same flow through the HTTPS personal Tunnel reaches `/admin/` without the login form and `/admin/api/overview` with HTTP 200.
- Root-cause hypothesis: A stale cookie with the same name but a different Domain/Path can precede the newly issued valid session cookie; `r.Cookie` previously validated only that first value. The direct HTTP Secure-cookie loop is a separate expected scheme boundary.
- Fix / verification: Added a process-level black-box regression with `stale.invalid; gptadmin_admin_session=<valid>`; it failed before the change and passes after `adminSessionValid` accepts any valid signed cookie with the session name. The clean HTTPS live flow also passes through `/admin/` and `/admin/api/overview`.
- Fix / verification: Deployed commit `3c4baf4` after private backup. Immutable evidence `trash/logs/admin-login-session-repair-20250725.md` records RED→GREEN black-box coverage and the real HTTPS duplicate-cookie flow passing `/admin/` and `/admin/api/overview`.
- Status: fixed.
- Next action: Refresh the browser once and sign in again; if an old page remains cached, clear site data for this origin and retry. Then continue authenticated live acceptance.

## 2026-07-25 - ADMIN-LEGACY-CSS-PATH-20260725 - Legacy admin console lost styles - in_progress

- Component: `public/admin/index.html` asset references and `tools/build.sh` legacy static packaging.
- First observed: 2026-07-25, immutable browser screenshot from the supplied `/admin/legacy/` URL; live unauthenticated probe also confirmed the Hub route is reachable.
- Confirmed facts: The release builder copies `public/admin` to `public/admin-legacy`, but the copied HTML requests `/admin/style.css` and `/admin/app.js`; after the React cutover those URLs target the primary `/admin/` payload rather than the legacy directory.
- Root-cause hypothesis: Absolute legacy asset URLs resolve outside the copied `/admin/legacy/` directory, so the browser renders the HTML with default styles.
- Fix / verification: Added the RED Python regression, changed both static references to document-relative URLs, and extended the Go static-handler regression to assert `/admin/legacy/style.css` returns CSS with `text/css; charset=utf-8`; focused and full suites are green.
- Fix / verification: Deployed only the corrected `index.html` atomically to server-100; the target hash matches the tested source, the previous file is backed up, Hub stayed active with the same PID, and the unauthenticated CSS request still returns the login gate.
- Status: fixed in the live primary static payload; authenticated visual verification remains pending.
- Next action: Hard-refresh the supplied browser tab and confirm the styled console; if stale, clear only this origin's cached site data.
