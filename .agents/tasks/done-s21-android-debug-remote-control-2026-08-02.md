# S21 Android Debug and Remote Control

Status: complete; Reviewer and final Overseer APPROVE
Class: Full

## Original User Request

> Следующая цель после текущего P0: интегрировать и настроить S21 как
> полноценный Android debug/remote-control телефон. Scope: Android Remote
> Control MCP, доступ и аутентификация без публичного туннеля,
> наблюдаемость/логи, Termux/ShellMCP, диагностика сети и USB, минимальные
> безопасные привилегии, реальные end-to-end canaries. Не смешивать с Notify
> Center cellular-call fallback; это отдельная задача.

Source task: `019fb641-7ddd-7612-90ac-2fc52215c374`.

## Objective

Integrate the S21 as a private, authenticated, full-capability Android debug and
remote-control target with complete MCP tooling, diagnosable USB and network
paths, and real end-to-end canaries. Authentication and private transport are
the safety boundary; the user explicitly requires preserving full debug rights.

## Business Canary

From an authorized local/LAN agent client, identify the physical S21, enumerate
the complete Android Remote Control MCP toolset, perform real remote-control and
debug diagnostics through the canonical MCP path, observe attributed logs, and
prove that no public tunnel or Notify cellular-call fallback participates.

## Confirmed Scope

- Android Remote Control MCP topology and capability contract.
- Private access and authentication without a public tunnel.
- Bounded observability/logging needed to diagnose the selected canaries.
- Termux/ShellMCP integration.
- Network and USB diagnosis.
- Preserve the existing full Android debug permissions and unrestricted Android
  Remote Control MCP tools as explicitly required by the user.
- Real physical-device end-to-end canaries and rollback proof.
- Fix the server-100 polling maintainer that repeatedly forces the S21 display
  timeout to 15 seconds; preserve the requested 300-second timeout through at
  least one complete five-minute timer cycle.

## Explicit Exclusions

- Notify Center cellular-call fallback or any blending with that feature.
- Public ingress/tunnel exposure.
- Implementation before the current GPTADMIN receipt P0 is closed.
- Any revocation or reduction of existing Termux, ShellMCP, Accessibility,
  camera, microphone, location, media, notification, shell, input, log, ADB, or
  related debug permissions.

## Initial Active-Minute Estimate (immutable)

- Optimistic: 60 minutes
- Likely: 180 minutes
- Pessimistic: 360 minutes

## Estimate Revisions (append-only)

- 2026-08-02 — Trigger: live inventory found the upstream Android MCP app
  already installed and running with Accessibility enabled, while USB ADB and
  Termux ShellMCP are already online. Evidence also found broad runtime grants,
  no registered child MCP, a public-Hub dependency, and missing durable private
  forwarding. Revised active-minute range: optimistic 45, likely 150,
  pessimistic 300. The immutable initial estimate remains above.

## Initial Plan (RU)

1. После закрытия текущего P0 провести topology-first read-only inventory S21,
   USB/ADB, LAN, Termux/ShellMCP и существующих Android MCP-компонентов.
2. Определить business canaries, permission boundary и rollback, затем
   представить ровно три варианта: Ultimate, Normal и YAGNI MVP.
3. Дождаться отдельного выбора пользователя и обязательного Overseer audit;
   только затем реализовать выбранный этап test-first.

## Progress (EN, append-only)

- 2026-08-02: Future Full task recorded from the delegated request. It is
  intentionally queued behind the active GPTADMIN receipt P0; no research,
  device access, configuration, permission, USB, network, or runtime mutation
  has started.
- 2026-08-02: The predecessor GPTADMIN receipt P0 was independently accepted
  and closed. S21 became the active next goal; Fleet and Notify work remain
  separate and untouched.
- 2026-08-02: Graphify plus live topology inventory proved that the physical
  SM-G998B is authorized over USB ADB, host ADB listens only on loopback, and
  the Termux ShellMCP target is online as Android app UID `u0_a591` under
  `untrusted_app_27`. The Android ShellMCP has no child MCP definitions.
- 2026-08-02: The upstream `android-remote-control-mcp` release app is already
  installed as version `1.10.0`, its Accessibility service and foreground MCP
  service are active, and it listens only on phone loopback `127.0.0.1:8080`.
  A transient ADB forward bound host `127.0.0.1:18080`; an unauthenticated MCP
  initialize failed closed with HTTP 401, and the forward was removed.
- 2026-08-02: Existing deployment is not yet acceptable as the canonical path:
  the bridge script reads `HUB_PUBLIC_URL` and removes reverse port `9001`, no
  server-100 child MCP references the phone service, and camera, microphone,
  fine/background location, media-read, notification-listener and other broad
  runtime access are currently granted. Termux separately retains
  `WRITE_SECURE_SETTINGS`, `READ_LOGS`, `DUMP`, and overlay privileges.
- 2026-08-02: Upstream comparison and an independent Explorer both favor the
  on-device app for Normal because it supports localhost binding, bearer auth,
  Streamable HTTP, Accessibility control, per-tool permissions, and no root.
  Host-only Android MCP alternatives expose broader shell/automation surfaces
  and add Python/Node runtime dependencies. No persistent S21 setting, package,
  permission, registry, service, or public route was changed during research.
- 2026-08-02: Provenance is exact, not version-string-only. The installed APK is
  130951118 bytes with SHA-256
  `b1a7cf0836c232776449367ab797ecd1c04ee174daa68b948ebcadafb71b53be`,
  matching the official v1.10.0 GMS release asset. Annotated tag `v1.10.0`
  peels to source commit `38d3eff5e3257a41d90862501214122070715465`.
- 2026-08-02: Pinned-source audit found a fail-open edge in upstream tool
  permissions: the policy is a disabled-tool denylist, unknown/new tools are
  enabled, and malformed JSON falls back to the empty denylist (all tools).
  Normal therefore needs an exact authenticated `tools/list` gate: only the
  five selected tools may appear, otherwise the host removes the ADB forward
  and the canonical child becomes unavailable. Pinning the exact APK prevents
  unnoticed new tools between policy checks.
- 2026-08-02: The currently installed bridge script has no matching source in
  either `gptadmin` or `agents-projects`; Normal must first create a repo-owned,
  testable canonical source and preserve the installed script/unit plus phone
  configuration receipts for rollback. Server-100 ShellMCP runs as root and
  already reads `/etc/gptadmin/gptadmin.env`, so a dedicated root-only
  EnvironmentFile can hold the Android MCP token while the child registry keeps
  only `${ANDROID_S21_MCP_TOKEN}`.
- 2026-08-02: Human correction superseded the least-privilege proposal: this is
  a dedicated full-debug phone, so existing Android/Termux permissions and the
  complete Android MCP toolset must remain available. L also corrected an
  earlier wording error: ShellMCP had not disappeared. Live GPTADMIN discovery
  reports `shell:android-SM-G998B-02SRFP` online, and a real ShellMCP call
  returned `shellmcp_e2e=ok` from PID 3745 under Android `uid=2000(shell)` with
  input/log/ADB groups. Only its child-MCP registry was empty.
- 2026-08-02: The earlier Normal recommendation is now selected with the human
  full-debug correction. Private localhost/USB transport, authentication,
  provenance, rollback, observability, and no Notify/public tunnel remain hard
  requirements; permission/tool restriction is explicitly out of scope.
- 2026-08-02: Mandatory pre-implementation Overseer returned `APPROVE` for the
  Normal full-debug plan. It independently confirmed no hidden permission
  revocation or tool allowlist/denylist and required physical identity, 401,
  full tools/list, loopback-only transport, private ShellMCP reverse, attributed
  action logs, USB reconnect, and rollback canaries.
- 2026-08-02: TDD red established four missing-contract failures for the absent
  canonical bridge, probe, and units. The first implementation adds a pinned
  v1.10.0 full-tool Android bridge, secret-safe Streamable HTTP receipt, private
  ADB forward/reverse, phone ShellMCP private-Hub reload, and bounded systemd
  timer. Focused tests now pass `4 passed`; `bash -n`, Python compilation and
  diff checks pass. `shellcheck` is not installed locally, so no ShellCheck
  claim is made. Production deployment has not started and waits for Reviewer.
- 2026-08-02: First activation of the obsolete USB-relay design failed closed
  at authenticated initialize and rolled back before becoming canonical. The
  old timer and phone outbound ShellMCP polling remained active; a live phone
  ShellMCP call still succeeded as Android `uid=2000(shell)` with input, log,
  ADB, network and storage groups. A bearer value was exposed by sudo command
  auditing during the failed attempt, so that credential is rejected for the
  final deployment and must be rotated without argv logging.
- 2026-08-02: Human explicitly rejected USB reverse/forward as a runtime
  architecture and clarified that outbound polling is the fleet default. This
  supersedes the earlier Overseer-approved USB design. The corrected topology
  keeps the existing S21 ShellMCP long-poll connection to Hub and registers the
  localhost Android MCP as a persistent child of that phone ShellMCP. USB is
  bootstrap/diagnostics only, never a runtime dependency.
- 2026-08-02: Delegated live evidence identified a related timeout regression:
  every five minutes server-100 wrote `screen_off_timeout 15000` over ADB. L
  confirmed the exact source in the installed legacy polling maintainer and its
  `OnUnitActiveSec=300s` timer. A rollback copy with SHA-256 receipt was saved
  under `/var/backups/gptadmin/android-s21-timeout-20260802T052703Z`; the writer
  now enforces `300000`. Manual service execution and a live S21 ShellMCP read
  returned `300000`; acceptance still requires the next automatic timer cycle.
- 2026-08-02: The corrected polling bootstrap was developed test-first. Two
  fail-closed production attempts exposed and fixed missing-config handling and
  root-only staging permissions before Android auth changed. A later reload
  exposed seven historical shell polling parents; exact Android `ps` matching
  now consolidates them deterministically without treating the separate Termux
  UID as a duplicate. Focused suite is `9 passed`; both shell scripts pass
  `bash -n`, the ARM64 status probe builds, and `git diff --check` is clean.
- 2026-08-02: Production polling topology is live. The S21 shell poller keeps
  its existing authenticated outbound Hub URL and `SHELLMCP_MODE=long_poll`.
  It loads a mode-0600 phone env and persistent MCP registry containing only a
  `${ANDROID_S21_MCP_TOKEN}` header placeholder; `AndroidRemoteControl-S21`
  points to phone-local `http://127.0.0.1:8080/mcp`. There is no MCP forward or
  reverse; the remaining ADB forward is the unrelated canonical Android 4G
  proxy. Current auth material is absent from host journal, phone logcat and
  MCP JSON.
- 2026-08-02: Canonical Hub canaries passed with exactly 58 Android tools and
  tools digest `cfaa792fa4a9585a461922fd51d9d61000f7ae4b7273b2d2eac3cb42e8198bfa`.
  A real `android_s21_get_screen_state` returned the 720x1600 physical UI tree,
  and `android_s21_press_home` completed successfully. A phone-local static
  diagnostic returned unauthenticated=401, compromised-old-token=401 and
  current-token=200, then removed both the probe and temporary old-token file.
- 2026-08-02: Full-debug access was preserved. Granted-permission digest before
  and after is `29354154a389fa0f49af9ff7c0167ffd696229d64b7106ac2231ef653d801801`;
  `ACCESS_RESTRICTED_SETTINGS` was expanded to `allow` because Samsung had the
  Accessibility service enabled but refused to bind it. An S21-only reboot
  proved Android MCP auto-start, both Accessibility services bound, one shell
  polling parent/child, persistent phone-local child config and timeout 300000.
- 2026-08-02: Runtime USB independence passed. With the server-100 ADB server
  and maintainer timer stopped, the Hub still returned shell UID 2000 and a
  fresh Android screen-state through the phone-local child. ADB, the unrelated
  4G proxy and timer were then restored active. A broken Termux duplicate with
  the same Hub identity was disabled recoverably after its logs proved only FRP
  404 polling; the Termux app, data and permissions remain untouched.
- 2026-08-02: The timeout correction passed a full automatic timer cycle at
  08:32:03 with live value 300000 at 08:32:24, and the new canonical maintainer
  passed again at 09:22:26 with `Result=success`. Repo-owned maintainer and unit
  sources now enforce 300000 and exactly one shell-UID poller. The historical
  unsafe 15000 copy is explicitly named `historical-unsafe-do-not-restore`;
  the normal rollback filename contains the corrected 300000 source.
- 2026-08-02: Independent final Reviewer returned `APPROVE`. Final Overseer
  returned `APPROVE`, accepting the phone-local Android MCP over outbound
  long-poll, preserved full-debug access, auth rotation, USB-off proof and real
  end-to-end canaries. Notify cellular-call fallback and Fleet work were not
  modified by this task.

## Selected Normal Full-Debug Design (RU)

- Сохранить все текущие Android, Accessibility, Termux и ShellMCP права; ничего
  не отзывать и не ограничивать в Android Remote Control MCP.
- Оставить приложение на `127.0.0.1:8080` с обязательным bearer token и без
  Cloudflare/ngrok/public tunnel; Android MCP доступен только локальному
  ShellMCP на самом телефоне.
- Сохранить существующий исходящий `SHELLMCP_MODE=long_poll` S21→Hub; не менять
  его `HUB_URL` и не создавать `adb reverse`/`adb forward`.
- Задать телефону постоянный `SHELLMCP_MCP_CONFIG`, сохранить в нём полный
  Streamable HTTP child `AndroidRemoteControl-S21` на `127.0.0.1:8080`, а
  bearer подставлять из отдельного mode-0600 env-файла только при запросе.
- Канонизировать bootstrap/reconcile script, добавить rollback-копии,
  USB-диагностику и redacted structured logs; USB не участвует в runtime.
- Проверять APK hash/version, auth 401, полный `tools/list` digest и доступность
  всех upstream tools как observability/compatibility contract. Не отключать
  канонический child из-за появления новых tools в явно выбранном full-debug
  режиме.

## Historical Architecture Options (RU, superseded)

These pre-correction options are retained as append-only decision evidence.
Every USB runtime transport below is superseded by the selected polling-only
design above and must not be implemented.

### ultimate perfect totally ideal

- Fork and pin the Android app, build a signed reproducible APK with SBOM and a
  project-owned restricted tool policy enforced in code.
- Use phone localhost plus USB ADB forwarding, dedicated credential rotation,
  explicit host policy, structured audit export, Samsung reboot/battery/USB
  reconnect tests, bounded self-healing, and LAN failover that never opens a
  public route.
- Revoke all unrelated Android and Termux privileges after dependency canaries;
  add fault-injection, rollback, retention, and release automation.
- Revised estimate: 300-600 active minutes.

### normal (selected, superseded by full-debug correction above)

- Keep pinned upstream `android-remote-control-mcp` v1.10.0, bind only
  `127.0.0.1:8080`, require a dedicated bearer token, disable OAuth/public
  tunnel/boot auto-start initially, and expose it only through durable host
  loopback `adb forward` on a dedicated port.
- Register one server-100 ShellMCP Streamable HTTP child using an environment
  reference for Authorization; keep the raw credential out of MCP JSON, logs,
  command output, and repository files.
- Preserve and expose the complete upstream Android Remote Control MCP toolset,
  including files, downloads, camera/mic, notifications, location, intents,
  clipboard, typing, gestures and app-management capabilities. Preserve full
  Android shell/debug access through the separate live ShellMCP target.
- After every app/config/start/reconnect, perform authenticated `tools/list`,
  record the sorted set/digest, and alert on compatibility drift without
  silently restricting the explicitly requested full-debug surface.
- Preserve all existing Android MCP and Termux grants; test and report them as
  debug capability evidence rather than revoke them.
- Replace the phone ShellMCP public-Hub dependency with private USB
  `adb reverse tcp:9001 tcp:9001`; add redacted service health, MCP-call audit,
  ADB/USB state, bounded logcat and restart evidence.
- Canary: exact physical identity fingerprint, unauthorized 401, authenticated
  tools/list, screen-state without screenshot, open Calculator, one harmless
  tap/back/home sequence, attributed logs, loopback-only listeners, private Hub
  path, USB reconnect recovery, and rollback to saved app/settings/scripts.
- Revised estimate: 120-240 active minutes.

### YAGNI MVP

- Keep the already installed app and Accessibility service; configure a fresh
  bearer token, phone localhost binding, one temporary/durable USB ADB forward,
  and one server-100 Streamable HTTP child.
- Disable every tool except screen-state, tap, back/home and app-open; revoke
  camera/mic/location/media/notification grants. Replace the existing phone
  ShellMCP public-Hub URL with private USB `adb reverse tcp:9001 tcp:9001`, but
  defer the broader Termux privilege sweep and automatic recovery pipeline.
- Prove unauthorized 401, authenticated screen-state, Calculator open/back,
  private ShellMCP heartbeat, loopback-only exposure, and manual rollback.
- Revised estimate: 60-120 active minutes.
