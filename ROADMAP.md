# Roadmap

Priority order: top first.

## P0 — Restore public GPTAdmin OAuth and Apps SDK widget

Status: complete; v140 receipt and live UI acceptance deployed

- [x] P0.1 Update the public primary and HAOS standby to tested build 140; prove public OAuth metadata and authorization routing without 502.
- [x] P0.2 Live authenticated GPTADMIN plugin proved a server-side canonical resource-read receipt for `ui://widget/admin-v3.html`, plus a separate live UI render. This is not a captured literal ChatGPT protocol transcript.

## M1 — [user-visible outcome]

Status: planned

- [ ] M1.1 [stable deliverable]

## Proposed

<!-- Add new requests here before work starts. Name priority tradeoff. -->

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
