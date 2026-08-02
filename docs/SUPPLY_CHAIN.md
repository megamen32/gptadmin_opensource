# Supply-chain policy

GPTAdmin release artifacts are published only after the build has produced a
versioned release manifest, an SPDX SBOM, installer-link verification and
dependency vulnerability checks. Tagged builds receive GitHub build provenance
attestations when the repository supports that GitHub feature. GitHub does not
offer attestations for user-owned private repositories; in that case the
manifest, SBOM, installer verification and vulnerability gates remain required
before any public mirror or release upload.

## Verification contract

The release manifest records the build version, commit, artifact path, size,
platform, architecture and SHA-256 digest. A consumer or operator can verify a
package from the repository root with:

```bash
python3 tools/verify_release_manifest.py verify \
  --root . --manifest build/manifest.json
```

The installer downloads the manifest before an update and rejects an artifact
when the manifest is unavailable or does not contain digest/size metadata. It
also rejects any size or SHA-256 mismatch after download. The explicit
`GPTADMIN_UPDATE_SKIP_MANIFEST=1` setting is a diagnostic-only escape for a
known private mirror; it must not be used for normal releases or production
updates.

## CI gates

The release job runs `govulncheck` for the Go modules and `npm audit` for the
admin UI lockfile before publication. Tagged public-repository builds use
`actions/attest-build-provenance` for the verified archives, manifest and SBOM.
For a user-owned private source repository, GitHub's unavailable attestation
endpoint is skipped; every other gate remains blocking for public sync and
release upload.

## Vulnerability response

- Critical vulnerabilities block release immediately and require an owner,
  mitigation or patched dependency within 24 hours.
- High vulnerabilities block the next release and require remediation within
  seven calendar days unless the owner records a time-bounded compensating
  control.
- Medium and low findings are triaged within 30 days and tracked with the
  affected component, fixed version and verification command.
- If an artifact or signing workflow is suspected compromised, stop updates,
  revoke the affected release, preserve the manifest/SBOM/attestation evidence,
  rotate the relevant publishing credential, rebuild from a clean commit and
  publish a replacement release with a new version.

The release owner records the finding and the exact immutable workflow run or
manifest path; secrets, private URLs and raw logs do not belong in the issue or
release notes.
