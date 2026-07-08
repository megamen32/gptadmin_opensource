# Open-Core Launch Plan — gptadmin

> Living document. Edit freely. Last updated: 2026-06-29.

Vision: make `gptadmin` a **public open-core** repository (AGPL-3.0) to earn
GitHub stars and community trust, while keeping a clear path to monetization
(hosted cloud, enterprise SSO/audit/RBAC, advanced panel, support).

## I. Ideal end state

### A. First impression (30 seconds)
- README with gif/screenshot — hub + 3 adapters, real dialogues
- One-paragraph elevator pitch
- Badges: license (AGPL-3.0), stars, CI status, Python version, "self-hosted"
- Quickstart in 3 lines: `curl install → connect adapter → work`
- Links: Demo (website), Docs, Telegram, Roadmap

### B. Repository structure (clean, obvious)
```
gptadmin/
├── README.md              ← sells vision, not a "deprecated" header
├── LICENSE                ← AGPL-3.0
├── CONTRIBUTING.md
├── SECURITY.md
├── CODE_OF_CONDUCT.md
├── CHANGELOG.md
├── .github/workflows/     ← ci.yml, release.yml
├── hub/                   ← gptadmin_hub + watchdog + installer server
├── shellmcp/              ← go-shellmcp + python client
├── adapters/              ← 3 adapters: openai-action/, mcp-sse/, userscript/
├── cli/                   ← gptadmin CLI
├── panel/                 ← /admin web panel (when ready)
├── deploy/                ← install scripts, systemd, nginx
├── docs/                  ← mkdocs / on website
└── tests/
```

### C. Security & hygiene
- Commit history clean of secrets (tokens, passwords, IPs)
- `.gitignore` without conflict markers
- No binaries/archives in repo (`.tar.gz`, `.zip` belong in releases)
- No private configs (`became.bezrabotnyi.com.conf`)
- `website` submodule either removed or kept public

### D. License & monetization
- **AGPL-3.0** for the main code (protects against cloud competitors)
- README clearly separates: "free forever" (hub, shellmcp, 3 adapters, basic panel) vs "will be paid" (hosted cloud, enterprise SSO/audit, advanced panel, support)
- SECURITY.md with responsible disclosure

### E. CI/CD
- GitHub Actions: lint (ruff/eslint) + pytest + build on every PR
- Automatic releases with changelog
- Status badge in README

---

## II. Audit of current state

### ❌ Critical (blocks publish)
1. **NO LICENSE file** — without it, code is "all rights reserved" by default; nobody can legally use it
2. **README is outdated and counterproductive** — starts with "Deprecated", mentions `rootd` (renamed to shellmcp), no mention of MCP/userscript/3 adapters/free web AIs, no quickstart/screenshots/vision
3. **`.gitignore` is broken** — contains conflict markers `<<<<<<< HEAD` / `=======` / `>>>>>>> headroom-spill-integration` (unresolved merge)
4. **Private configs in repo**: `deploy/nginx/became.bezrabotnyi.com.conf` (real domain/paths) — verify it's not in history
5. **Archives in root**: `gptadmin_refactor_2026-05-11_15-18-03.tar.gz` (44KB), `root_hub_license_refactor.zip` (13KB) — repo junk
6. **Root-level sprawl**: `cli.py`, `go-hub/`, `gptadmin_security.py`, `server_for_installer.py`, `telegram_logs_bot.py`, `mcp-add` — all in root, no structure

### ⚠️ Must verify
7. **Secrets in commit history** — critical to scan `git log --all -p` for `github_pat_`, `gh[po]_`, `sk-`, `token=`, `password=`. If found → `git filter-repo` to rewrite (BEFORE going public), then rotate all tokens
8. **ngrok_url.txt / cloudflare** — may contain public URL with token
9. **website submodule** — points to `adminchatgpt_website` (now public, ok), ensure it's up to date

### ⚙️ Medium
10. CI exists (`.github/workflows/build-and-sync.yml`) — verify it runs tests, not just builds
11. `tests/` exists (`test_rootd.py`, `test_hub.py`) — but no tests for CLI, userscript, MCP-SSE, install scripts
12. No `CONTRIBUTING.md`, `SECURITY.md`, `CODE_OF_CONDUCT.md`
13. `VERSION` file exists (good), but no `CHANGELOG`
14. `.claude/`, `.serena/` dirs — private AI-assistant settings, should not be public
15. `mcp-add` file (617B) no extension — unclear what it is

### ✅ Good
- Code works (used in production)
- Userscript (`mcp-bridge.user.js`) is valid and ready
- 3 adapters conceptually ready
- CI scaffolding exists
- Website is already public and sells the vision correctly

---

## III. Execution plan: current → ideal

### Stage 0 — Secret audit ✅ DONE

**Status:** MCP_AUTH_TOKEN, CTL_TOKEN, ADMIN_PASSWORD scrubbed from git history via git filter-repo. Force-pushed. Tokens must be rotated by maintainer.

### Stage 0 — Secret audit (BLOCKING, before any publish) — ARCHIVED

**0.1** Scan full history:
```bash
git log --all -p | grep -iE "github_pat_|gh[po]sr?_[A-Za-z0-9]{20}|sk-[A-Za-z0-9]{20}|ROOTD_TOKEN=.{8}|CTL_TOKEN=.{8}|password[:=].{6}|bearer\s.{20}|api[_-]?key[:=].{12}"
```

**0.2** If found → clean with `git filter-repo` (NOT filter-branch):
```bash
pip install git-filter-repo
git filter-repo --replace-text secrets.txt   # "old_text==>***REMOVED***"
git filter-repo --invert-paths --path deploy/nginx/became.bezrabotnyi.com.conf
```

**0.3** Force-push rewritten history (ONLY while repo is still private):
```bash
git push --force origin main
```
> NOTE: the user has explicitly forbidden force-push on the website repo because
> multiple contributors work on it. For the gptadmin repo, force-push is acceptable
> ONLY while private AND only after confirming with the user. If unsure, ASK.

**0.4** Rotate ALL tokens that may have leaked: GitHub PAT, ngrok/cloudflare tokens, Bearer CTL_TOKEN, Telegram bot token, SSH keys.

### Stage 1 — Basic hygiene ✅ DONE

**Status:** LICENSE (AGPL-3.0), .gitignore fixed, SECURITY/CONTRIBUTING/COC added, junk removed, pyproject license metadata, CHANGELOG.

### Stage 1 — Basic hygiene (1-2 hours) — ARCHIVED

**1.1** Fix `.gitignore`: remove conflict markers, merge duplicates, add `.claude/`, `.serena/`, `*.tar.gz`, `*.zip`, `ngrok_url.txt`, `*.db`, `.cloudflared/`.

**1.2** Remove from repo (and history via filter-repo if already committed):
- `gptadmin_refactor_2026-05-11_15-18-03.tar.gz`
- `root_hub_license_refactor.zip`
- `.claude/`, `.serena/`
- `deploy/nginx/became.bezrabotnyi.com.conf` (if it has real paths)
- `1.html`

**1.3** Add `LICENSE` (full AGPL-3.0 text).

**1.4** Add `.editorconfig`, update `pyproject.toml` with license metadata.

### Stage 2 — Restructure (2-3 hours)

**2.1** Move root-level files into folders:
```
go-hub/                  → hub/proxy + control plane
server_for_installer.py → hub/install_server.py
cli.py                  → cli/gptadmin.py
gptadmin_security.py    → hub/security.py
telegram_logs_bot.py    → integrations/telegram_logs.py
mcp-bridge.user.js      → adapters/userscript/mcp-bridge.user.js
go-shellmcp/            → shellmcp/go/
client/                 → shellmcp/python/
```

**2.2** Update all import paths in `deploy/install*.sh`, `cli`, systemd units.

**2.3** Decide on `website` submodule: keep (it's public) OR remove and link in README. Recommend **remove submodule** — the website lives separately, it doesn't belong in the product repo.

**2.4** Create `adapters/` structure so README can point at "3 adapters".

### Stage 3 — README and meta-files ✅ DONE

**Status:** New README (vision, architecture, quickstart, 3 adapters, use-cases, license, links). SECURITY/CONTRIBUTING/COC done in Stage 1. CHANGELOG added.

### Stage 3 — README and meta-files (2-3 hours) — ARCHIVED

**3.1** Write new README:
- Hero with gif/screenshot from website
- Vision in 2 sentences
- Architecture diagram (reuse from website)
- Quickstart (3 lines)
- 3 adapters with links to docs
- Use-cases (admin/code/PR/logs/search/subagents)
- Link to website + docs
- License (AGPL-3.0) + "what's free vs what will be paid"
- Roadmap (web panel, hosted cloud, enterprise)

**3.2** `CONTRIBUTING.md` — dev setup, running tests, code style, PR process.

**3.3** `SECURITY.md` — responsible disclosure, where to report (email/Telegram), SLA.

**3.4** `CODE_OF_CONDUCT.md` — standard Contributor Covenant.

**3.5** `CHANGELOG.md` — start Keep a Changelog format, port current VERSION.

### Stage 4 — CI/CD ✅ DONE

**Status:** badges in README, release.yml workflow, issue/PR templates.

### Stage 4 — CI/CD (1-2 hours) — ARCHIVED

**4.1** Update `.github/workflows/ci.yml`:
- `lint` job: ruff (python) + eslint (userscript) on PR
- `test` job: pytest tests/ on PR (matrix: py3.10, 3.11, 3.12)
- `build` job: build artifacts on tag

**4.2** Add badges to README: CI, license, Python, stars.

**4.3** `release.yml` — on tag v* create GitHub Release with changelog + artifacts.

### Stage 5 — Tests ✅ DONE (static)

**Status:** 15 new static tests (userscript, install scripts, secret prevention). All pass in CI without infrastructure. Integration tests (hub/shellmcp/tunnels) already existed.

### Stage 5 — Tests (gradual, 1-2 days) — ARCHIVED

**5.1** Cover the basics:
- `tests/test_hub.py` — extend (MCP-SSE endpoint, heartbeat, auth)
- `tests/test_shellmcp.py` — exec, file ops, systemd
- `tests/test_cli.py` — setup, tunnel, status commands
- `tests/test_install.sh` — smoke-test install scripts in Docker

**5.2** Smoke-test userscript: headless check that `mcp-bridge.user.js` parses without errors.

### Stage 6 — Launch (after all stages)

**6.1** Make repo public.

**6.2** Topics/description: `mcp`, `mcp-server`, `ai-agent`, `server-admin`, `chatgpt`, `claude`, `self-hosted`.

**6.3** Announce: HN (Show HN), r/selfhosted, r/LocalLLaMA, Russian Telegram channels (Habr, VC), Twitter/X with a gif.

**6.4** Prepare FAQ answers in Discussions.

### Stage 7 — Open-core split (1-2 months later, once stars grow)

**7.1** Private fork: enterprise features (SSO, extended audit, RBAC, advanced panel, SLA hosting).

**7.2** Public repo stays AGPL-3.0 with full basic functionality.

**7.3** Update README: "Cloud / Enterprise — see pricing" linking to website.

---

## IV. Priorities (do right now)

| Priority | Task | Time |
|----------|------|------|
| 🔴 P0 | Secret audit in history (Stage 0) | 30 min |
| 🔴 P0 | LICENSE + .gitignore fix | 20 min |
| 🟠 P1 | New README (Stage 3.1) | 2 hours |
| 🟠 P1 | Remove junk (archives, .claude/.serena) | 30 min |
| 🟡 P2 | Folder restructure (Stage 2) | 2-3 hours |
| 🟡 P2 | CONTRIBUTING/SECURITY/COC | 1 hour |
| 🟢 P3 | Improve CI + badges | 1-2 hours |
| 🟢 P3 | Extend tests | 1-2 days |

---

## V. Constraints & ground rules

- **NEVER force-push** the website repo (multiple contributors).
- For `gptadmin`: ask before any force-push, even while private.
- Resolve conflicts by hand, always preserving the newer incoming work.
- Keep this file updated as work progresses — check off stages as they complete.
