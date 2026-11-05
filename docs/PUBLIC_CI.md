# Public CI

Product checks run in `megamen32/gptadmin_opensource`, using standard public
GitHub-hosted runners. Private `megamen32/gptadmin` Actions are disabled.
There is no private-source checkout, credential input or self-hosted fleet runner
in public workflows. One source push runs product checks once; no auto-tag loop
dispatches a second complete release for every commit. Exact Node artifact
retention is one day. Full-platform GitHub Release publication remains a
separate explicit action; the verified Node package publisher runs
automatically after successful public product CI on `main`.

Private-only mirror-policy and website-source contract tests stay in the
authoritative source because their protected inputs are intentionally excluded
from the public tree. Public CI keeps Go, Python/CLI, admin UI, real candidate,
installer/failover and platform checks. It must report failures honestly.

In the private checkout, install the local pre-push hook once:

```sh
python3 .gitpublic/publish_hook.py install
```

After that, `git push origin main` publishes the sanitized public descendant
before the private push completes. The hook ignores other remotes and branches,
checks that the pushed commit is the checked-out private `main`, and blocks the
private push if the public scanner or non-force public push fails.

For manual recovery, publish committed source with the installed privacy policy,
preserving the existing public history:

```sh
python3 tools/publish_public_source.py prepare --receipt /home/admin/gptadmin/.tmp/PUBLICATION/receipt.json
# Inspect source/public parent, excluded paths, scanner and payload receipts.
python3 tools/publish_public_source.py push --receipt /home/admin/gptadmin/.tmp/PUBLICATION/receipt.json --receipt-sha256 REVIEWED_PREPARE_DIGEST
```

Create the private receipt directory first. The publisher reads committed bytes,
uses the existing `.gitpublic` exclusions/replacements/scanner and the installed
authorized Git SSH connection. It appends a normal commit and refuses source or
public-parent drift. It never resets or force-pushes either repository. The old
snapshot publisher has `push_force=false`, so it fails closed rather than erasing
this public CI history. The public workflow overlay is versioned under
`.gitpublic/public/` and deliberately contains no secret input.

The candidate contract runs real packaged Hub executables on isolated runner
loopback ports. It imports OpenAPI from one actual instance and validates the
second artifact through Actions and MCP. It does not require a deployed tenant,
a fleet credential or production secret. Test signing keys are ephemeral fixtures.

If a future publication step needs credentials, use GitHub Actions Secrets with
minimum permissions and an explicit protected release environment. Never commit
credentials or transfer owner/fleet credentials into pull-request checks. Current
public product workflows have no `secrets.*` references or credential input.

Public scope excludes obsolete private auto-tag/publication workflow source
contracts and the private installed marketplace index. Public CI has its own
fail-closed gate/pinned-actions/secret-input/build checks. Windows and immutable
workflow tests select the actual active workflow. Golden-path/completion matrix
inventories are validated; their duplicate recursive full-suite wrappers are not
executed because the same checks run directly in the main suite and native jobs.
A private deployed installer's byte comparison cannot match anonymized source;
real source-exact installer and candidate lifecycle jobs remain required.
All Go modules (Hub, ShellMCP, ProxyRelay, bezhook) run their own checks.
These cold platform/Docker/compiler jobs use slow-nightly category, expected600s,
maximum1800s (Go/Python maximum900s). No ordinary180s completion is claimed.

## macOS ShellMCP packages and a full release

The canonical public workflow sources are the committed overlay under
`.gitpublic/public/.github/workflows/`. The pre-push hook publishes their
sanitized snapshot; its public commit has exactly one final `Source-Commit:`
trailer pointing to the private commit. Public Product CI builds both macOS
client packages with `tools/build.sh macos-client` and verifies their Mach-O
architecture, build number, matrix, checksums and public commit. It retains the
two packages as CI artifacts; this alone does not publish a GitHub Release.

**One-time fleet alignment hold:** before changing `VERSION` or pushing a
source change that can publish the next Node package, bring every supported
Node to the currently approved version and independently confirm its installed
version. Do not change `VERSION` or start the next release before that fleet
alignment is complete. This is an operational ordering step, not a recurring
workflow approval. Once alignment is confirmed, bump `VERSION` once and push
the reviewed source. The automatic Node publisher runs after successful
`Public Product CI` for that exact `main` commit, and publishes only if the
version/source scope requires it. The full release workflow separately checks
that the matching Node release already exists for the exact public commit.

After that hold is lifted and the lead integrates the change, the release
operator uses this route:

1. Push the reviewed private `main` through the installed pre-push hook. It
   publishes the scanned public snapshot and reports its public and private
   commits. Check that the public commit's final `Source-Commit:` equals the
   approved private commit. This push must happen only after the one-time Node
   fleet alignment and `VERSION` bump described above.
2. Wait for `Public Product CI` on that exact public `main` push to succeed,
   including `Verify both macOS ShellMCP release packages`. Record its run ID.
   The Node publisher starts automatically from that successful workflow
   completion; verify the matching `node-v<VERSION>-<first 12 characters of
   public commit>` release has completed before proceeding.
3. Dispatch the public `Publish verified full release` workflow on current
   `main`, supplying the successful CI run ID and full approved private source
   commit. For example, with the reviewed values in `CI_RUN_ID` and
   `PRIVATE_SOURCE`:

   ```sh
   gh workflow run publish-full-release.yml --repo megamen32/gptadmin_opensource --ref main \
     -f ci_run_id="$CI_RUN_ID" -f private_source="$PRIVATE_SOURCE"
   ```

4. Check the workflow run through completion, then independently read the
   resulting `v<VERSION>` tag and all 18 assets. Confirm the tag points at the
   tested public commit, the release notes identify its private Source-Commit,
   and both macOS archives match the published matrix and checksum file. Check
   the existing `latest` release separately. A successful workflow dispatch is
   not a publication receipt.

The full-release workflow rejects dispatches from any ref other than current
`main`; workflow-level permissions are read-only and only the publish job gets
`contents: write`. It checks exact public CI and its macOS gate, checks both downloaded
Node packages against the published Node release and its digests, builds the
full matrix from that same public snapshot, then checks the actual release
archives again. Before creating a tag it reads the current stable `latest`
release through GitHub's API. API errors or malformed responses are UNKNOWN and
stop before mutation. It creates a draft, verifies every uploaded GitHub asset
digest, rechecks source and Node provenance, publishes with
`make_latest=false`, and reads `latest` again. A changed or unreadable readback
after publication requires reconciliation: publication may have completed, so
do not retry blindly.
An existing tag, draft, ambiguous result or failed upload must be reconciled;
do not rerun an uncertain mutation. The former `tools/release_all.sh` path is
disabled because it replaced public workflows and promoted `latest`.

The historically published FRP bootstrap credential was also present in old
public history and was confirmed in current server100 client configuration.
New source removes static credential defaults: use protected GPTADMIN_FRP_TOKEN
for CLI or BEZHOOK_TOKEN/the existing private TOML config for bezhook. Public
checks need neither. The existing local export policy scrubs and forbids that
exact legacy credential. This source repair is not revocation: a coordinated
fleet gateway/client rotation is still required; history is preserved, never
silently rewritten. No live gateway/token has been changed by this CI migration.
