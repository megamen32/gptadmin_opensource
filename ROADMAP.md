# Roadmap

Priority order: top first.

## P0 — Restore public GPTAdmin OAuth and Apps SDK widget

Status: complete; v140 receipt and live UI acceptance deployed

- [x] P0.1 Update the public primary and HAOS standby to tested build 140; prove public OAuth metadata and authorization routing without 502.
- [x] P0.2 Live authenticated GPTADMIN plugin proved a server-side canonical resource-read receipt for `ui://widget/admin-v3.html`, plus a separate live UI render. This is not a captured literal ChatGPT protocol transcript.

## M1 — AI-first administration and restart recovery

Status: incremental implementation; see docs/ACCESS_OPERATIONS.md

- [x] Owner API tools for profiles, named connections and durable access history.
- [x] Preserve profile fields in the UI; effective read-only permission; retire the unserved duplicate dashboard.
- [x] Paired production upgrade to acb80d0 / build 193; independent systemd supervisor, compatible fallback 835b240, live connector and both process versions verified.
- [ ] Activate the explicitly selected AI connection's admin role: the platform rejected the owner role-assignment call before execution; owner UI action remains pending.
- [x] Explicit delegated admin roles, shared token/OAuth state and owner retrieval of stored token values (source and isolated-process/browser proof).
- [ ] Persist queued execution inputs and define executor delivery acknowledgements.
- [ ] Measure and narrow remaining coarse locks and other snapshot stores.

## Proposed

<!-- Add new requests here before work starts. Name priority tradeoff. -->

- [ ] Health Incident Autopilot: monitor current hosts, failed services, and
  selected error/keyword log signals; route one deduplicated incident through
  NoticePlace → GPTAdmin → Agent Herder → OpenCode/Hermes/OmniRoute, require
  explicit plan choice, supervise progress, and prove a resolved Telegram/
  NoticePlace trace. Research and three implementation plans are recorded in
  `docs/health-incident-autopilot-current-state-2026-08-09.md` and
  `docs/health-incident-autopilot-plans-2026-08-09.md`; implementation awaits
  plan selection and technical-preview approval.

- [ ] Windows/Android ShellMCP autostart everywhere: Windows is reboot-proven;
  the S21 exact-serial maintainer is installed and safely waiting for physical
  reconnect before its authorized reboot/Hub/relay acceptance.
- [ ] Agent-resume reliability: add a per-session exclusive resume lease and
  validate duration/job creation before creating state directories, preventing
  duplicate Codex writers and orphaned failed-job directories.

- [x] S21 Android debug/remote-control phone: completed after P0.
  Add private authenticated Android Remote Control MCP access without a public
  tunnel through the phone's existing outbound ShellMCP long-poll connection;
  Android MCP is a localhost child on S21 and USB is bootstrap/diagnostics only,
  never runtime transport. Preserve full debug privileges and add real E2E
  canaries. Keep this separate from Notify Center cellular-call fallback.
- [ ] Fleet MCP registry cleanup and local PostgresMCP: retire explicitly obsolete
  ChromeDevTools/OpenMemory/AgentMonitor targets, migrate Mac Chrome to BrowserOS,
  and materialize PostgresMCP for OpenCode/ZCode/Codex/Claude. Normal plan is
  selected and remains queued after the newly prioritized S21 goal.
- [ ] Notify-controlled agent jobs: Normal architecture selected by the user.
  Agent Herder `create_session`/`new_or_resume` and the server-100 runtime slice
  are complete for OpenCode and Codex. Next is the allowlisted
  Notify→GPTAdmin→ShellMCP:100 routing slice; Mac follows afterward. This stays
  separate from the S21 debug/remote-control goal and its cellular-call fallback.
- [x] Make the S21 Android 4G proxy the canonical egress path: installed a
  boot-persistent, cellular-bound Android bridge with server-100-only LAN
  ingress; verified 4G egress plus boot recovery.
