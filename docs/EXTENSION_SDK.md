# GPTAdmin MCP extension SDK

The extension contract lets an independently maintained adapter describe MCP
capabilities without editing Hub internals. The current schema is
`gptadmin.mcp-extension/v1`; the reference manifest is
[`tests/fixtures/mcp-extension-example.json`](../tests/fixtures/mcp-extension-example.json).

## Manifest

An extension manifest is UTF-8 JSON and must contain:

- `id`, `version`, `kind`, `protocol`, and a non-empty `entrypoint`;
- `capabilities`, where every capability has a stable `name`, human-readable
  `description`, and JSON `input_schema`;
- `scopes` and `network_needs`, which state the requested authority before
  activation;
- `provenance`, `risk_level` (`low`, `medium`, or `high`), and
  `maintenance_owner`.

`kind` is currently `stdio` or `http`, and `protocol` is `mcp-jsonrpc`.
Unknown fields may be retained by tooling, but the validator rejects missing,
malformed, or contradictory required fields. Manifests are metadata: parsing a
manifest never executes its `entrypoint`.

Validate a manifest before installation:

```bash
python3 cli.py mcp extension-validate tests/fixtures/mcp-extension-example.json
```

The command prints the validated metadata and no credentials. Validation does
not grant scopes, bypass access profiles, approve network targets, or make an
untrusted extension safe. Activation still goes through the MCP capability
catalog, Hub profile policy, approval mode, audit trail, and the selected
relay transport.

## Runtime lifecycle

An adapter must implement the ordinary MCP lifecycle over its declared
transport:

1. `initialize` negotiates the MCP protocol and reports a stable server name
   and version.
2. `tools/list` exposes only the capabilities declared by the manifest.
3. `tools/call` validates arguments against the declared input schema and
   returns a bounded JSON-RPC result or a typed error.

The Hub owns authentication, target selection, profile filtering, approval,
idempotency for write operations, and audit events. An extension must not guess
an MCP target or retry an ambiguous write without a caller-supplied bounded
`idempotency_key`.

## Conformance and evidence

The local manifest, executable stdio lifecycle, and live relay boundary are checked by
the native Go ShellMCP supervisor conformance suite using the third-party-shaped
`tests/fixtures/mcp_extension_reference.py` adapter. The fixture proves the
adapter-side `initialize -> tools/list -> tools/call` contract without editing
Hub internals, and the live test proves `discover -> schema -> execute` through
the real Hub and generic relay. This local fixture is still not third-party
certification: public ChatGPT, browser, and independently published adapter
versions remain `external_required` evidence and must be recorded separately
from local tests.

Extension authors should publish the manifest, source provenance, release
version, supported MCP protocol range, requested scopes, network policy, and a
maintainer contact together. Do not put tokens, private URLs, customer data,
or raw command arguments in manifests, logs, or support claims.
