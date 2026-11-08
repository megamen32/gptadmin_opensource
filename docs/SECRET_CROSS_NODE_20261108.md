# Cross-node secret resolution

Bug Bdf2aac53334448158f5a was reproduced using owner-provided QA data:
`secret_status` ready and shell100 successful, but the same caller/ref on shell44
returned HTTP403 `secret not found` before execution (installed Node1112).
No value is included in evidence or this document.

Node forwarding precedes secret resolution, so an enrolled remote Node resolves
against its local store even when the reference was issued by the writer.
The fix must use the enrolled authenticated writer/member channel, validate
original caller and exact member target, and return only the requested value
for immediate authorized use. All normal execution policy gates and output
redaction must remain in effect. Never replicate the whole secret store to
ordinary executor nodes or send plaintext through tool results or logs.

The current ingress TTL expires the input form while ready values remain
resolvable. This repair preserves that existing behavior; it does not change
retention policy or promise expiry of ready values. That policy needs a
separate consistent implementation if changed.

Acceptance remains pending until the same QA reference succeeds through
ordinary public GPTAdmin shell_exec on a different physical host after install.
Unit tests and transport status alone are not this acceptance.

## Live acceptance, 2026-11-08

Delivered1113: Hub100 private26378055953db40ce92d3d0636e2717aa06d7ca4;
Nodes44/88 sanitizedcaaf31e806a58c60322de965efb3763cec10ee07.
Public CI42515717701 completed successfully. The same owner-provided QA
reference returned `QA_SECRET_AVAILABLE` and exit0 through ordinary MCP
`shell_exec` with `secret_env` on44 (operationop_2c91bfffa4a55e67) and88
(operationop_befa03c94acb2ba8). No value was printed. Node88 required its
own current instruction receipt; this was supplied through the normal context
field without changing authorization. The earlier428 attempts did not execute.

Hub update initially aborted safely on drain. Only the never-dispatched
child schema receipt6f775da245e100a866eac9913a41f461 was cancelled through
the authenticated administrative MCP API; native updater then passed drain
and installed1113 with expected-revision CAS. No business job was replayed.

A separate source-only drain fix recognizes queued child/tools/list under
the same strict predicate as tools/list (no start/result/error/parent).
Race TestNodeDrain passes; started/unknown/parent-bound operations still fence.
This follow-up is NOT in installed1113 and must enter the next normal release.
Drain counter diagnostics remain tracked separately in B8b0fddc92fcf4feda168.
