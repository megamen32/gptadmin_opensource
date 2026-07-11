# CLAUDE.md — gptadmin

## What this is
Self-hosted MCP hub. Two Go binaries + one Python CLI + vanilla-JS admin UI.
- `go-hub/` — hub/proxy (stores metadata, auth, routes MCP calls). `BuildVersion` via ldflags.
- `go-shellmcp/` — shell execution agent (parity port of the old Python `services/shellmcp.py`, deleted in PR #22).
- `cli.py` — single-file (~3900 lines) Python installer + CLI (`gptadmin setup/update/auto-update/...`). Platform-aware: systemd on Linux, launchd on macOS.
- `public/admin/` — vanilla JS SPA (no framework). `app.js` `renderAll()` reads `/admin/api/overview`.
- `tools/build.sh` — build/release: bumps VERSION, Go ldflags inject version, packages tarballs.

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

## How binaries actually reach users
`became.bezrabotnyi.com/gptadmin*.tar.gz` (what `install.sh` and `cli.py` fetch) is served by `server_for_installer.py` running **on the server**, which reads from a **local `build/`** produced by running `bash tools/build.sh` there (not from the public repo, not from GitHub). So `gptadmin_opensource/binaries/` in git is currently unused dead weight.

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
- `AGENTS.md` carries the same context for non-Claude agents (Codex, etc.) — keep it in sync when architecture changes.

## Style
- Go: follow existing `internal/hub` / `internal/server` patterns.
- Python: f-strings, explicit logging, match surrounding code.
- Admin UI: no build step, no framework — edit `app.js`/`index.html`/`style.css` directly.
