# Architecture notes

The Go agent should be the durable transport/process layer:

- small single binary;
- no unbounded stdout/stderr in memory;
- stream/spool large output to files;
- process groups for cleanup;
- explicit timeouts and cancellation;
- small JSON-compatible API first, then MCP adapter.

The Python implementation remains production until the Go agent reaches parity.
