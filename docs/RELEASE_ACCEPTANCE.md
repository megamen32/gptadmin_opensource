# Release acceptance contract

GPTAdmin does not define a release as "tests passed". A release is accepted only
when the candidate artifact can install, configure, start, authenticate and
perform real work through the supported client paths.

This page is the canonical contract for the Linux runtime/release lifecycle.
Unit tests remain important, but mocks, fakes, stubbed services and synthetic
health responses are not release evidence.

## Definition of done

A runtime acceptance PASS must cross the same boundaries a real user crosses:

1. use the exact candidate artifacts for the commit under test;
2. boot a clean Linux environment with a real service manager;
3. run the real installer in unattended mode;
4. verify the installer creates the runtime configuration and starts the real
   Hub and ShellMCP processes;
5. authenticate through the supported client contract;
6. execute real commands through both Custom GPT Actions and native MCP;
7. verify the command output independently;
8. create, read, modify and delete a real file through the product;
9. verify the file side effect independently;
10. update the installed system and repeat the real execution checks;
11. reject or roll back a broken candidate without losing the previously
    working runtime;
12. only then allow release publication.

`active`, `online`, HTTP `200`, registration, discovery, schema visibility and
successful process start are useful intermediate observations. None of them is
sufficient by itself.

## Runtime heartbeat

The standard harmless runtime heartbeat is deliberately deeper than discovery.
It proves execution and filesystem effects without repeatedly writing to a
persistent disk.

### Custom GPT / Actions path

The acceptance runner must prove:

```text
/actions/openapi.yaml
  -> OAuth/JWT auth
  -> discover
  -> schema
  -> shell_exec("uptime")
  -> expected real uptime output
  -> shell_exec create/read/delete in /dev/shm
  -> file_editor create/edit/delete in /dev/shm
  -> independent verification of every side effect
```

### Native MCP path

The same candidate must independently prove:

```text
/mcp
  -> initialize
  -> tools/list
  -> tools/call shell_exec("uptime")
  -> expected real uptime output
  -> tools/call shell_exec create/read/delete in /dev/shm
  -> tools/call file_editor create/edit/delete in /dev/shm
  -> independent verification of every side effect
```

The file probes use `/dev/shm` only after verifying that it is mounted as
`tmpfs`. If `/dev/shm` is unavailable or is not tmpfs, the acceptance lane must
fail instead of silently falling back to a disk-backed path. Every probe uses a
unique filename and removes it in a `finally` cleanup path.

The purpose is not to benchmark storage. It is to prove that the complete
Hub -> protocol -> ShellMCP -> OS execution path really works.

## Clean-install lifecycle

The Linux lifecycle runner is:

```bash
tests/e2e/systemd/run-lifecycle.sh build/gptadmin-ci-full.tar.gz
```

It runs in a disposable Ubuntu environment with real systemd. The runner uses
the real `deploy/install.sh`, installs the candidate Hub and ShellMCP, verifies
the services, then executes the runtime heartbeat.

The installer must configure a bundled local ShellMCP without leaving it in an
unusable pending state. Local auto-approval is allowed only when the pending
registration cryptographically matches the ShellMCP identity generated on that
same machine.

## Update lifecycle

A successful update is not defined by a restarted service. After replacing the
runtime, GPTAdmin must prove a fresh local ShellMCP registration and complete a
real shell operation before reporting update success.

The target contract is:

```text
known-good N-1
  -> real execution PASS
  -> preserve client identity / auth material
  -> update to candidate N
  -> real execution PASS with the existing client
```

For local candidate development, the lifecycle runner may use the same candidate
archive to exercise the update mechanism itself. That is useful plumbing
coverage but does not replace the N-1 -> N release migration gate.

## Restart and reconnect contract

A supported installation must also survive ordinary lifecycle boundaries:

```text
real execution PASS
  -> restart Hub and ShellMCP
  -> reconnect / re-register
  -> real execution PASS
```

This gate is required because a service can be `active` while registration,
queue polling or job delivery is broken.

## Broken-candidate and rollback contract

A deliberately broken candidate must never convert a working installation into
a dead one. The required evidence is:

```text
working old runtime
  -> real execution PASS
  -> attempt broken update
  -> update rejected or rolled back
  -> old runtime still serves the existing client
  -> real execution PASS
```

Checking rollback log text is not sufficient. The old runtime must execute the
same harmless operation after the failure.

## Credential continuity

Changes to OAuth origin, issuer, audience, MCP resource or connection metadata
must be tested with credentials created before the update. A newly issued token
cannot prove compatibility with an existing client.

For migrations that intentionally preserve an old client origin, acceptance must
obtain the old credential first, perform the update, then use that same credential
to complete the post-update runtime heartbeat.

## Release artifact identity

The lifecycle gate must test the archive that will actually be published. The
release matrix digest and size must match the tested file. After publication, a
short consumer smoke should download the published asset and repeat at least the
real `uptime` and tmpfs create/delete probes.

A source-tree test or a different locally assembled archive does not certify a
release asset.

## CI/CD placement

The release pipeline has two distinct responsibilities:

- **CI candidate gate**: build the candidate on every relevant main-branch
  change and run the clean-install lifecycle before release packaging can be
  considered healthy;
- **CD/release gate**: run the same lifecycle against the exact Ubuntu release
  archive before publication.

Linux runtime jobs are suitable for a self-hosted runner. Platform-specific
claims must be proven on the real platform: Windows behavior on Windows, macOS
behavior on macOS, and Android/Termux behavior on Android. Cross-compilation is
not runtime acceptance.

## Test-double boundary

Mocks, fakes, stubs and monkeypatches remain valid for narrow unit tests. They
must not be promoted into evidence for installation, integration, update,
failover, deployment or release.

If a required real dependency is unavailable, report that lane as unverified or
blocked. Do not replace the dependency with a fake and keep a green release
claim.

## Current implementation and remaining gates

The repository currently contains the real-systemd clean-install lifecycle and
runtime probes under `tests/e2e/systemd/`, and the workflow invokes that lane for
candidate and Ubuntu release artifacts.

The remaining high-value release gates are:

- stable N-1 -> candidate N upgrade, not only candidate -> same candidate;
- old-client credential continuity across that update;
- explicit Hub + ShellMCP restart/reconnect execution proof;
- intentionally broken candidate with post-rollback real execution proof;
- published-asset re-download smoke;
- equivalent real-runtime lanes for supported non-Linux platforms.

Until those lanes have real evidence, they must remain described as incomplete
rather than inferred from unit or contract tests.
