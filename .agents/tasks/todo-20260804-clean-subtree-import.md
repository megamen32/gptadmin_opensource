# Clean website subtree import

Role: Worker
Goal: In this clean integration worktree, replace the `website` gitlink with a
history-preserving unsquashed subtree from
`https://github.com/megamen32/adminchatgpt_website.git` at its current `main`.

Allowed paths: `/tmp/gptadmin-monorepo-docs-8dyCEn/.gitmodules` if present,
the root gitlink entry, and `/tmp/gptadmin-monorepo-docs-8dyCEn/website/**`.
The worktree is disposable and clean; do not touch `/home/roomhacker/gptadmin`.

Excluded: docs content rewrites, CI/test edits, deployment, external website
repository writes, archive operations, pushes, and unrelated files.

Acceptance: website is no longer index mode `160000`; gitlink and `.gitmodules`
reference are gone; imported website history is visible in `git log -- website`;
no generated cache is staged; `git diff --check` passes; create one local commit
on `codex/monorepo-docs` and report its SHA plus exact subtree source SHA.

Estimate: 20 / 35 / 60 active minutes. Cost: medium.
Stop: stop if an import would overwrite a non-empty tracked tree, source ref
cannot be resolved, or the operation requires a push.
Report: append detailed evidence and TL;DR here.

## Harness revision

The prior Worker produced no worktree mutation or report. A replacement Worker
must start by `cd /tmp/gptadmin-monorepo-docs-8dyCEn`, record the pre-state,
and execute only this bounded import. This is a fresh assignment, not a request
to modify the parent checkout.
