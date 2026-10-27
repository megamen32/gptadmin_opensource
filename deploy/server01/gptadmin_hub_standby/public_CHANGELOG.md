# Changelog

## 1.0.7

- Publish the ARM64 image from the linked public add-on package so the public
  repository workflow can update it with its scoped `GITHUB_TOKEN`.

## 1.0.6

- Add an expiring `failover.maintenance_until` window for planned maintenance.
- Keep signed reclaim available during maintenance and resume promotion checks
  automatically when the timestamp expires.

## 1.0.5

- First public Home Assistant Apps repository package.
- Adds secret-free Supervisor configuration and persistent internal credential generation.
- Includes independent HAOS watchdog, fallback proxy and ARM64 Tunnel client.
