# GrepMesh binary and systemd unit name mismatch

## Symptom

The Rust package builds `/home/admin/gptadmin/grepmesh/target/release/grepmesh`, while `grepmesh/grepmesh-mcp.service:8` starts `/usr/local/bin/grepmesh-mcp`.

## Smallest evidence

`cargo build --release --manifest-path grepmesh/Cargo.toml` exited 0; `find grepmesh/target/release -type f -perm -111` lists `grepmesh`, not `grepmesh-mcp`.

## Blocker

Systemd deployment cannot be claimed until the artifact name and unit `ExecStart` are made consistent and the focused service-start canary passes. No production mutation was made for this defect.
