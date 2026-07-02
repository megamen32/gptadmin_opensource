# Security Policy

## Reporting a Vulnerability

GPT‑Админ gives AI agents access to your servers — security matters. If you
find a vulnerability, please report it responsibly.

- **Do NOT open a public GitHub issue.**
- Report via Telegram: [@careviolan](https://t.me/careviolan)
- Or email the maintainer directly.

Please include:
- A description of the issue and its potential impact
- Steps to reproduce (proof-of-concept)
- Affected version (`cat VERSION` or `gptadmin --version`)
- Suggested fix, if any

## Response SLA

- **Acknowledgement:** within 48 hours
- **Initial assessment:** within 7 days
- **Fix or mitigation:** depends on severity, target 30 days for critical issues

## Disclosure

We follow coordinated disclosure. Once a fix is released, we will publish a
GitHub Security Advisory and credit the reporter (unless they prefer to remain
anonymous).

## Scope

- The `gptadmin_hub` / `shellmcp` services and their HTTP/MCP endpoints
- The `gptadmin` CLI
- The `mcp-bridge.user.js` browser extension
- Install scripts (`deploy/install*.sh`, `install_win.ps1`)

## Out of scope

- Vulnerabilities in third-party dependencies (report upstream)
- Issues requiring already-compromised root access
- Social engineering
