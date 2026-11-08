# Integration checkpoint, 8 November 2026

Published source: 296e5087d2e8786044970162819d4fd8a230b4cc (1111).
Full go-hub tests PASS: hub 316.894s, node 97.495s. Real isolated candidate
bootstrap, shell/file and revocation canary PASS. Logs are retained under
.tmp/integration1111-{go,canary}.log on node-100; these logs are not credentials.

ToDo: existing upstream registered as shared operational child `Todo`.
Public MCP project creation and card create/update/readback succeeded.
Project GPTAdmin: 0a49be76-02fb-4890-ac91-f3951f7a1723.
Card: 6a8342c5-f67e-47a1-b1de-c6522b4f71d1.
Original ChatGPT OAuth binding restored; ordinary unbound MCP read rechecked.
See TODO_CONNECTION.md for access boundaries, UI login and credential renewal.

Server88: public 1110 e0dd9c25a2b6d9f0d0e4a571e01d180792972edd installed;
ordinary shell rc0, file read and control list passed. Auth snapshot and default
instructions synchronized. Detailed receipt remains in canonical fullcycle
plan evidence/integration-20261108/public1110-88-result.md.

Remote Node SSH/Telnet remains paused by owner. Fleet convergence and legacy
retirement remain unfinished: 44, M1, Mini, HAOS and VPN2 require their native
installation/adoption paths; MacIntel transport unavailable; VUSA excluded.
Do not retire legacy services before identity/consumer adoption is verified.
Old accepted/UNKNOWN operations must not be replayed.

Preserve existing worktrees: old diagnostics and permission UI changes need
selective integration; 048e9dc6 combines unrelated UX changes and has a known
pagination regression. No broad cherry-pick or worktree deletion is authorized
by this checkpoint. Main contains the reviewed current QJS, browser ownership,
typed receipts, schema queue and bootstrap fixes.
