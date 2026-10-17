# Shared Rust routing core

GPTAdmin verifies each existing authorized node target using the public mesh-core Go binding over private Unix HTTP. Credentials, effective profiles, connection IDs, durable jobs and delivery once receipts remain owned by GPTAdmin. The core cannot grant a target or reroute a business operation.

Default configuration remains disabled. Explicit deployment sets `GPTADMIN_MESH_CORE_SOCKET=/run/mesh-core-admin/core.sock` and `GPTADMIN_MESH_CORE_HOST_ID` to the actual node identity. Enabled missing/unavailable core rejects before peer enqueue/network or managed MCP session allocation. The existing authenticated websocket and HTTP peer routing remains responsible for forwarding. No operation is retried after an uncertain write.

The core private socket accepts its operating-system owner and root only, behind a mode0700 runtime directory. The installed core has CPU25%, RAM32/64MiB, swap0 and tasks16. Root GPTAdmin and same-user Herder share this one service; no per-client core process or STDIO transport is added.

Evidence: nine local route-verifier units completed in11.073s; those units do not prove deployment or failover. Earlier whole-Hub cold test compilation exceeded its160s allowance before any tests; retained as failed setup, not GREEN. The private core is currently installed on100/44/88; core100 own-process fault/recovery passed with unchanged product runtimes. Product activation and real peer outage acceptance remain open. Mac Mini is physically unreachable; M1 artifact/delivery remains open.
