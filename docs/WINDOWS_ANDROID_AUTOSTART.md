# Windows and Android ShellMCP autostart

This runbook defines one startup owner for each confirmed development endpoint.
Offline development machines are opportunistic test targets and never block the
working fleet.

## Windows

`deploy/windows-shellmcp-autostart-reconcile.ps1` installs the public Windows
ShellMCP as the SYSTEM `gptadmin-shellmcp` task with an `AtStartup` trigger. It
reuses the existing Hub credential and identity, exports the legacy
`gptadmin-rootd` and `GPTAdminRootd` task definitions, then disables those tasks
without deleting them. The unrelated generic MCP task is not changed.

The command emits a secret-free JSON receipt. Keep its receipt directory for
rollback. Pass `-RollbackReceipt <receipt.json>` to disable the new owner,
restore backed-up files, restore any pre-existing canonical task, and restore
legacy enabled states. The persisted credential file is readable only by
SYSTEM and the local Administrators group.

The receipt and protected backup tree are written before target mutation. A
failed install automatically stops the exact canonical owner, restores or
removes files according to their pre-cutover existence, restores task states,
and launches an explicitly supplied `-FallbackLauncherPath` when configured.

Acceptance requires a real reboot, one `shellmcp.exe` from the canonical
ProgramData path, SYSTEM/AtStartup task evidence, a fresh Hub target, and a
read-only relay call.

## Android S21

`deploy/android-s21-shellmcp-autostart.sh` is intentionally narrower than the
historical polling maintainer. It only inspects and reconciles the exact serial
`R5CR702SRFP`, `/data/local/tmp/gptadmin/run.sh`, and its shell-owned ShellMCP
child. It does not edit runtime configuration, permissions, display state,
routes, Android 4G proxy state, credentials, or Termux services.

Install the script under `/usr/local/libexec/gptadmin/` and the supplied system
service/timer under `/etc/systemd/system/`. The persistent timer retries when
ADB is unavailable and converges duplicate or missing owners to one parent and
one associated child. Acceptance also requires exactly one process using the
canonical `$BASE/bin/shellmcp` executable, so an orphan duplicate forces
reconciliation instead of a false no-op.

Acceptance requires a real S21 reboot, ADB reconnect, one parent plus one child,
the preserved `shell:android-SM-G998B-02SRFP` identity online in Hub, and a
read-only relay call. Disable the timer/service to roll back; the existing phone
runtime remains available for a manual launch.

An offline phone is a safe waiting state, not a successful reboot canary. The
timer remains enabled and `latest.json` records `status=waiting`; acceptance is
completed only after the exact physical serial returns through ADB and all
post-reboot checks above pass.
