# Public CI

Product checks run in `megamen32/gptadmin_opensource`, using standard public
GitHub-hosted runners. Private `megamen32/gptadmin` Actions are disabled.
There is no private-source checkout, credential input or self-hosted fleet runner
in public workflows. One source push runs product checks once; no auto-tag loop
dispatches a second complete release for every commit. Exact Node artifact
retention is one day. Release publication remains a separate explicit action.

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

The historically published FRP bootstrap credential was also present in old
public history and was confirmed in current server100 client configuration.
New source removes static credential defaults: use protected GPTADMIN_FRP_TOKEN
for CLI or BEZHOOK_TOKEN/the existing private TOML config for bezhook. Public
checks need neither. The existing local export policy scrubs and forbids that
exact legacy credential. This source repair is not revocation: a coordinated
fleet gateway/client rotation is still required; history is preserved, never
silently rewritten. No live gateway/token has been changed by this CI migration.
