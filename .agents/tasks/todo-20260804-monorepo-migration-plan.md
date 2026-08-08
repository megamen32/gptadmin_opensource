# Monorepo migration implementation research

Role: Worker
Goal: Add the root-level docs-as-code contract checks that remain valid before
and after the website subtree import.
Allowed paths: root `.github/workflows/`, root `tests/`, root docs-contract
scripts/config only. Do not edit the `website/` tree or git metadata.
Excluded: submodule/subtree import, docs prose migration, external-repo writes,
deployment, archival and unrelated code.
Acceptance: a deterministic local command checks generated OpenAPI against its
checked-in artifact, CLI-help/doc contract hooks where already available, and
the website docs contract without skip-gating; workflow runs it; focused tests
pass. Do not push.
Model: 5.4-mini. Estimate: 35 / 60 / 100 minutes. Cost: medium.
Stop: report a hard generator/CLI source ambiguity rather than inventing a
second source of truth.
Report: append detailed evidence and TL;DR to this file.

## Worker report (2026-08-04) — STOP: generator/source ambiguity

### TL;DR

Работа остановлена по предусмотренному условию `hard generator/CLI source
ambiguity`. Runtime OpenAPI генерируется функцией
`go-hub/internal/hub/server.go:1196-1205,1766-1838`
(`defaultCustomGPTActionsOpenAPI(s.origin(r)`), а checked-in
`public/openapi.yaml` является другим контрактом: он содержит `/connect.json`,
webhooks и расширенные схемы, тогда как runtime default contract содержит
только `discover → schema → execute → job` и отдельный `EmptyObject` schema.
В репозитории нет генератора, fixture или canonical normalization command,
который связывает эти два артефакта. Проверку «generated OpenAPI == checked-in
artifact» нельзя добавить корректно без выбора владельца источника.

### Evidence

- `go-hub/internal/hub/server.go:1196-1205`: `/actions/openapi.yaml` строится
  на лету из `defaultCustomGPTActionsOpenAPI(s.origin(r))`.
- `go-hub/internal/hub/server.go:1766-1838`: generated runtime schema имеет
  `discover`, `schema`, `execute`, `job`; описывает compact Custom GPT relay
  contract и optional capabilities как отдельные virtual MCP servers.
- `public/openapi.yaml:1-180` (и далее): checked-in schema содержит
  `/connect.json`, `/webhooks/v1/{route}`, `/webhook-jobs/{job_id}`, admin
  webhook endpoints и расширенные schemas; это не byte-/semantic-equivalent
  output runtime generator.
- `rg` over `tools scripts tests go-hub Makefile` found no generator command or
  fixture that emits `public/openapi.yaml`; only existence/content assertions
  (`tests/test_install_scripts.py:128-134`) and runtime Go tests consume it
  (`go-hub/internal/hub/network_proxy_test.go:383-399`).
- Existing docs workflow hook (`.github/workflows/build-and-sync.yml:110-111`)
  runs docs product tests only; it does not generate or compare OpenAPI.
- `tests/test_site_docs.py:8-16` remains skip-gated when website source is not
  checked out. Removing that guard now would fail current CI checkout topology;
  making it unconditional belongs with the website import/CI checkout slice,
  not this stopped slice.

### What was checked

Checked root tests, workflow references, Go runtime OpenAPI handlers, checked-in
schema references, scripts/tools/Makefile for generation hooks, and current git
status. No files other than this task report were changed; website tree and git
metadata were not touched. No tests were run because no safe implementation
exists under the current source contract.

### Required Lead decision / next probe

Lead must choose one authoritative source:

1. Make the Go `defaultCustomGPTActionsOpenAPI` output canonical and add a
   deterministic generator/export command plus regenerate `public/openapi.yaml`;
   or
2. Make `public/openapi.yaml` canonical and change the runtime handler to serve
   that artifact (which may be a behavior/API contract change requiring review);
   or
3. Explicitly define them as two separate contracts and narrow the requested
   check to a named artifact, not equality.

Until that decision, adding a root docs-as-code checker would either falsely
  fail the repository or silently bless drift. Worker slice cannot proceed.

## Explorer report (2026-08-04)

### TL;DR

- `website` is a gitlink (`160000`) at `aaccbb7...`, configured by
  `.gitmodules:1-3` to `https://github.com/megamen32/adminchatgpt_website/`;
  the checked-out submodule is on `main` tracking `origin/main`.
- Parent automation is pointer maintenance only: `.github/workflows/website-bump.yml:3-5,38-60`
  fetches the external repo twice daily, updates the gitlink, commits, and pushes.
  It requires `GPTADMIN_BOT_PAT` with cross-repository read/write access
  (`:22-36`). This workflow must be removed/replaced after import.
- Website-side translation automation is in `website/.github/workflows/translate-docs.yml:3-6,13-76`.
  It translates `src/content/docs/en/**` into `ru/` and `cn/`, syncs generated
  `public/docs`, and commits back. In a monorepo this must become a root CI job
  (or a deliberately authorized bot commit), with loop prevention and one
  commit SHA governing source plus generated output.
- Runtime content contract is `website/src/content/docs/{en,ru,cn}` (17 Markdown
  files per locale currently), mirrored to `website/public/docs` by
  `website/scripts/sync-docs.mjs`; `website/src/components/site/pages/docs-page.tsx:120-125`
  fetches `/docs/<locale>/<slug>.md`. Keep this URL shape during import.
- The only discovered deploy path is local/manual: `scripts/update-website.sh:3-11`
  does `git pull`, `bun run build`, then restarts
  `gptadminwebsite-next.service`. No checked-in service unit or root GitHub
  website deploy workflow was found. The deployment owner/host configuration is
  therefore an unresolved external boundary and requires user authorization
  before changing trigger, hostname routing, or service ownership.

### Affected files and migration slices

1. **History-preserving import / git topology**
   - Replace root gitlink and `.gitmodules:1-3` with ordinary tracked files.
   - Import the external repository tree at `website/`, preserving the external
     history via a merge/unrelated-history import or subtree strategy selected by
     Lead. Preserve a rollback tag/branch at the pre-import parent commit and
     retain the external repository as read-only legacy until replacement URL is
     live. Do not delete or archive the external repo in this slice.
   - Remove `.github/workflows/website-bump.yml`; no cross-repository PAT should
     remain necessary.

2. **Docs source-of-truth**
   - Existing root canonical docs are `docs/*.md` (inventory includes
     `docs/Home.md`, `GETTING_STARTED.md`, `ADAPTERS.md`, `API_REFERENCE.md`,
     `DOCUMENTATION_MAP.md`, etc.; 43 top-level Markdown files observed).
   - Existing website docs are a separate 17-file localized subset under
     `website/src/content/docs/{en,ru,cn}`. The migration must explicitly map
     which root docs are website-published, rather than blindly moving all
     operational/private docs into the public site.
   - Recommended independent slice: establish `docs/site/en/` (or another
     clearly canonical public subset), move/copy only selected public docs there,
     then point website source/sync/translation to that directory. Keep
     `docs/` links and `README.md` stable through compatibility links or a
     documented mapping. This is an architecture decision requiring Lead/user
     selection because current sources differ in count and likely audience.

3. **Generated translations and static mirrors**
   - `website/package.json:5-18` defines `sync-docs`, translation, i18n checks,
     standalone build, and lint. `website/scripts/sync-docs.mjs` copies source
     docs and i18n JSON into `public/`; the build's `prebuild` runs sync.
   - `.gittranslate` and `website/scripts/check-translation-layout.mjs` enforce
     `ru cn` from `src/content/docs/en/*.md`; `check-translation-literals.mjs`
     protects fenced/inline code and links. Preserve these checks, but make
     their paths root-monorepo aware and ensure generated `ru/cn` policy is
     explicit (committed outputs vs CI artifacts).
   - Add root CI checks for: source/translation filename parity, protected
     literals, i18n static audit, no owner/private hub URL, and no stale public
     mirror. Existing `tests/test_site_docs.py:8-33` is currently skip-gated
     because CI does not check out the private submodule; after import it should
     run unconditionally and be expanded for the selected public docs set.

4. **Website build/test CI**
   - Website build is Next standalone (`website/next.config.ts:3-16`) and uses
     Bun in `website/package.json:14-16`; `website/package-lock.json` and
     `website/bun.lock` both exist. Existing root CI only builds/tests
     `admin-ui` (`.github/workflows/build-and-sync.yml:23-58`), not website.
   - Add a bounded website job: install using the chosen lockfile, run sync,
     translation/layout/literal/i18n checks, lint, `next build`, standalone
     output check, and root `tests/test_site_docs.py`. Avoid adding a deploy step
     until the external deployment owner/target is identified and authorized.

5. **Public legacy notice**
   - The old public repository is `megamen32/adminchatgpt_website` (confirmed in
     `.gitmodules`, parent workflow comments, and submodule origin). The notice
     should be added to that repository's public `README.md` (or GitHub repo
     description/topic if README is not the chosen channel), linking to the new
     canonical `gptadmin` website path and stating that no new changes belong in
     the old repo. This is an external repository write and archival action;
     user authorization is required after replacement URL and live canary exist.

### Required tests / canaries

- Git topology: assert `website` is no longer mode `160000`, `.gitmodules` is
  absent/unused, and all imported website files are tracked in the parent.
- Docs contract: run existing `tests/test_site_docs.py` without skip; assert
  action URL, OAuth authorize/token paths, required scopes, and absence of the
  owner hub URL (`tests/test_site_docs.py:24-33`).
- Translation contract: run website layout/literal/static checks and assert each
  generated locale exactly matches English filenames plus protected executable
  literals.
- Build contract: `bun/npm` install from exactly one selected lockfile, run
  website build, verify `.next/standalone/server.js`, and smoke `/`, `/#/docs`,
  `/docs/en/<known-slug>.md`, `/docs/ru/<known-slug>.md`, and `/docs/cn/<known-slug>.md`.
- Full business canary before legacy archival: public replacement hostname,
  docs navigation, all three locale fetches, install links, and OpenAPI/action
  links must be externally verified from the deployed runtime. A local build or
  systemd `active` state is not sufficient.

### Breaking boundaries / authorization flags

- **Potential URL break:** changing the hash routes (`/#/docs/...`) or static
  fetch paths (`/docs/<locale>/<slug>.md`) would break bookmarks and clients;
  preserve them or obtain explicit authorization and add redirects.
- **Potential deployment break:** replacing `git pull` from the external repo
  with parent checkout/CI or changing `gptadmin.bezrabotnyi.com` routing and
  `gptadminwebsite-next.service` ownership requires authorization. The checked-in
  tree does not identify the service unit, host, reverse proxy, or DNS owner.
- **Potential docs URL break:** changing slug/file names or moving `docs/` without
  compatibility links can break README and website links. Establish a mapping
  and redirect/alias test before moving files.
- **Legacy archival:** do not archive/delete `adminchatgpt_website` merely because
  the gitlink is removed. Archive only after replacement URL, deployment owner,
  public canary, and legacy notice are confirmed.

### Checked and excluded

Checked: root gitlink/submodule metadata, external remote/branch/history,
website package/build/i18n scripts, website translation workflow, parent CI,
site-doc tests, local update script, docs inventory, and references to website
deployment. Excluded: live host inspection, systemd unit contents, DNS/Caddy
configuration outside this repository, GitHub API state, external repository
writes, commits, pushes, deploys, and archival.

### Highest-value next probe

Lead should obtain explicit user direction and current deployment-owner evidence
for the host serving `gptadmin.bezrabotnyi.com` and
`gptadminwebsite-next.service`; then choose the canonical public-doc source
layout (`docs/site/en` versus website-local `src/content/docs/en`) before any
Worker import. No code mutation is recommended until that decision is gated.
