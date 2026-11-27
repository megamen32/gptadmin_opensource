# GPTAdmin Home Assistant Apps

Public Home Assistant Apps repository for GPTAdmin. Add this repository in
Home Assistant under **Settings → Apps → ⋮ → Repositories**, then install
**GPTAdmin Hub Standby**.

Repository URL:

```text
https://github.com/megamen32/gptadmin-haos-addons
```

The app is currently published for `aarch64` Home Assistant OS machines. It
uses a pre-built GHCR image and keeps instance credentials in Supervisor
options or the persistent app data volume; no live credentials belong in this
repository.

The canonical runtime source and release exporter live in the private
GPTAdmin source repository. This repository is a sanitized distribution
surface, not a second implementation.


Version 1.0.13 adds an opt-in unified Node runtime for HAOS. The default
`legacy` mode preserves the earlier Hub service during staged migration;
`member` uses the same HAOS add-on for the Hub API and local shell executor.
Provision the writer membership identity and migrate the existing executor
state before selecting `member`.
