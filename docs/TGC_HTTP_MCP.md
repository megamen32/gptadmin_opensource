# Existing TGC tools over one owner-local HTTP service

`deploy/systemd/gptadmin-tgc-mcp-http.service` runs the existing TGC FastMCP
module with its original virtualenv through `tools/python_mcp_http.py`. It
imports a sealed copy in the same process. It does not start a stdio MCP or
translate tool definitions. The listener is loopback-only at port 9431.

The protected binding is `/var/lib/gptadmin/mcp-http/tgc-zcode100/binding.json`.
Its fields are `schema: 1`, absolute `module`, `module_sha256`, absolute
`token_file`, and `port`. The source snapshot and dedicated upstream token are
owner-only files. The original `TGC_API_BASE` and `TGC_ADMIN_COOKIE` remain in
the root-owned environment file under `/etc/gptadmin/mcp-http/`; they are not
stored in this repository. Do not substitute another client's credentials.

The service has 256 MiB high / 512 MiB hard memory, no swap, one CPU,
128 tasks, 64 MiB temporary storage plus 16 MiB `/var/tmp`, a 32 MiB file
limit, and a 12-second bounded stop. It can write only its own state directory.
There is no automatic restart. Existing TGC application services are separate.

The first real installed verification retained all 39 original schemas,
rejected an unrelated upstream credential, and completed a harmless backend
read in 0.731 seconds. This proves the HTTP service and original backend path.
It does not prove global ZCode migration or native tool consumption.

To migrate a client, use a dedicated owner-profile-fenced GPTAdmin connection
and preserve its original enabled name, identity, tool schemas and backend
authority. Verify the managed connection before changing only that client's
entry with a protected backup and compare-and-swap. Preserve loaded foreign
sessions. Retain the original client entry and service configuration for rollback.

The installed server-100 connection is `TGC-ZCode100`, exposed at
`/connection/tgc-zcode100/mcp` on the existing tenant origin. Its dedicated
`zcode-tgc-admin-100-managed` profile allows only this child target and
preserves the original tool authority. Other profiles cannot use this personal
connection. The upstream and client credentials are separate protected values.

A real authenticated managed call to the original `interaction_stats` tool
completed in 0.451 seconds. All 39 schemas matched the direct service, an
unrelated credential was rejected, and the managed session closed with HTTP
204. Only the global ZCode `tgc` entry was changed from stdio to HTTP, retaining
its enabled name and all other client settings. The service is enabled for
startup; its process generation was unchanged during registration and switching.

This is installed managed-backend acceptance. Existing native ZCode contexts
have not been reloaded, and native ZCode tool consumption remains a separate
acceptance gap. Do not repeat the accepted backend reads or reload foreign
sessions to fill that gap.

Cross-profile verification rejected the separately owned ATV credential with
HTTP 403 before TGC backend dispatch. Dedicated issuance copies and temporary
upstream/environment packets were removed after checking the target config;
protected runtime files and original rollback backups remain.
