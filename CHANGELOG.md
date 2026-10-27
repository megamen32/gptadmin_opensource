# Changelog

All notable changes to GPT‑Админ are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Performance
- Reuse delivered profile-instruction receipts for the same authenticated credential and profile by default, avoiding repeated receipt transmission on each Hub call.

### Fixed
- The browser OAuth callback now delivers its PKCE-bound authorization code to
  the chat-site opener; the bridge still validates the Hub origin and OAuth state.
- An asynchronous webhook dispatcher no longer starts a job after it has already
  reached a terminal state, so late progress updates remain rejected.
- Nodes in the `failed` state were invisible: the status filter offered no way to select
  them, the Overview attention panel truncated its list to six rows, and the summary tile
  counted only online and stale, so the numbers contradicted both each other and the node
  total. Failed nodes are now filterable, listed first, counted, and labelled in Russian.
- Child nodes inherited their parent host label, so a shell target, a file target and a
  child MCP on the same machine rendered as identical rows. Rows now carry a kind
  qualifier such as «MCP-компонент: BrowserOS» next to the friendly name.
- For online nodes `last_seen` is the hub poll time, which tracked the current clock and
  answered nothing. Recent values now render as «только что» / «N минут назад», with the
  exact timestamp in the tooltip; stale and failed nodes keep absolute time.

### Changed
- An issued token now states its granted scopes, access mode and expected expiry, in the
  result block and on the client card. The result block sits next to the issuing form
  instead of at the top of the page, the form has an «Отмена» action, and the result
  includes the Hub endpoint plus a copy-ready `mcp.json` server entry.

### Fixed
- Admin UI renders timestamps as readable local date and time instead of raw
  Unix seconds on the Servers, Overview and Jobs screens, for ISO strings,
  Unix seconds, Unix milliseconds and missing values.

### Changed
- The managed-token form on the Clients screen is now reachable from a primary
  "Новый клиент" entry point, stays visible while the client list is loading or
  failed to load, and shows the granted scopes plus a copy-ready
  `Authorization: Bearer ...` header for `mcp.json`.


## [1024] - 2026-10-17

### Added
- Code Mode chains authorized MCP tools in a bounded JavaScript runtime and returns only selected JSON.
- Profile-specific instruction delivery before execution, with versioned context receipts and refresh after rule changes.
- Portable GPTAdmin plugin 1.3.0 packaging alongside the existing Codex format.

### Fixed
- Secret request and status tools are routed through the generic Hub execution facade.
- Installation, update verification and acceptance clients obtain the current instruction context before dispatch instead of failing on the new precondition.
- Exact tool-schema selection stays scoped even with full detail; compact successful replies retain expandable diagnostics.
- Regression coverage for profile isolation, current-context admission, reader restrictions and cross-node delivery.

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
