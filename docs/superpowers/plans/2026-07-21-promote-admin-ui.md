# Promote React Admin UI Implementation Plan

**Goal:** Make the React/Vite admin console the live `/admin/` surface and verify its profile, instruction, client-binding and MCP-tool controls.

**Architecture:** Build `admin-ui/dist` as a static artifact, then publish it into the Hub's effective `/opt/gptadmin/public/admin` path only after the existing React tests/build and browser/API parity checks pass. Keep the old vanilla UI as a rollback backup, restart the Hub, and verify the live HTML and authenticated API routes.

**Global constraints:**

- Do not expose tokens or private workspace contents.
- Admin UI auth uses the existing same-origin admin session; do not add browser token storage.
- Profile writes use ETag/If-Match and must survive Hub restart.
- Startup instructions remain bounded to 16 KiB and are operational guidance, not an authorization boundary.
- The production switch must be reversible from a local backup.

## Tasks

1. Run React tests, lint and build; inspect the generated `dist` contract.
2. Add/adjust parity tests for live static entrypoint and required navigation/API markers.
3. Back up the effective live `public/admin`, publish `admin-ui/dist`, restart Hub.
4. Verify live `/admin/` HTML, profile CRUD/binding, instruction CAS update, and MCP `tools/list`/`tools/call` using an authenticated session.
5. Record deployment evidence in `docs/WORKLOG.md`, commit and push one integrated change.
