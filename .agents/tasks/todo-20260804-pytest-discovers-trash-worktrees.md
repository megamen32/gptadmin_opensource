# Pytest discovers nested disposable worktrees — 2026-08-04

Status: todo

## Symptom

Running `pytest -q` from the shared root recursively collects tests from
`trash/worktrees/*`, producing duplicate-import collection errors. The CI
command `pytest tests/` is unaffected.

## Smallest evidence

2026-08-04 local root invocation: 190 collection errors, with paths under
`trash/worktrees/v141-*`; the scoped test root remains the intended CI target.

## Blocker

Unselected local test-discovery hygiene work. Do not change pytest discovery
configuration during the Custom GPT release without explicit selection.
