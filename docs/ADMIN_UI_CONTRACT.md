# Admin display contract

The operator UI must not confuse transport records with user identities.
`/admin/api/clients` remains the credential inventory for existing consumers.
The UI projects it with `logicalConnections`: one OAuth registration per
`client_id`, rotating refresh credentials under read-only history. A token's
historical role is never promoted to the registration. Orphaned refresh history
is visible but is not presented as an editable registration. Individually issued
bearer connections remain distinct and carry their own IDs. No credentials are
revoked or deleted by this display projection.

`/admin/#clients?connection=<exact-id>` selects the registration/token by its
actual ID. The selected role comes from this connection, not the first raw
inventory record. Generated IDs wrap and the selection button does not repeat
the entire ID. The list supports search and an explicit inactive-record filter.
OAuth refresh history is expandable, not discarded.

`/admin/api/overview` supplies full server metadata even though the default MCP
inventory is compact. Model context optimization must not strip the UI's data.
Recent tasks are sorted newest first, with bounded input/result/error previews;
only displayed results are serialized. Overview uses 200 records. The jobs API
accepts offset/limit (maximum 500), and the UI can load older pages without
reducing stored history. Full task inspection requests `detail=full`, preserves
the outer task status and unwraps the actual result envelope. Missing legacy
input is labelled missing, never invented. Light-theme code blocks must retain
readable text/background contrast.

Regression evidence: backend metadata/sort/pagination tests, frontend duplicate
refresh grouping and exact-role-target tests, and the real Go Hub browser test
`tests/e2e/unified_admin_ui.py`. That fixture starts with 107 credential records,
8 OAuth registrations and 11 historical refresh records for the selected client.
It checks 1440/1280/1024/390px layouts for overlapping titles/buttons and horizontal
overflow, plus populated success/error results and saved-token retrieval after
restart. Those are fixture checks, not claims of production role elevation.
