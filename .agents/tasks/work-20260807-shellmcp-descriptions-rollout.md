# Build, verify, commit, and rollout ShellMCP descriptions

## Original request

После билда сначала заcommitить, прогнать все тесты/blackbox/Custom GPT, затем при успехе раскатать и push.

## Objective

Проверить и доставить commit `9c5848e` с понятными descriptions ShellMCP child-MCP tools и сохранённым macOS PATH fix.

## Acceptance canary

Full Go tests, blackbox, Python contracts, public Custom GPT live acceptance, затем fleet rollout и post-rollout canary.

## Evidence

- `go-hub go test ./...`: passed.
- `go-shellmcp go test ./...`: passed, including blackbox.
- Python MCP/service contract tests: 20 passed.
- Commit created: `9c5848e`.
- Initial Custom GPT 401 root cause: the installed server-100 CLI was older and issued JWTs without `resource` and `kid`; Hub secret/origin configuration matched.
- Current-source token probe: local Hub HTTP 200.
- Custom GPT live acceptance before rollout: passed (`health`, `version`, `connection`, `oauth`, `openapi`, `mcp`; `tool_count=16`).
- Build: `tools/build.sh cli hub shellmcp` passed; artifacts generated for commit `9c5848e`.
- Post-rollout server-100: Hub HTTP 200, build version 142, git commit `9c5848e`; installed CLI now issues accepted JWTs.
- Post-rollout Custom GPT live acceptance: passed (`health`, `version`, `connection`, `oauth`, `openapi`, `mcp`; `tool_count=11`).
- Post-rollout Mac mini: Darwin amd64 Go ShellMCP installed and restarted; BrowserClaw `mcp_tools` returned HTTP 200 with tools.
- Post-rollout Mac ShellMCP schema: `mcp_manage`, `mcp_tools`, and `mcp_call` descriptions match the intended host-local topology wording.

## Status

Commit exists. Tests, build, server-100 rollout, Mac mini rollout, live canaries, and push to `origin/main` are green.
