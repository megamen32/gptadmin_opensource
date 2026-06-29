# Changelog

All notable changes to GPT‑Админ are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- AGPL-3.0 license
- SECURITY.md (responsible disclosure policy)
- CONTRIBUTING.md (dev setup, code style, PR process)
- CODE_OF_CONDUCT.md (Contributor Covenant v2.1)
- Open-core launch plan (`docs/OPEN_CORE_PLAN.md`)
- New README with vision, architecture diagram, quickstart, 3 adapters, use-cases
- pyproject.toml: license metadata, classifiers, keywords

### Changed
- `.gitignore`: fixed broken merge conflict markers, merged duplicates, added
  `.claude/`, `.serena/`, `*.tar.gz`, `*.zip`, `ngrok_url.txt`, `.cloudflared/`,
  `scripts/check_mac_tunnel_matrix.env`

### Removed
- `gptadmin_refactor_2026-05-11_15-18-03.tar.gz` (repo junk)
- `root_hub_license_refactor.zip` (repo junk)
- `.claude/`, `.serena/` (private AI-assistant settings)

### Security
- Scrubbed leaked `MCP_AUTH_TOKEN` from git history via `git filter-repo`
- Scrubbed leaked `CTL_TOKEN` / `ADMIN_PASSWORD` from git history

## [0.1.0] - 2025-05-01

### Added
- Initial release of GPT‑Админ
- `gptadmin_hub` — MCP hub, proxies commands to agents
- `shellmcp` — shell agent (Python + Go) for target machines
- Three adapters: OpenAI Action, MCP remote SSE, browser extension (userscript)
- CLI (`gptadmin`): setup, tunnel (FRP/Cloudflare), status, logs
- Web panel at `/admin` (queue, agent/MCP health, logs)
- OAuth for OpenAI SDK
- Auto-tunnel via FRP and Cloudflare
- Install scripts for Linux/macOS/Windows (user-mode and system-mode)
