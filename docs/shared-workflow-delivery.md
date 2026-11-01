# Shared workflow delivery

The canonical engineering component is embedded from
go-hub/internal/hub/shared_workflow.md and appended at delivery time to the
selected raw InstructionSet. Persisted default and named profile content is not
rewritten. Admin editing still has its original 16 KiB profile limit. Delivery
allows that full profile plus a separately bounded 4 KiB shared component and
128 bytes of framing. Oversize/invalid input fails rather than truncating rules.

The effective content hash and existing signed/session receipts include the
component. Bootstrap responses expose instruction_version and
shared_workflow_version; these prove text delivery, not agent understanding or
business acceptance. Shared updates invalidate old receipts without changing
profile IDs, credentials, ACLs or approval modes. MCP initialize/resource text
and search/schema use the same composition for pluginless clients.

A missing named profile does not fall back to a different profile. Its startup
text reports unavailability and instruction-context execution remains fail-closed.
No production config migration is needed: deploy the reviewed binary through the
existing release path, verify current revision before replacement, then test a
real initialize and discovery request. Existing plugin installs are independent.
Actual Z.ai profile coverage must be verified from authorized profile metadata;
generic protocol tests are not Z.ai end-to-end evidence.
