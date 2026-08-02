## 2026-07-31 — Android GPTADMIN proxy-settings probe (Direct)

- What slowed or confused L? The first approval bound an older ShellMCP identity; current private Termux identity `go-shellmcp-4af7b84b40cdbaa4` remained pending, while direct UDP DNS was blocked by the Wi-Fi network.
- Which instruction should change? none.
- Which skill, MCP, or tool is missing? Proposed: GPTADMIN should expose the identity fingerprint/server_id that an approval binds and surface queued-job delivery errors.
- What operation or error repeated? Two approvals were required for two identities; guard: recheck Hub `pending` after client replacement. UDP DNS failed repeatedly until the Hub-only LAN route override was installed.
- State: fixed now

## 2026-07-31 — Android USB recovery after Wi-Fi disable (Direct)

- What slowed or confused L? The prior control path was Wi-Fi ADB, not USB; disabling Wi-Fi removed it immediately.
- Which instruction should change? none.
- Which skill, MCP, or tool is missing? Proposed: an explicit connection inventory showing which ADB transports are physically present before a network cutover.
- What operation or error repeated? Restarting ADB alone cannot discover an absent device; guard: inspect `lsusb` first. The AMI virtual hub and then full xHCI controller were reset successfully, but no Samsung device enumerated.
- State: blocked by missing Android USB enumeration and no ShellMCP session

## 2026-07-31 — Android power-cycle recovery (Direct)

- What slowed or confused L? A powered-off phone cannot enumerate over USB or run ShellMCP, so transport resets could not restore it.
- Which instruction should change? none.
- Which skill, MCP, or tool is missing? none.
- What operation or error repeated? USB became visible immediately only after the user powered the S21 on; guard: distinguish device power state from ADB/USB faults. Hub E2E was then blocked by GPTADMIN connector reauthentication.
- State: pending GPTADMIN reauthentication for Hub E2E

## 2026-07-31 — Android ShellMCP DNS diagnosis (Direct)

- What slowed or confused L? The earlier `8.8.8.8:53` error looked like a broken Android resolver, but it bypassed the DHCP resolver and obscured the Wi-Fi egress policy.
- Which instruction should change? none.
- Which skill, MCP, or tool is missing? Proposed: a Termux-safe helper for executing a bounded command without fragile screen-input escaping.
- What operation or error repeated? Screen input rendered `%s` literally; guard: use per-key space events or a dedicated Termux runner. Android's resolver and router `192.168.2.1:53` both returned correct A records, while the OpenWrt gateway rejected external packets.
- State: fixed now

## 2026-07-31 — Canonical Android 4G proxy (Full implementation)

- What slowed or confused L? `android-4g-lan-proxy.service` exists but its script loops at `LAN_BIND could not be detected`, so active service state was not egress proof; the first bridge used the `dataSync` FGS type, which Android 15 rejects from `BOOT_COMPLETED`.
- Which instruction should change? none.
- Which skill, MCP, or tool is missing? Proposed: a focused Android 4G controller status probe that reports bridge foreground state, cellular interface, source restriction, and external egress together.
- What operation or error repeated? Wi-Fi became implicit fallback twice; guard: require a cellular `Network.getSocketFactory()` plus network-specific DNS when Wi-Fi stays enabled. Android delayed `BOOT_COMPLETED` delivery on this S21, so verify after the post-boot grace window.
- State: fixed now

## 2026-07-31 — LAN-wide Android proxy ingress (Direct)

- What slowed or confused L? none.
- Which instruction should change? none.
- Which skill, MCP, or tool is missing? none.
- What operation or error repeated? One static contract changed before implementation; red test proved removal of the former server-100-only gate.
- State: fixed now

## 2026-07-31 — Android bridge post-reboot proof (Direct)

- What slowed or confused L? ADB restarted its local daemon after the host reboot, but the USB device remained available.
- Which instruction should change? none.
- Which skill, MCP, or tool is missing? none.
- What operation or error repeated? One live health and CONNECT probe after reboot; guard: require both `rmnet4` health and external proxy egress.
- State: fixed now
