# Changelog

All notable changes to GPT‑Админ are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [139] - 2026-08-02

### Fixed
- Private-source releases no longer fail solely because GitHub does not offer
  artifact attestations to user-owned private repositories; manifest, SBOM,
  installer verification and vulnerability gates remain mandatory.

## [138] - 2026-08-02

### Fixed
- Release documentation no longer links to host-local incident attachments or
  disposable logs that are absent from a clean source checkout.

## [137] - 2026-08-02

### Added
- Durable OAuth refresh-token support for MCP/ChatGPT connections, including
  `offline_access` discovery metadata and digest-only rotating credentials.
- Explicit documentation for the one-time reconnect required by sessions that
  predate refresh support.

### Changed
- HAOS GPTAdmin Hub Standby now deploys as app version 1.0.6 and fails closed
  when Supervisor rejects an update or the requested listener build is absent.
- Release identity advances to build 137 so the private source tag and public
  release do not regress behind existing v136 artifacts.

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
