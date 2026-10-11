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

Publish committed source using the installed privacy policy, preserving the
existing public history:

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
