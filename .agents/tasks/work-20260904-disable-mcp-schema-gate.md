# Disable MCP schema gate

Started at 2026-09-04T01:55:23+03:00 (manual clock)
Estimate: minimum 8 / maximum 20 active minutes.

## Minimal path

- Result: `shell_exec` relay calls work without `schema_version` or schema digest.
- Canary: a Fast-Agent CLI run invokes `shell_exec` through the configured GPTAdmin MCP with only its normal arguments.
- Slice: remove execute-time schema/digest admission checks and change their regression to assert metadata-free execution.
- Discarded: schema metadata removal, unrelated relay authentication/policy changes, deployment rollout.

Status: complete locally. Both relay execution paths ignore schema metadata; focused Go and process regressions pass. Fast-Agent native status command passed. Deployment deliberately not performed.

## Live rollout

Started at 2026-09-04T02:04:39+03:00 (manual clock)
Estimate: minimum 8 / maximum 20 active minutes.

- Result: live Hub stops rejecting `shell_exec` because of schema metadata.
- Canary: Fast-Agent CLI invokes the live GPTAdmin MCP `shell_exec` without schema fields.
- Slice: build a clean `HEAD` snapshot with only the gate removal, back up `/opt/gptadmin/bin/gptadmin_hub`, atomically replace it and restart its service.
- Discarded: deploying unrelated dirty checkout changes, changing auth/policy, broad release publication.

Status: deployed. The prior binary is `/opt/gptadmin/backups/mcp-schema-gate-20260904-0207/gptadmin_hub.before`; `gptadmin-hub.service` is active. A stale-digest REST relay call and Fast-Agent CLI MCP call both ran `shell_exec hostname` successfully on `admin-server-100` without schema fields.
