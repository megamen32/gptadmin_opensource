# Schema validation opt-in

Status: complete — committed, rebased, pushed, and deployed on server-100

Started at 2026-09-04T16:03:30+03:00 (manual clock)
Estimate: minimum 60 active minutes; maximum 180 active minutes.

## Minimal path

1. **Result:** schema version/digest admission is an explicit opt-in and disabled by default.
2. **Canary:** a stale-metadata relay call completes with the default configuration, while the same call is rejected only with `GPTADMIN_SCHEMA_CONTRACT_VALIDATION=true`.
3. **Slice:** gate the existing validation at both Actions and MCP call paths with one boolean configuration field and focused regression tests.

Discarded: removing schema discovery metadata, relay redesign, authentication changes, and unrelated cleanup.

## Unified-history release route

- User requested a self-review of the complete intended GPTAdmin change set, one linear history, and push.
- `main` is 3 commits ahead and 59 commits behind `origin/main`; the safe route is review/test the current intended sources, commit coherent changes, rebase on `origin/main`, repeat affected checks, then push.
- Temporary builds, clones, dependency caches, and test outputs under `.tmp/` are not source artifacts. The repository must ignore that directory rather than attempting to publish it.
- Full checks now pass: Hub and ShellMCP Go suites; Admin UI tests/lint/build; root docs and Hub-process contracts; website production build; and GrepMesh Rust tests. The docs public mirror was regenerated from its canonical sources. A GrepMesh black-box test was isolated from host runtime settings, which made the full suite deterministic.
- Unified history: `3d820f5` is rebased on current `origin/main` and pushed. Deployment rebuilt the Hub from that SHA, backed up the prior binary as `/opt/gptadmin/backups/gptadmin_hub.before-3d820f5-20260904`, atomically replaced `/opt/gptadmin/bin/gptadmin_hub`, and restarted `gptadmin-hub.service`.
- Final public canary: schema discovery still returned `gptadmin.mcp-schema/v1`; relay `hub_status` completed both with no schema metadata and with a deliberately stale digest; metadata-free `shell_exec hostname` on `shell:admin-server-100` completed. `GPTADMIN_SCHEMA_CONTRACT_VALIDATION` is absent, so validation is disabled by default.

## Session-binding follow-up

Started at 2026-09-04T16:26:17+03:00 (manual clock)
Estimate: minimum 20 / maximum 45 active minutes.

- Result: the default ChatGPT Action flow has no schema binding to cache or resend.
- Canary: public `actions/openapi.yaml` contains neither schema field and has `Cache-Control: no-store`; a default live relay call succeeds without schema metadata, while the same metadata is issued and checked only when `GPTADMIN_SCHEMA_CONTRACT_VALIDATION=true`.
- Slice: make metadata emission, Action properties, and admission validation one opt-in feature; retain the enabled contract unchanged.
- Discarded: unrelated relay redesign, auth changes, tool argument changes, or WhatsApp work.

Status: complete — public Action schema has no schema version/digest fields and
is served with `Cache-Control: no-store`. After deployment, public discovery
omitted metadata, a deliberately stale pair completed without `schema_mismatch`,
and public `shell_exec hostname` completed on `shell:admin-server-100`.
