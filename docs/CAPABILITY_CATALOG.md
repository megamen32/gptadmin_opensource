# Curated capability catalog

`gptadmin mcp catalog --json` prints the bundled capability definitions before
activation. Each definition carries a stable ID/version, provenance, requested
scopes, network needs, risk level, maintenance owner and advertised tools.

`gptadmin mcp add NAME ... --catalog-id ID` binds an MCP entry to one of these
definitions and persists the catalog identity with the local configuration.
When `--install` is also requested, the command prints the definition and
requires `--accept-capability` before it can activate the relay service.

Uncurated local MCP entries remain supported for development and private
integrations, but they do not receive a catalog provenance claim. They must be
reviewed by the operator before use. The catalog is metadata and a review
boundary; Hub authentication, profile policy and approval gates remain the
authoritative security controls.

## Verification

The catalog metadata, signature check and explicit activation acknowledgement
are covered by:

```bash
python3 -m pytest tests/test_mcp_catalog.py -q
```
