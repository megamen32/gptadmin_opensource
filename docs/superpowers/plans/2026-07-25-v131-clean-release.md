# v131 clean release

**Goal:** ship an installable release after local gates, CI repair, and production browser-to-MCP proof.

1. Add a per-commit background gate runner that writes a namespaced immutable result under `trash/logs/` and never pushes on failure.
2. Reproduce each v130 CI-only Python failure in a clean environment; fix the test/product contract with RED/GREEN evidence.
3. Finish and review the macOS-safe ShellMCP `/file` repair; require the focused regression and full package suite.
4. Integrate reviewed slices, bump only to v131, run release gates once, push, tag, watch CI, install the release artifact, and run the strict public flow.

**Protected dirty files:** `.vscode/settings.json`, `AGENTS.md`, `CLAUDE.md`, `docs/BUGS.md`, `docs/WORKLOG.md`, existing untracked plans; never stage them without explicit user direction.
