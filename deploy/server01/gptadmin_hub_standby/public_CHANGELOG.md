# Changelog

## 1.0.24

- Report the failing FRP ownership check without exposing process arguments.

## 1.0.23

- Allow explicitly configured legacy public origins for existing OAuth and managed bearer credentials.

## 1.0.22

- Keep executor queue polls and their accepted callbacks visible during drain.

## 1.0.21

- Protect and recover undispatched quorum jobs after a leader change, without replaying started jobs.
- Route enrolled executor work through the quorum leader and prevent unsafe follower drain.

## 1.0.20

- Authenticate a follower’s local executor against the quorum leader over enrolled mutual TLS.

## 1.0.19

- Provide direct mutually authenticated HTTPS between configured quorum Nodes.

## 1.0.18

- Allow securely provisioned HAOS Nodes to join an explicitly enabled quorum.

## 1.0.17

- Preserve child MCP results and pending task receipts across Node routing.

## 1.0.16

- Route child MCP calls and discovery to their owning Node without unsafe retries.

## 1.0.15

- Reconcile named instructions with the writer and apply control snapshots fail-closed.

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
