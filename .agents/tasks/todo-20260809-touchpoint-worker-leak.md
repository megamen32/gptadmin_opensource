# Touchpoint worker leak

Status: todo

Symptom: `touchpoint/diagnostics`, `apps`, and `windows` return `Transport closed`. The parent Codex app-server has spawned many identical `/home/admin/.local/share/touchpoint-mcp/bin/python ... touchpoint-mcp` workers (observed 19+ PIDs) instead of maintaining one healthy transport.

Smallest evidence: targeted TERM of the original worker PID 542365 immediately left many same-command workers under parent PID 163039; browser-control MCP did not become callable.

Blocker: real ChatGPT browser acceptance cannot proceed through this surface until worker lifecycle/transport ownership is repaired. Do not kill broad process patterns or alter user browser profiles as part of the Custom GPT acceptance task.
