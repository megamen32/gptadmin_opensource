# Hermes egress is enabled but not listening

Status: todo
Observed: 2026-08-10

Symptom: the compact server-health probe reports
`egress_enabled=yes egress_listening=no`, while the Hermes webhook gateway is
active and `http://127.0.0.1:8644/health` returns HTTP 200.

Smallest evidence: `hermes egress status` was reduced to the two flags above;
the direct gateway health response was `{"status":"ok","platform":"webhook"}`.

Blocker: enabling or starting Hermes egress is a separate outbound/provider
mutation. It is not included in the approved credential/timer boundary and
must not be performed until the user explicitly authorizes that action.
