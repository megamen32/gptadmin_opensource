# GPTAdmin deployment roles and topology

The operator describes which computers can run tasks, who can reach them and who can change policy. GPTAdmin should help the agent choose an installation, inspect the effective configuration, apply the smallest change and demonstrate the result using the operator's connection.

## Implementation and acceptance

| Work | Acceptance |
| --- | --- |
| Full installation | A fresh full installation runs one unified Node with the embedded executor. Verify the actual process and an authenticated operation on Linux, macOS and Windows. |
| Managed executor | An explicit executor installation runs standalone ShellMCP. It enrolls with its own identity, consumes authorized work and cannot change access policies or publish shared instructions with its device credential. Bootstrap and renewal must keep a fleet administrator's credential off the executor. |
| Updates | Installation and update preserve the selected role, instance name, identity, paths and membership. A deliberate migration of an existing split installation has its own rollback. |
| Peer operation | Configure two independent Nodes with explicit routes. Stop the entry node after dispatch, read the receipt from its owner and verify exactly one execution. |
| Organizational hierarchy | Configure an organization, departments and subgroups. A department's operator can use its descendants. A sibling department and global instruction publication require their own grants. Verify removals, reassignments and the effective rights of the original caller. |
| Mixed deployments | Place a standalone executor beneath a managed group alongside a full peer. Routing must preserve identity and each destination must enforce the original caller's scope. |
| Agent-assisted setup | Discover the choices via MCP. Report the installed roles, permitted actions and any missing capability. The operator need only resolve decisions that change authority or the requested scope. |
| Failure and recovery | Separate membership and global changes from owner-local operations. An outage test should identify what remains available, what fails closed and whether a previous request actually ran. |
| Release | From the merged source, exercise separate instances with distinct state, identities and ports. Record source and build revisions, functional receipts on the target machines and the normal full public CI run for the same source revision. |

[Node composition](UNIFIED_NODE_ARCHITECTURE.md) · [Peer routing](NODE_PEER_ROUTING.md) · [Access operations](ACCESS_OPERATIONS.md) · [ShellMCP](SHELLMCP.md)
