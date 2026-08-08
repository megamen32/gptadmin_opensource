# Docs-as-code and three-language design

Role: Adviser
Goal: Design the minimum maintainable single-source documentation contract for
GPTAdmin code, OpenAPI, CLI help and English/Russian/Chinese documentation.
Allowed paths: read-only docs, tests, OpenAPI generators, website source and
workflows in both repositories.
Excluded: writes, commits, pushes, translation-provider changes.
Acceptance: state canonical language/source layout, derived artifacts,
version/commit visibility, CI checks, stale-translation behavior, and exact
risks of three independently edited languages.
Model: 5.4-mini. Estimate: 20 / 35 / 55 minutes. Cost: medium.
Stop: stop when a concrete implementation blueprint is possible.
Report: append detailed evidence and TL;DR to this file.

## Adviser report

I found enough evidence to define a minimal single-source documentation
contract without touching implementation.

### Evidence

- `docs/DOCUMENTATION_MAP.md` is already the canonical navigation map for
  supported product paths, and it explicitly says changes to a supported path
  must update the relevant section and the changelog.
- `website/scripts/sync-docs.mjs` defines the intended editable source trees as
  `src/content/docs/{en,ru,cn}` and `src/content/i18n/{en,ru,cn}.json`, then
  mirrors them into `public/docs` and `public/i18n` for runtime fetches.
- `website/scripts/translate-docs.mjs` makes English docs the translation
  source, protects fenced code / inline code / links, and writes translated
  markdown back into the locale trees.
- `website/scripts/check-translation-layout.mjs` enforces that only English
  docs are the source for ru/cn, that each locale has the same filenames, and
  that generated translations are not stored beside the English originals.
- `tests/test_docs_product_contract.py` already checks the docs map, local link
  integrity, required verification snippets, and that the release workflow runs
  the docs contract.
- `.github/workflows/build-and-sync.yml` runs the docs product contract during
  release builds and also verifies manifest / SBOM / installer-link gates.
- `go-hub/internal/hub/server.go` generates the live OpenAPI schema for
  `/actions/openapi.yaml` and per-server `/server/{slug}/actions/openapi.yaml`
  directly from hub code.
- `public/openapi.yaml` is the checked-in artifact test target; the install
  tests require it to exist and to stay on OpenAPI 3.1.0 with stable
  `info.version`.
- `cli.py` exposes the CLI help tree from argparse, including a `version`
  command, and `gptadmin_build_info.py` exposes `build_version` plus
  `git_commit`.
- `docs/SUPPLY_CHAIN.md` says the release manifest records build version,
  commit, artifact path, size, platform, architecture, and digest.
- `website-bump.yml` shows the website content is a separate submodule that is
  bumped independently.
- There is a real ambiguity today: `website/src/content/i18n/*` and
  `website/src/i18n/*` both exist, but they are not identical (`cmp` returned
  non-zero for en/ru/cn), so the repo currently has two editable-looking i18n
  trees.

### Minimum maintainable contract

1. One authoritative source per semantic surface.

   - Code help text: `cli.py` argparse declarations.
   - OpenAPI: the live generator in `go-hub/internal/hub/server.go`, with
     `public/openapi.yaml` as the checked-in verification artifact.
   - English docs: `website/src/content/docs/en/*.md`.
   - Translations: `website/src/content/docs/ru/*.md` and
     `website/src/content/docs/cn/*.md`, generated from English only.
   - UI chrome / landing copy: one canonical English JSON source tree plus
     generated ru/cn mirrors; no second editable tree.

2. Derived artifacts are publish-only.

   - `website/public/docs/{en,ru,cn}/*.md`
   - `website/public/i18n/{en,ru,cn}.json`
   - any served OpenAPI mirror or alias

   These should be rebuilt, never hand-edited.

3. Every human-facing change follows a source → derived chain.

   - Update English source.
   - Regenerate translations and public mirrors.
   - Run layout, literal-preservation, docs-contract, and runtime i18n checks.

4. Stale translation behavior is fail-closed.

   - Missing ru/cn files for an English doc fail CI.
   - Extra ru/cn files without an English source fail CI.
   - Protected literals changing in translation fail CI.
   - Runtime locale smoke must still show the intended dominant script.

5. Version / commit visibility is shared across surfaces.

   - CLI `gptadmin version`
   - `GET /version`
   - release manifest / SBOM / provenance

   All three should carry the same build version and commit identity.

### Exact risks of three independently edited languages

- Terminology drift: the same route, capability, or concept will be named
  differently across en/ru/cn.
- Route drift: one language can keep an old path or action name while another
  is updated, which is especially dangerous for OpenAPI and install docs.
- Literal drift: code blocks, URLs, and command snippets can diverge if they are
  edited manually instead of generated from one source.
- Navigation drift: the docs map and cross-links can disagree, so users land on
  the wrong canonical page.
- Mirror drift: if the public mirror is treated as editable, it can silently
  stop matching the source tree.
- Tree ambiguity: the current `website/src/content/i18n` vs `website/src/i18n`
  split is already a concrete example of how duplicated editable trees create
  invisible divergence.

### TL;DR

Use one English source tree for docs, one code-driven OpenAPI generator, one
CLI help source, and generated ru/cn/public mirrors only. The current repo
already has most of the guardrails; the main contract gap is the duplicated
i18n source tree and the need to treat every derived artifact as publish-only.
