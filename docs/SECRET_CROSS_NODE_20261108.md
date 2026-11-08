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
