# Existing ATV tools over one owner-local HTTP service

`deploy/systemd/gptadmin-atv-mcp-http.service` imports a sealed copy of the original
ATV FastMCP module through `tools/python_mcp_http.py` in its original virtualenv.
One process listens only on loopback port 9432. The original module's tools and
backend routing are retained; no stdio MCP process or tool translation is added.

The protected binding directory is `/var/lib/gptadmin/mcp-http/atv-zcode100`.
The dedicated upstream credential is separate from the client credential.
The native module continues to use its existing sudo read of the root-owned
`/etc/telegramauto-ha/atv/runtime-v2.env` with the original exact PG identity,
TLS verification and CA. Database credentials are never copied into the client
or service environment. Consequently this unit retains the existing admin
sudo authority with `NoNewPrivileges=false`; it does not run the service as root.

The unit reserves 256 MiB high / 512 MiB hard memory, zero swap, one CPU,
128 tasks, 64 MiB temporary storage plus 16 MiB `/var/tmp`, and a 12-second stop.
Only its own state directory is writable. Original ATV application/DB services
and loaded client sessions are separate and must remain intact.

Installing this unit is not native ZCode acceptance. Verify the actual HTTP
protocol, owner-profile fence and original read-only backend path before changing
only the global enabled `atv` client entry. Preserve a protected rollback copy.

The installed `ATV-ZCode100` connection uses the dedicated
`zcode-atv-admin-100-managed` profile and `/connection/atv-zcode100/mcp`.
Its real managed `list_services(limit=1)` call completed in 0.500 seconds.
All 34 original schemas matched the direct HTTP service. An unrelated
credential received HTTP 401; the other owned TGC profile received HTTP 403
before backend dispatch. The managed session closed with HTTP 204.

Only the enabled global ZCode `atv` entry changed to HTTP/legacy. All other
client settings and loaded sessions remained untouched. The service is enabled
for startup with the same native process generation and bounded resource policy.
Native ZCode tool consumption remains unproven and must not be inferred from
these managed-backend results.

The initial nullable `get_service` verification did not produce an accepted
result: the verifier required nonempty content. That observation is retained.
A separate real original `db_health` diagnostic returned `ok=true` in the same
service, without service/source changes; the later managed read above is the
acceptance evidence. No failed/accepted calls were resubmitted.
