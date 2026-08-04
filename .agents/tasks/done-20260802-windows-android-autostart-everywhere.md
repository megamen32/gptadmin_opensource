# Автозапуск GPTAdmin ShellMCP на Windows и Android везде

Status: completed
Class: Full

## Original request

`do Автозапуск on windows/android everywhere`

## Objective

Обеспечить единый, устойчивый и проверяемый автозапуск GPTAdmin ShellMCP на всех обнаруженных Windows- и Android-endpoint'ах пользователя без дублирующихся poller'ов, конфликтов identity и ручного переподключения после reboot/logon.

## Business canary

Для каждого подтверждённого Windows/Android endpoint существует ровно один канонический startup owner; после завершения управляющей сессии и безопасной проверки startup lifecycle Hub показывает правильный target `online`, а read-only relay canary возвращает identity/version именно этого устройства.

## Confirmed scope

- Read-only inventory всех известных Windows и Android endpoint'ов, startup owners, runtime paths, identities и Hub targets.
- Спроектировать единый контракт автозапуска с платформенными адаптерами Windows/Android.
- Исключить duplicate pollers и сохранить существующие рабочие credentials/identity.
- После выбора плана реализовать, проверить, документировать и подготовить rollback receipts.

## Explicit exclusions

- Не перезагружать Windows/Android до выбранного плана и отдельного acceptance шага.
- Не ротировать credentials, не менять firewall/ACL и не раскрывать secrets.
- Не менять unrelated MCP agents, Notify routes или legacy tasks без доказанной необходимости выбранного плана.
- Не трогать чужие изменения в shared worktree.

## Initial estimate (immutable)

- Optimistic: 35 active minutes.
- Likely: 75 active minutes.
- Pessimistic: 150 active minutes.

## Initial plan

1. Инвентаризировать все Windows/Android endpoint'ы, их текущий runtime, identity, transport и startup owner.
2. Выявить дубликаты, stale registrations и platform-specific ограничения boot/logon.
3. Представить ровно три варианта: «Максимально идеальный», «Нормальный», «YAGNI MVP» с рисками, оценками и canary.
4. Получить явный выбор пользователя и Overseer gate.
5. Реализовать выбранные стадии, проверить реальный lifecycle, оформить rollback и завершить публикацию при необходимости.

## Evidence

- Hub inventory: Windows `shell:BeyondInfinity`, `shell:DESKTOP-E55QU9Q`, and `shell:DESKTOP-I9BIG4S` are stale; Android `shell:android-SM-G998B-02SRFP` is online and `shell:android-SM-G998B-02SRFP-termux` is stale.
- Actionable Windows endpoint now: `BEYONDINFINITY` (`192.168.2.190`) reachable by trusted SSH. It has legacy `gptadmin-rootd`/`GPTAdminRootd` tasks but no canonical Go ShellMCP autostart; the restored v141 localhost process is intentionally not persistent.
- `DESKTOP-E55QU9Q` historical address `192.168.1.119` is unreachable. `DESKTOP-I9BIG4S` historical `172.18.0.1` resolves to a local bridge with port `25900` closed; device ownership/address is unproven.
- S21 `R5CR702SRFP` live receipt: exactly one shell-owned `/data/local/tmp/gptadmin/run.sh` parent (PPID 1) and one `shellmcp` child; main Hub target online. Termux, Termux:Boot and Termux:API are installed, but the Termux target is stale.
- No server-100 Android ShellMCP maintainer service/timer is installed or enabled; only the ADB server is active. The repository maintainer script can enforce one child but also changes unrelated display/background settings, so it is not a safe periodic startup owner unchanged.
- Windows canonical installer supports user `AtLogOn`, SYSTEM `AtStartup`, and Startup-folder fallback. Android generic installer supports Termux runit when available, otherwise non-persistent `nohup`.
- Explorer reports: Windows/fleet `019fc1e6-21c3-7911-8f77-76297ac8825f`; Android lifecycle `019fc1e6-1e94-7e13-9ed7-715c9441ee74`.
- Overseer planning gate: `RETHINK`; implementation is blocked until the user selects a target matrix, one startup owner per platform, legacy-task policy, preserved Android identity, and separate reboot/logon acceptance.

## Planning options awaiting selection

### Максимально идеальный

- Recover ownership/connectivity for all five Hub records (three Windows, two Android identities), add desired-state reconciliation and durable lifecycle receipts, one owner/lease per endpoint, plus real reboot/logon acceptance.
- Preserve current identities; no credential rotation. Unreachable endpoints remain blocked until accessible.
- Estimate: 180–300 active minutes plus endpoint availability.

### Нормальный (recommended)

- Scope now: confirmed `BEYONDINFINITY` and S21; E55/I9 become explicit blocked enrollment records until ownership/routes are proven.
- Windows owner: SYSTEM `AtStartup`; export/disable but do not delete legacy tasks after successful cutover. Preserve restored ZIP/runtime as rollback; replace only the temporary process after the polling target is online.
- Android owner: a new narrow server-100 systemd user maintainer for the current shell identity; no display/grant mutations and no Termux duplicate.
- Simulated lifecycle first; actual Windows/S21 reboot only with separate named approval.
- Estimate: 75–110 active minutes; 20–40 more per recovered stale Windows endpoint.

### YAGNI MVP

- Windows user `AtLogOn` task for BeyondInfinity and a minimal server-100 one-shot/timer for the current S21 shell runtime.
- No stale endpoint recovery, no legacy reconciliation, no real reboot proof, no fleet receipts/UI.
- Estimate: 25–40 active minutes. Does not fully prove `everywhere` or reboot durability.

## Human selection

- Selected: `Нормальный`.
- Explicit acceptance authorization: reboot S21 — yes; reboot Windows — yes.
- Fleet intent clarification: Windows/Android dev machines are opportunistic test targets when online; their absence must not block the working production path.
- Confirmed first-stage targets: `BEYONDINFINITY` and S21 `R5CR702SRFP`.
- Stale E55/I9 records remain enrollment candidates and are reconciled only when current ownership and connectivity are proven.
- Windows owner: SYSTEM `AtStartup`; conflicting legacy tasks may be exported and disabled, never deleted.
- Android owner: narrow server-100 maintainer preserving `shell:android-SM-G998B-02SRFP`; stale Termux identity is not started.
- The restored Windows ZIP/runtime remains as rollback; its temporary localhost process may be stopped only during successful canonical cutover.

## Pre-implementation gate answers

### Acceptance sequence and authorization

1. Windows `BEYONDINFINITY` first: preflight → backup/export → install without start → task/config invariants → canonical start → Hub/relay canary → stop only the temporary localhost runtime → reboot → post-reboot owner/Hub/relay canary.
2. On any Windows failure: stop the new task/process, restore exact task enabled states from receipts, restore backed-up binary/config, and relaunch the preserved temporary localhost runtime. Do not proceed to S21.
3. S21 second: install narrow server-100 system service/timer → no-reboot process/Hub canary → reboot S21 → wait for ADB reconnect/timer retry → post-reboot owner/Hub/relay canary.
4. On any S21 failure: disable the new timer/service and invoke the preserved existing `/data/local/tmp/gptadmin/run.sh` manually; do not touch Termux, Android 4G proxy, display, grants, or runtime config.
5. User already explicitly authorized real reboot of both `BEYONDINFINITY` and S21; no additional approval is required. Pass requires all stated device-specific post-reboot evidence. Fail closes the stage and triggers rollback.

### RED tests and invariants

- Windows dry-run contract: exact canonical task `gptadmin-shellmcp`; SYSTEM principal; `AtStartup`; existing identity/token/config reused; `gptadmin-rootd` and `GPTAdminRootd` exported before disable; no `Unregister-ScheduledTask`; unrelated `GPTAdmin MCP BeyondInfinity-windows-mcp` untouched; temporary localhost process stopped only after canonical Hub canary.
- Windows rollback contract: prior task enabled states, binary/config hashes, task XML, and temporary runtime path/PID are receipted; failure restores them without deletion.
- Android fake-ADB contract: exact serial `R5CR702SRFP`; zero owner starts canonical `run.sh`; one parent+child is no-op; duplicates consolidate to one; unavailable/unauthorized ADB exits safely for timer retry.
- Android forbidden-mutation contract: new maintainer contains no `settings put`, `appops`, `deviceidle`, `adb reverse`, `sed -i`, Termux start, runtime-config edits, credential reads, or Android 4G proxy operations.
- Acceptance contract: post-reboot exactly one canonical process/owner, preserved target identity, fresh Hub `online`, expected build/version, and explicit read-only relay response.

### Android canonical unit

- System-level server-100 units under `/etc/systemd/system`, not `systemd --user`.
- A root oneshot service invokes ADB under the established `roomhacker` ADB identity and is paired with a persistent retry timer (`OnBootSec` plus bounded recurring interval).
- It operates only on serial `R5CR702SRFP`, only on `/data/local/tmp/gptadmin/run.sh` and matching shell-owned `shellmcp`, and preserves `shell:android-SM-G998B-02SRFP`.
- The stale Termux target is never started. Existing broad maintainer script is not reused unchanged.

### Windows cutover transaction

- Conflicting legacy task names: `gptadmin-rootd` and `GPTAdminRootd`. Export XML plus enabled/state receipts, then disable only. Never delete.
- Unrelated task `GPTAdmin MCP BeyondInfinity-windows-mcp` is not modified.
- Canonical path: `C:\ProgramData\gptadmin`; exact public v141 binary and existing identity/config receive hash/backup receipts.
- Install canonical `gptadmin-shellmcp` SYSTEM `AtStartup` without starting; verify principal/trigger/config first.
- Start canonical polling task while preserved localhost-only v141 remains available; after Hub target and relay are proven, stop the temporary localhost process. Thus there is no overlap of two Hub pollers.
- Post-reboot proof: task enabled, SYSTEM principal, last result non-failing/running as expected, exactly one `shellmcp.exe` from canonical ProgramData path, preserved `shell:BeyondInfinity`, fresh Hub online state, expected build/commit, and read-only relay response.

### Overseer gate state

- Fresh pre-implementation gate returned `RETHINK` with six questions; all six are answered above. Implementation remains blocked until the same gate accepts this evidence.
- Re-audit after those answers returned `APPROVE`; implementation started.

## Windows implementation evidence and credential gate

- RED→GREEN focused contracts: initial `6 failed` on missing artifacts; implementation reached `8 passed` after explicit Hub URL and Windows launcher regressions.
- Transaction receipts were created under `C:\ProgramData\gptadmin\rollback\windows-shellmcp-autostart\`; legacy XML exports exist, both legacy tasks are disabled only, unrelated MCP task remains, and the temporary localhost runtime remains alive.
- Canonical SYSTEM `AtStartup` task and exact v141 binary are installed. Three test starts exposed and fixed: stale website Hub URL (`404`), same-file PowerShell stdout/stderr sharing violation, and PowerShell native-stderr promotion. Each received a red regression before the fix.
- Current remaining blocker: the preserved May legacy `ROOTD_TOKEN` is rejected by the current Hub queue with `401`. Hub source confirms queue auth uses the single current `SHELLMCP_TOKEN`; there is no per-server queue credential mechanism.
- Proposed minimal prerequisite: reuse the already configured current Hub `SHELLMCP_TOKEN` without rotation or generation, transfer it to the Windows reconciler only through SSH stdin (never argument/output/temp file), persist it in the existing protected ProgramData `shellmcp.env`, and record only digest equality. Rollback restores the pre-cutover env backup.
- The failing canonical task is stopped; the preserved localhost rollback process remains the only running Windows ShellMCP while this credential mutation is gated.
- Credential gate returned `APPROVE`; stdin-only RED was added, then GREEN reached `9 passed`. Current Hub credential was streamed over SSH stdin with no argument/output/temp file, and receipt reported `credential_digest_match=true` with credential redacted.
- Windows pre-reboot acceptance: canonical task `Running`, Hub target `shell:BeyondInfinity` online, read-only relay returned `BeyondInfinity`, Windows `10.0.22631.6199`, and `NT AUTHORITY\SYSTEM`. Temporary PID `543304` was stopped only after this canary; its ZIP/runtime remain. Exactly one canonical ProgramData `shellmcp.exe` remained.
- Final Windows reboot acceptance is green. Agent-resume job `gptadmin-windows-autostart-final-reboot` observed SSH down then `windows-final-up` without timeout. Post-boot OS time was `2026-08-02T20:28:10.5000000+03:00`; canonical `gptadmin-shellmcp` is enabled/running as a boot-triggered service-account task; exactly one canonical `C:\\ProgramData\\gptadmin\\bin\\shellmcp.exe` process (PID 10744) is present; both exported legacy tasks remain disabled and rollback artifacts remain present.
- Claim-relevant post-reboot relay proof: GPTAdmin discovery reported `shell:BeyondInfinity` online; schema exposed `shell_exec`; `hostname & ver & whoami` completed with return code 0 from `C:\\Windows\\system32`, returning `BeyondInfinity`, Windows `10.0.22631.6199`, and the SYSTEM principal (localized console rendering). Windows acceptance is complete, so the approved S21 stage may proceed.
- Focused cross-platform regression suite before S21 deployment: `12 passed in 1.29s`; `bash -n deploy/android-s21-shellmcp-autostart.sh` exited 0.

## S21 implementation evidence

- Preflight found the S21 physically absent from server-100: `adb devices -l` empty and no Samsung `04e8` USB device. Memory/live topology confirms this is physical disconnect, not RSA or `adbd` failure.
- Existing system `android-adb-reconnect.timer` is enabled and retrying every 30 seconds. The new narrow `android-s21-shellmcp-autostart` system service/timer was installed beside it, not as a user unit.
- Installed hashes: script `d04ce251d154dad250478af74077c51f61bb338b37d5b501bc12cf56f16ff469`; service `44a65161e5d4c99cecffb30f23f8532013fa6a6f41e6de4e0ffb8d71d2ada9ba`; timer `21999702ddb50760ff9bf5449a7cf140603a4848ba3c04a65df6891d46a2fb27`.
- Service result is success and receipt is `status=waiting`, exact serial `R5CR702SRFP`, zero process mutations. Timer is enabled/active/waiting.
- S21 reboot was not attempted while recovery transport is physically absent; doing so could strand the currently remote-only target. Agent-resume job `gptadmin-s21-autostart-adb-wait` is armed for six hours and will resume on exact ADB serial reconnect for no-op, reboot, Hub, and relay acceptance.
- Narrow S21 owner is installed on server-100: `android-s21-shellmcp-autostart.timer` is enabled and active with a 30-second retry interval; the first oneshot exited safely and wrote `status=waiting` for exact serial `R5CR702SRFP`. Deployment rollback directory is `/var/lib/gptadmin/android-s21-shellmcp-autostart/deploy-rollback/20260802T2044`.
- Current lifecycle blocker is external device transport, not the maintainer: server-100 has no attached ADB devices, `agent-device devices --json` exposes no Android target, and `lsusb` exposes no Samsung USB device. Hub still labels the main Android target online, but a fresh relay canary remains queued, so it is not accepted as live evidence. No reboot command has been sent yet.
- Cross-host transport audit also found no S21 on server-44; the documented fixed Wi-Fi ADB endpoint `192.168.2.243:5555` is unreachable from both control hosts. The Android maintainer remains enabled in the explicit safe `waiting` state and the existing 4G proxy is untouched.
- Pre-review RED exposed two Windows rollback/credential-hardening gaps: a pre-existing canonical task was exported but rollback did not restore it, and the new credential env could inherit broad ProgramData ACLs. Two focused regressions failed first; implementation now restores canonical XML/enabled state and restricts the env to SYSTEM plus local Administrators. These changes do not rotate credentials or mutate the already accepted live task.
- Concurrent resume audit found two Codex processes had been launched for the two Windows reboot watchers. The exact duplicate process was terminated after its useful Android regressions were preserved; focused tests then stabilized at `16 passed`.
- The corrected Android maintainer was deployed to `/usr/local/libexec/gptadmin/android-s21-shellmcp-autostart.sh`; after review fixes, local and installed SHA-256 both equal `698e726ce877fc5a0c0e99644a1bf79f3b8530fd89a44df58dee6291b3e961cb`. Both installed unit hashes also match local artifacts, and a no-device invocation remains `status=waiting` with all three counts zero.
- Agent-resume job `gptadmin-s21-autostart-adb-wait` is alive with its watcher and waits up to six hours for exact serial `R5CR702SRFP` on server-100 before resuming the authorized reboot acceptance. A second accidentally armed monitor was stopped to prevent duplicate resumes.
- Windows ACL runtime canary passed on a disposable file, then the existing canonical `C:\ProgramData\gptadmin\shellmcp.env` was restricted without reading its contents. Live proof reports protected inheritance and only SID `S-1-5-18` plus `S-1-5-32-544`; the previous SDDL is stored in the task rollback directory.
- Reviewer returned `CHANGES_REQUIRED` for partial-install rollback and an Android healthy-pair-plus-orphan false no-op. RED coverage was retained; Windows now writes a private pre-mutation receipt and automatically restores/removes exact artifacts, task states, and optional fallback launcher on failure. Android now requires one parent, one associated child, and one total exact executable. Focused suite is green at `20 passed`, including safe normalization of legacy v1 rollback receipts; the updated PowerShell script executed its secret-free `-DryRun` successfully on BeyondInfinity.
- Post-ACL/runtime recheck remained green: Windows canonical task is enabled and `Running`, exactly one canonical ProgramData process exists, and a fresh Hub relay returned `GPTADMIN_AUTOSTART_POST_ACL_OK` with return code 0.
- Final Reviewer returned `PASS`. Clean-main cherry-pick `b1960276e47dd54df9e78f5e7e53c11c98c91339` was pushed to `origin/main`; remote ref equality is proven. GitHub `Build, Sync, Release` run `30761028831` completed `success` for that exact SHA. Build, tests, binary generation, Android artifact verification, provenance verification, installer-link verification, and dependency scan passed. Because this was an ordinary branch push, release mirroring was intentionally skipped; no new tag, GitHub Release, or assets were created by this run.
- S21 return watcher caught one transient `R5CR702SRFP` sample, but exact preflight immediately afterward found no ADB device and `agent-device` only exposed server-100. Kernel evidence at 22:50–22:52 MSK shows repeated USB port power cycles followed by `unable to enumerate USB device`; fixed Wi-Fi ADB `192.168.2.243:5555` also remained unreachable. No reboot was sent. Agent-resume job `gptadmin-s21-adb-stable-v3` now requires five consecutive ADB samples before resuming acceptance.
- Stable return acceptance passed later: six consecutive ADB samples reported `R5CR702SRFP device`, and `agent-device devices --json` exposed physical Android `SM G998B`. `agent-device open Settings --session s21-autostart` then hung without creating that session; the existing unrelated `s21-notify-call` session was not touched. Per the project fallback rule, direct ADB is therefore used only for the transport-level reboot and the failure/reason is recorded here.
- S21 pre-reboot proof: boot ID `defd274f-2e1b-4efa-841d-a929dc981f78`; timer reconciliation receipt `status=noop` with parent/associated child/total exact executable all `1`; Hub main target online while Termux remains stale; fresh relay returned marker `GPTADMIN_S21_PRE_REBOOT_OK`, serial `R5CR702SRFP`, model `SM-G998B`, and return code 0.
- Authorized S21 reboot acceptance is complete. Watcher `gptadmin-s21-autostart-reboot-final` observed the device go down and return with new boot ID `0e54cdf5-175f-4fdc-b811-0181995851cf`, replacing pre-reboot ID `defd274f-2e1b-4efa-841d-a929dc981f78`; `sys.boot_completed=1` and `agent-device devices --json` again identified booted physical device `R5CR702SRFP` / `SM G998B`.
- Post-reboot startup-owner proof: server-100 timer remains enabled/active/waiting, service result is success, and its receipt is `status=noop` with canonical parent/associated child/total exact executable counts `1/1/1`. Process topology is one `/data/local/tmp/gptadmin/run.sh` parent owned from PID 1 and one matching `shellmcp` child.
- Fresh post-reboot Hub discovery reports the main target `shell:android-SM-G998B-02SRFP` online and the unused Termux target stale. A new background relay job completed in 59 ms with return code 0 and no stderr, returning marker `GPTADMIN_S21_POST_REBOOT_OK`, exact serial `R5CR702SRFP`, model `SM-G998B`, the new boot ID, and uptime of three minutes.

## Completion

- Normal-plan Windows and Android lifecycle acceptance is complete for the two currently confirmed dev targets: `BEYONDINFINITY` and S21 `R5CR702SRFP`.
- Stale/unreachable dev records remain opportunistic enrollment candidates and do not block the working production path, per the user's clarified fleet intent.
- Implementation is published on `origin/main` at `b1960276e47dd54df9e78f5e7e53c11c98c91339`; exact-SHA GitHub run `30761028831` is green. The ordinary branch push intentionally created no tag or release.

## Estimate revisions

- 2026-08-02 inventory revision: likely `90` active minutes, pessimistic `180`; trigger is two additional stale Windows identities with unavailable/unproven routes plus the Android shell-vs-Termux dual-owner risk. Initial estimate remains unchanged.
