# Disposable canary acceptance

`tests/e2e/canary_acceptance.py` is the local process-level S3.5 gate. It
builds two real Go Hub binaries with distinct build versions, starts the old
candidate, swaps to the new candidate on the same endpoint, runs the live
health/OAuth/OpenAPI/MCP smoke after reconnect, then attempts a bad candidate
and restores the known-good binary.

Run it from the repository root:

```bash
python3 tests/e2e/canary_acceptance.py
```

The result is redacted and includes only the versions and boolean outcomes.
This proves binary swap, endpoint health, safe MCP reconnection and local
rollback semantics. It does not prove signed artifact provenance, a clean host,
systemd/launchd behavior, a public Tunnel or a third-party MCP client; those
remain separate external gates.
