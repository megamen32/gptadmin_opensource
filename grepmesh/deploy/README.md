# GrepMesh rollout

`manifest.two-node.json` is the first real vertical-slice manifest for
server-100 and server-88. It uses the known LAN/VPN addresses, keeps agent
entrypoints on `127.0.0.1:9419`, and uses the management listener only for
peer-to-peer calls.

The manifest intentionally contains no token. Before `apply`, the operator
must provision the same `GREPMESH_PEER_TOKEN` in
`/etc/grepmesh-mcp/peer.env` on both hosts and confirm the firewall/VPN
allowlist. The rollout helper refuses to mutate a host when that file is
missing.

The service runs as `admin-search` and receives the `admin`
supplementary group at process start. This is required on server-88 where
`/home/admin` is `0750`; it grants only the files already readable by that
group, while explicit secret excludes remain active.

Commands:

```bash
python3 grepmesh/deploy/rollout.py preview \
  --manifest grepmesh/deploy/manifest.two-node.json

python3 grepmesh/deploy/rollout.py verify \
  --manifest grepmesh/deploy/manifest.two-node.json

python3 grepmesh/deploy/rollout.py rollback \
  --manifest grepmesh/deploy/manifest.two-node.json \
  --confirm GREPMESH-ROLLBACK-<value-from-manifest>
```

`apply` requires the exact `confirmation` emitted by `preview`, a matching
committed GrepMesh source tree/artifact, passwordless sudo on the target, and the
pre-provisioned peer token. It backs up the existing binary/config/unit under a
per-apply directory in the state directory and writes an install receipt with
the backup location and availability flags. `rollback` requires its own exact
confirmation and restores only a complete previous installation, then records
`last-rollback.json`.

GPTAdmin/ShellMCP is an approved operator transport for the target commands;
the GrepMesh peer traffic itself remains direct and bearer-authenticated over
the management/VPN listener. The production apply gate is separate from local
build and test results.
