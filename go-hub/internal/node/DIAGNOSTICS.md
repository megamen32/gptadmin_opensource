# GPT-Node local memory diagnostics

Diagnostics are disabled unless `GPTADMIN_NODE_DIAGNOSTICS_SOCKET` is set in
the Node process environment. Set it to a new absolute socket path inside a
directory owned by the Node process user with mode `0700` or stricter. Startup
fails closed if the parent is not private or the socket path is occupied by a
non-socket, another user's socket, or a live listener. A stale socket is removed
only after a refused connection and an ownership/mode/inode check.
The Node creates a process-owned Unix socket with mode `0600`; no TCP listener,
Hub route, or change to existing Hub authentication is involved. The socket is
removed on normal Node shutdown. The Linux node manager preserves this optional
unknown environment entry when merging an existing node configuration, but
does not enable it by default.

Use the local OS account that owns the Node to read the socket:

```sh
curl --fail --silent --show-error --unix-socket /run/gptadmin-node/diagnostics.sock \
  http://localhost/debug/metrics
curl --fail --silent --show-error --unix-socket /run/gptadmin-node/diagnostics.sock \
  http://localhost/debug/pprof/heap -o /var/tmp/gpt-node-heap.pprof
```

The metrics response contains a fixed allowlist of Go runtime counters. Heap
profiles are serialized in memory, capped at 16 MiB, and limited to one capture
per 30 seconds. Oversized profiles fail with HTTP 413. Neither metrics nor
profiles are logged. The profile can contain process memory contents; treat the
saved file as sensitive and remove it after analysis.

This is a source change only. An already running binary cannot expose this
socket; the Node must run a newly built binary with the opt-in variable set.
