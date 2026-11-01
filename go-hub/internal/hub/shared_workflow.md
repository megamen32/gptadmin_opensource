## Shared engineering workflow (LHC + Agent Herder)

Applies to engineering and project work, including clients without local plugins.
Preserve the selected profile's domain/admin instructions and access restrictions.
This workflow grants no credentials, permissions or authority beyond the user's task.

- Use the existing project plan/board, keep source, review, merge, installation and live verification distinct. Record branch, exact commit, PR and evidence. Put large-task plans in the existing LHC plan, not a competing tracker.
- Each task uses its own branch and worktree from the verified integration target. Commit coherent changes and push the task branch freely. An early draft PR with the plan link makes unfinished work visible; check the plan's audience/access before attaching private information. Mark ready after review and tests.
- Integrate through PRs, not direct writes to shared main. Follow the owner's existing merge/deploy policy and repository checks; do not invent extra approval questions for already authorized steps or bypass genuine permission/security gates.
- Build candidates in an isolated staging area; do not move shared current/live pointers during build. Only the lease-admitted rollout changes live pointers. Use currently merged release scripts, not stale task-branch scripts that bypass guards.
- Before changing a shared runtime use Agent Herder project_release_lease status/acquire/renew/check with canonical project identity, session owner, token and fence. Re-read the live revision and enforce expected-revision CAS. Never overwrite a newer release with a stale artifact. Release in finally; terminal lifecycle events release ownership and TTL handles crashes. Ordinary turn completion is not session end.
- At terminal session end inventory project work and hand off unfinished integration. The last active worker owns whole-project integration, testing and cleanup. Use authoritative session state, not a guess from Git. Contact active owners through SendMessage.
- When bounded lookup finds no active owner of existing work, take responsibility: preserve exact commits and dirty diffs, record the adoption in the existing plan, inspect/review/test it and finish integration. Ambiguous ownership is not inactivity, and owner absence is not proof the changes are correct. Recheck ownership before mutation.
- Preserve active, dirty, conflicted, unmerged or unknown work. Clean up only verified merged, clean, inactive work under project coordination after rechecking HEAD and ownership. Never bulk-stash, reset foreign work, force-remove worktrees or blindly merge unknown branches.
- Reuse existing components and supported native install/release paths. Verify the exact installed revision and a harmless real consumer operation; tests, HTTP 200 or a receipt alone do not prove live acceptance. Keep unsupported clients, missing lifecycle evidence and remaining blockers explicit.
