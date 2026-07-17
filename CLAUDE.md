# CLAUDE.md — gptadmin

## What this is
Self-hosted MCP hub. Two Go binaries + one Python CLI + vanilla-JS admin UI.
- `go-hub/` — hub/proxy (stores metadata, auth, routes MCP calls). `BuildVersion` via ldflags.
- `go-shellmcp/` — shell execution agent (parity port of the old Python `services/shellmcp.py`, deleted in PR #22).
- `cli.py` — single-file (~3900 lines) Python installer + CLI (`gptadmin setup/update/auto-update/...`). Platform-aware: systemd on Linux, launchd on macOS.
- `public/admin/` — vanilla JS SPA (no framework). `app.js` `renderAll()` reads `/admin/api/overview`.
- `tools/build.sh` — build/release: bumps VERSION, Go ldflags inject version, packages tarballs.

## Plan and multi-agent work

- The canonical execution plan is [`docs/PROJECT_PLAN.md`](docs/PROJECT_PLAN.md).
  The public `docs/ROADMAP.md` does not replace it.
- The canonical product philosophy is [`docs/PHILOSOPHY.md`](docs/PHILOSOPHY.md).
  New MCP surfaces keep a minimal stable context and lazily load only data the
  current task actually selects.
- The canonical append-only handoff log is [`docs/WORKLOG.md`](docs/WORKLOG.md).
- Before implementing work directly, the orchestrator explicitly asks whether a
  bounded slice can be delegated to a subagent with clear instructions.
  Delegate independent diagnosis, tests or isolated edits; retain integration,
  risky decisions, deployment and acceptance in the primary agent.
- Before substantial work, read both files, select one milestone and create an
  `active` entry using the worklog template. Before finishing, replace it with
  a factual `completed`, `blocked` or `handed-off` entry containing tests,
  commit, CI/deploy evidence and one next action.
- For behavior changes use TDD: record a failing regression test or precise
  pre-fix evidence before implementation, then focused and full verification.
  Do not mark a milestone or stage complete without its listed exit gate.
- Never record tokens, private URLs, customer data or raw logs in the worklog.
- Do not edit files or runtime surfaces owned by another active agent without
  explicit coordination. Keep this section aligned with `AGENTS.md`.
- Product-surface vocabulary is **Hub**, **MCP clients** and **Tunnel**. Do not
  expose `CTL_TOKEN`, FRP/frpc or internal key names in normal setup, status,
  UI or quickstarts. Read `docs/AUTH_SIMPLIFICATION.md` before auth, installer,
  client-connect or documentation work. `AdminPassword` is the only
  user-owned secret; internal JWT/signing/device credentials must stay hidden.

## Commands (copy-paste ready)
```bash
# Go tests (run from each module dir)
cd go-hub && go test ./...
cd go-shellmcp && go test ./...

# Python tests (skip slow e2e)
python3 -m pytest tests/ --ignore=tests/e2e

# Cross-compile check for macOS (we have no Mac in local dev)
cd go-hub && GOOS=darwin GOARCH=arm64 go build ./... && GOOS=darwin GOARCH=amd64 go build ./...

# CLI smoke
python3 cli.py version
python3 cli.py auto-update status

# Full build + release (CI does this; manual is rare)
bash tools/build.sh
```

## Release flow (non-obvious)
- Bump `VERSION` (plain integer) + commit "Release build N" → push `main`.
- `auto-tag.yml` creates `v<N>` tag in the **private** repo → dispatches `release.yml` → GitHub Release (source tarball) in the **private** repo `megamen32/gptadmin`.
- `build-and-sync.yml` builds binaries (`tools/build.sh`) and **commits them into git** of the **public** mirror `megamen32/gptadmin_opensource/binaries/` (needs `OPENSOURCE_PAT`). **Known bad design** — binaries bloat git history; see TODO below.
- macOS CI: `macos-build` job runs Go tests on `macos-latest` (darwin runtime).

## How binaries reach users (canonical)
`install.sh` and `cli.py` fetch packages from **GitHub Releases** on the public mirror:
`https://github.com/megamen32/gptadmin_opensource/releases/latest/download/gptadmin-{platform}-{arch}.tar.gz`
On a `v*` tag, `build-and-sync.yml` mirrors source into the public repo, pushes the tag, and uploads `build/*.tar.gz` as release **assets** (never into git history). The `gptadmin.py` bootstrap script and `FRPC_BASE_URL` still come from the legacy host `became.bezrabotnyi.com` (served by `server_for_installer.py`); binary packages no longer depend on it. Override the package base with `PKG_BASE_URL` / `RELEASES_URL` env.

## Build from source (for contributors / offline / reproducibility)
```bash
git clone <repo> && cd gptadmin
# All 4 platform install bundles (linux/darwin × amd64/arm64) — needs Go only:
bash tools/build.sh platform
# Full release set (Linux bins, platform bundles, component tarballs, win/android):
bash tools/build.sh all
# One-off native build for current host:
cd go-hub && CGO_ENABLED=0 go build -o gptadmin_hub ./cmd/gptadmin-hub
cd go-shellmcp && CGO_ENABLED=0 go build -o shellmcp ./cmd/shellmcp-go
# Verify a built hub's architecture:
tar xzOf build/gptadmin-linux-arm64.tar.gz ./gptadmin_hub/linux_arm64/gptadmin_hub | file -
# Cross-compile check (no Mac needed): 
cd go-hub && GOOS=darwin GOARCH=arm64 go build ./... && GOOS=linux GOARCH=arm64 go build ./...
```
No C toolchain needed (CGO disabled). Cross-builds run on a plain Linux box.

## Gotchas
- **No Mac in local dev.** Darwin launchd/systemd code is cross-compiled on Linux; real-launchd behavior verified by `tests/mac/launchd_verify.py` (skips on Linux, runs on Mac).
- **ARM64 hub**: hub is cross-compiled for linux/arm64 + darwin/* by `build_hub_cross_platforms` in `tools/build.sh`. Do NOT regress to building hub for linux/amd64 only — that ships an amd64 binary to arm64 hosts (Orange Pi etc.).
- `cli.py` is intentionally single-file — don't split into modules.
- Auto-update service unit is **always installed**; the timer is toggled by user preference. macOS uses `launchctl kickstart` (unified trigger), not nohup.
- Read-only MCP clients never receive raw shell access. They use typed
  `system_inspect`; `SHELLMCP_INSPECT_ROOTS` bounds readable paths and
  recognizable credentials are redacted before the MCP response. See
  `docs/READONLY_MODE.md`.
- `AGENTS.md` carries the same context for non-Claude agents (Codex, etc.) — keep it in sync when architecture changes.

## Style
- Go: follow existing `internal/hub` / `internal/server` patterns.
- Python: f-strings, explicit logging, match surrounding code.
- Admin UI: no build step, no framework — edit `app.js`/`index.html`/`style.css` directly.
