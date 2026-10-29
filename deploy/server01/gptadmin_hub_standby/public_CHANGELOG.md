# Changelog

## 1.0.14

- Correct the published Home Assistant API capability for the unified Node runtime.


## 1.0.13

- Add an opt-in unified member Node with the existing HAOS shell executor.
- Keep the previous Hub runtime as the default until identity and executor state are migrated.
- Include the command-line dependencies and HAOS mounts previously provided by ShellMCP.
- Recover safely when a persisted FRP PID is reused by a different process after container restart.

## 1.0.9

- Include the Go Hub guard that keeps terminal webhook jobs from returning to `running`.

## 1.0.8

- Report failed nested MCP schema refreshes under `coverage.unreachable` while
  keeping the reachable search coverage complete.

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
