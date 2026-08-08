# Website topology research

Role: Worker
Goal: Import the `website` submodule into GPTAdmin as a history-preserving,
unsquashed git subtree on the current migration branch.
Allowed paths: root git metadata needed for submodule removal, `.gitmodules`,
and the imported `website/` tree only.
Excluded: docs content rewrites, CI/test changes, deployment, external-repo
writes, archive operations, unrelated tracked changes.
Acceptance: `website` is ordinary tracked content rather than mode `160000`;
the external website history is present; no `.gitmodules`/submodule reference
remains; `git diff --check` passes; report exact import command and rollback
commit/tag.
Model: 5.4-mini. Estimate: 25 / 45 / 75 minutes. Cost: medium.
Stop: stop on a non-fast-forward/history ambiguity or if import would alter
unrelated files. Do not push.
Report: append detailed evidence and TL;DR to this file.

## Explorer evidence (2026-08-04, read-only)

### Finding / exact topology

- GPTAdmin is the parent repository at `/home/roomhacker/gptadmin`, remote
  `origin = git@github.com:megamen32/gptadmin.git`; current branch is
  `codex/haos-addon-public`, at `3c643120befd45caf852fc87ff3b5f982d012905`
  (ahead of its `origin` tracking branch by one commit). The parent tree stores
  `website` as a gitlink, not ordinary files:
  `git ls-tree HEAD website` => `160000 commit
  aaccbb7dea5e26041b9f386c01f5b25b7c998f88 website`.
- `.gitmodules:1-3` defines `[submodule "website"]`, path `website`, URL
  `https://github.com/megamen32/adminchatgpt_website/`. Local submodule config
  has `submodule.website.active=true` and the same URL.
- The checked-out website repository is independently rooted at
  `/home/roomhacker/gptadmin/website`, remote
  `origin = https://github.com/megamen32/adminchatgpt_website/`, branch `main`
  tracking `origin/main`. Its current commit is
  `aaccbb7dea5e26041b9f386c01f5b25b7c998f88`, dated 2026-07-15 13:38:12 UTC,
  `docs(site): auto-translate from en/ [skip ci]`; HEAD equals origin/main.
- The local parent differs from `origin/main`: local HEAD points at website
  `aaccbb7`, while `origin/main` (`842758524b6e7548672b79c6bb418bd912b6bd5f`)
  points at website commit `bc76d8ce14ba8fd57902aff52c26ba2814d8028b`.
  Therefore the import should use an explicitly recorded source SHA, not
  assume the remote parent branch is the same snapshot.
- Current read-only status shows the parent has many pre-existing untracked
  task/metadata paths and the submodule has untracked generated files under
  `__pycache__/`; these must be excluded from any import staging. No files were
  changed by this investigation.

### Build and publish/deploy trigger

- The website is a Next.js standalone application: `website/package.json:5-22`
  defines `prebuild` as `node scripts/sync-docs.mjs`, `build` as `next build`
  plus copying `.next/static` and `public` into `.next/standalone`, and `start`
  as `bun .next/standalone/server.js`. `website/next.config.ts:4-5` sets
  `output: "standalone"`.
- Website repo CI has only
  `website/.github/workflows/translate-docs.yml:13-76`. It runs on pushes to
  `main` only when `src/content/docs/en/**`, `.gittranslate`, or the workflow
  changes; it translates/syncs/checks docs and commits generated locale output.
  It does not build or deploy the Next.js site.
- Parent `.github/workflows/website-bump.yml:7-60` runs twice daily (06:17 and
  18:17 UTC) or manually, fetches website `origin/main`, compares the parent
  gitlink with the remote website SHA, updates the submodule pointer, commits,
  and pushes GPTAdmin using `GPTADMIN_BOT_PAT`. This is a pointer-sync trigger,
  not a website deployment trigger.
- The only checked-in deployment procedure found is
  `scripts/update-website.sh:3-11`: `git pull --no-edit` in the submodule,
  `bun run build`, then `sudo systemctl restart
  gptadminwebsite-next.service`, followed by an Active-status check. Thus the
  evidence supports a host-local pull/build/systemd deployment, with no
  repository-hosted website publish workflow found.

### Bounded recommendation / rollback point

For a history-preserving monorepo import, first create an immutable backup/tag
of the parent commit and record the exact website SHA (`aaccbb7...` for this
checkout; use the intended SHA explicitly). On a dedicated integration branch,
preserve the submodule directory outside the worktree, remove only the
gitlink and `.gitmodules` entry, then run an unsquashed subtree import from the
website remote, e.g. `git subtree add --prefix=website
https://github.com/megamen32/adminchatgpt_website.git main` (or import a local
mirror containing the selected SHA). Unsquashed `git subtree add` retains the
website repository commit history; `--squash` would not satisfy that goal.
Reapply only intentionally tracked website files, excluding `.next`,
`node_modules`, and `__pycache__`, then verify the resulting tree and run the
existing website build canary before changing any host deployment mechanism.

The rollback point is the pre-import parent backup/tag (current local
`3c643120...` if that is the selected source state), plus the recorded source
website SHA. Reverting the integration branch to that commit restores the
gitlink topology; no deployment change is needed or authorized by this task.

### TL;DR for Lead

`website` is currently a gitlink to
`megamen32/adminchatgpt_website` at `aaccbb7`; the parent remote is
`megamen32/gptadmin`. Website CI translates docs only; parent CI auto-bumps the
pointer twice daily. Actual publish is host-local `git pull` + `bun run build` +
`systemctl restart gptadminwebsite-next.service`. Use an explicit-SHA,
unsquashed `git subtree add` on an integration branch after tagging the parent
pre-import commit; retain that tag as rollback.

## Worker execution (2026-08-04)

### STOP_SCOPE_DRIFT / blocker

The assigned import was not started. Before mutation, shared-worktree checks
confirmed `website/` is a dirty submodule with untracked generated files:

```text
?? __pycache__/
?? skills/gaokao-fetch-volunteers/scripts/__pycache__/province_config.cpython-310.pyc
?? skills/gaokao-fetch-volunteers/scripts/__pycache__/test_batch_api.cpython-310-pytest-8.4.2.pyc
```

An unsquashed `git subtree add --prefix=website ...` cannot use the existing
non-empty submodule path directly. Making the path available would require
moving, deleting, or archiving the existing submodule checkout, and staging the
directory would include the untracked generated files. Those actions are
outside the selected slice (`archive operations` excluded) and violate the
shared-worktree hands-off rule for foreign dirty paths. I did not run `git rm`,
`git clean`, `git reset`, `git restore`, `git stash`, staging, commit, push, or
any deployment action.

### Evidence before stop

- Parent remains at `3c643120befd45caf852fc87ff3b5f982d012905` on
  `codex/haos-addon-public`.
- `git ls-tree HEAD website` remains mode `160000`, pointing to
  `aaccbb7dea5e26041b9f386c01f5b25b7c998f88`.
- `git -C website ls-files | wc -l` = `1301`; website HEAD remains
  `aaccbb7dea5e26041b9f386c01f5b25b7c998f88`.
- No files outside the task report were modified by this worker.

### Required next action

Lead/human must explicitly provide a safe path disposition for the dirty
submodule checkout (for example, independently preserve it and replace the
checkout with a clean checkout, or authorize a narrowly named temporary move).
After that disposition, the worker can perform the explicit-SHA unsquashed
subtree import and validate ordinary tracked files, external history,
`.gitmodules` removal, and `git diff --check`.

### Worker TL;DR

STOP_SCOPE_DRIFT: no import performed because the existing submodule path has
foreign untracked generated files and replacing it would require an excluded
archive/move/delete operation. Gitlink and worktree are unchanged; no staging,
commit, push, or deployment occurred.
