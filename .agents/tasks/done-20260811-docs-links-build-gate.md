# Documentation links and build gate

Status: done — implementation present in shared dirty checkout; task receipt only

## User request

Fix the incorrect public documentation-site link and documentation 404s. Add a
build-time test that rejects incorrect links or unavailable generated docs.

## Delivered behavior

- Footer links now use `https://became.bezrabotnyi.com` instead of the
  unrelated `https://bezrabotnyi.com`.
- `website/scripts/check-docs-build.mjs` validates the canonical footer target
  and every manifest-selected document across `en`, `ru`, and `cn` in the
  public build tree.
- `website/scripts/check-docs-build.test.mjs` demonstrates failure for a bad
  canonical URL and for a missing document asset, while passing a valid fixture.
- `website/package.json` invokes the docs gate as part of `npm run build` after
  standalone output assembly.

## Fresh evidence

- `node website/scripts/check-docs-build.test.mjs` passed; its deliberately bad
  URL and missing-asset fixtures were rejected.
- `cd website && npm run build` passed. It synchronized 17 documents to each
  of three locales, completed the Next.js production build, and reported:
  `[docs-build] verified 17 docs across 3 locales and canonical footer links`.

## Ownership boundary

The implementation files were already concurrently modified at the same time
by another active author in the shared checkout. They were not staged or
committed by this task, preserving that author's ownership. This receipt is the
only task-owned commit.
