# GrepMesh MCP

GrepMesh is one Rust MCP server installed independently on each host. Each
instance searches its configured roots locally, exposes the four MCP tools, and
fans global requests directly to routable peer MCP endpoints.

The local agent connects to one endpoint:

```json
{
  "mcpServers": {
    "files": {
      "type": "streamable-http",
      "url": "http://127.0.0.1:9419/mcp"
    }
  }
}
```

`hosts` defaults to `"*"`. Use `"local"` for one host or an explicit array for
selected hosts. A peer request is always rewritten to `hosts=["local"]` and
`hop_count=1`; request IDs are deduplicated for a bounded window.

Tools:

- `search_text`: literal, case-insensitive literal, or regex search with roots,
  path globs, context, host status, and partial/truncated flags.
- `find_paths`: path discovery by glob or substring.
- `read_text`: bounded line-range reads from a selected remote host.
- `search_status`: local `rg`/topology status and per-host status.

Local literal search uses the persistent trigram index to narrow candidate
files, then runs the installed `rg` binary to render exact matches, metadata,
and context lines. Regex, glob-filtered, or not-yet-ready searches fall back to
bounded `rg` over the selected roots. Binary files, excluded trees, oversized
files, symlink traversal, and sensitive credential paths are excluded by
default.

`search_text.results` contains compact ranges rather than repeated per-line
hits. A range groups only adjacent matching lines from one host and path:
`{host_id, path, start_line, end_line, matches:[{line_number,column}],
lines:[{line_number,text}]}`. `lines` contains each visible matching or context
line once, in line-number order, so the path, text, line number, and match
column metadata remain available without repeating overlapping context.

GPTAdmin is used only for the read-only topology projection at
`/mcp-relay/grepmesh`. Search and reads do not traverse GPTAdmin. If discovery
fails, the cached topology is retained and peers are marked stale/partial.

## Local canary

Build and run without installing a service:

```bash
cargo test --all-targets
cargo run -- --config ./config.example.json
```

The example is intentionally a template: replace host IDs, roots, peer URLs,
and the GPTAdmin endpoint before use. Production enrollment, credentials,
mTLS/firewall policy, ACLs, and service restart remain explicit deployment
gates.
