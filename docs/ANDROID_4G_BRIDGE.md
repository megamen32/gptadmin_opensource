# Android 4G bridge

The Samsung benchmark device is the mobile-path witness for VPN testing. Its
physical USB ADB connection is authoritative. Wi-Fi ADB is only a fallback
when no USB device is connected.

The bridge selects a connected ADB device with a `usb:` transport first, then
uses `ANDROID_ADB_SERIAL` from `/etc/gptadmin/gptadmin.env` only if USB is
unavailable. This prevents an obsolete Wi-Fi address from hiding a plugged-in
phone.

`android4gproxy` currently uses ordinary Android sockets. Before it starts, the
bridge persistently sets `wifi_on=0`, disables Wi-Fi, and requires the probe
route to use an `rmnet` cellular interface. A running service or a validated
cellular network alone is not LTE proof when Wi-Fi is also connected.

## Recovery check

On `roomhacker-server-100`:

```bash
adb kill-server
adb start-server
adb devices -l
sudo systemctl restart android-4g-lan-proxy.service
```

The Samsung serial must show `device usb:`. Confirm that the route to the probe
address uses `rmnet` and that the proxy egress differs from the LAN/direct
egress before treating a bridge result as a 4G result. Do not use a running
systemd unit alone as proof: check the bridge journal and the listener it
reports.

If the phone is intentionally absent, leave the Android test target disabled;
an Android-only failure must be reported as a mobile-path observation and must
not trigger ingress failover.
