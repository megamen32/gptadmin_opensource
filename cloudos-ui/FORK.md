# CloudOS UI — fork of PuruVJ/macos-web

This directory is a fork of [PuruVJ/macos-web](https://github.com/PuruVJ/macos-web)
(MIT License, see `LICENSE`), vendored as the GPTAdmin CloudOS desktop shell.

- **Upstream:** https://github.com/PuruVJ/macos-web
- **Vendored from commit:** `14204808226d67fce14924d0b2ea889c807f12a9` (2026-07-05)
- **Upstream license:** MIT — © 2021 Puru Vijay. MIT terms are compatible with
  this repository's AGPL-3.0 for the combined work; the original MIT license
  and notice remain in effect for the vendored code.

## Fork purpose

GPTAdmin CloudOS is a macOS-style web desktop whose apps (Terminal, Finder,
Browser, Computers) operate real GPTAdmin nodes through the hub's
`/api/v1/cloud-os/*` APIs. CloudOS is only a client of the hub — it owns no
backend and no second host-enrollment protocol.

## Local changes (kept intentionally small)

- `vite.config.ts`: `base: '/cloudos/'`; PWA/serwist service worker removed
  (a service worker below `/cloudos/` would fight hub deployments).
- `src/sw.ts`, `src/components/Desktop/SystemUpdate.svelte`: removed with the
  PWA plugin.
- `index.html`: retitled CloudOS, removed upstream telemetry (Clarity) and
  OG tags; asset links made relative to the base.
- `src/lib/`: new — `cloudos-api.ts` (hub API client) and `assets.ts`
  (BASE_URL-aware public asset paths).
- `src/components/apps/`: new GPTAdmin apps — `Terminal/`, `Finder/`,
  `Browser/`, `Computers/` — wired to the hub APIs.
- `src/configs/apps/apps-config.ts`, `src/state/apps.svelte.ts`,
  `src/components/apps/AppNexus.svelte`: app registry now derives from the
  config; upstream demo apps (App Store, developer profile) dropped from the
  dock; Calculator/Calendar/Wallpapers/VSCode kept.
- `src/css/theme.css`: cursor asset URLs pinned to the `/cloudos/` base.
- `prefetch-plugin.ts`: prefetch hrefs respect the configured base.
- `public/app-icons/computers/`: generated icon set (programmatic, no
  third-party assets).

## Upgrading

Re-export the upstream tree with `git archive`, re-apply the local changes
above, and keep this file's upstream commit line accurate.
