#!/usr/bin/env bash
set -euo pipefail
repo="$(cd "$(dirname "$0")/../../.." && pwd)"
package="${1:-$repo/build/gptadmin-ci-full.tar.gz}"
[[ -f "$package" ]] || { echo "missing candidate package: $package" >&2; exit 2; }
package="$(realpath "$package")"
package_dir="$(dirname "$package")"
package_name="$(basename "$package")"
matrix="$package_dir/gptadmin-release-matrix.json"

# Candidate CI archives are intentionally not public release assets yet, but
# update verification must still exercise digest/size enforcement.  Give that
# private candidate a one-entry matrix.  Real release bundles must arrive with
# the release matrix produced by tools/build.sh; never synthesize that case.
if [ "$package_name" = "gptadmin-ci-full.tar.gz" ]; then
  python3 - "$package" "$matrix" "$repo" <<'PY_MATRIX'
import hashlib, json, pathlib, subprocess, sys
pkg = pathlib.Path(sys.argv[1])
out = pathlib.Path(sys.argv[2])
repo = pathlib.Path(sys.argv[3])
version_text = (repo / "VERSION").read_text().strip()
try:
    build_version = int(version_text)
except ValueError:
    build_version = 1
try:
    commit = subprocess.check_output(["git", "-C", str(repo), "rev-parse", "HEAD"], text=True).strip()
except Exception:
    commit = "candidate"
payload = {
    "schema": "gptadmin.release-matrix/v2",
    "build_version": build_version,
    "git_commit": commit,
    "artifacts": [{
        "platform": "ubuntu", "arch": "x64", "edition": "full",
        "file": pkg.name, "sha256": hashlib.sha256(pkg.read_bytes()).hexdigest(),
        "size": pkg.stat().st_size,
    }],
}
out.write_text(json.dumps(payload, indent=2) + "\n")
PY_MATRIX
else
  python3 - "$package" "$matrix" <<'PY_MATRIX_CHECK'
import hashlib, json, pathlib, sys
pkg = pathlib.Path(sys.argv[1]); matrix_path = pathlib.Path(sys.argv[2])
if not matrix_path.is_file():
    raise SystemExit(f"release lifecycle requires matrix: {matrix_path}")
matrix = json.loads(matrix_path.read_text())
entry = next((x for x in matrix.get("artifacts", []) if isinstance(x, dict) and x.get("file") == pkg.name), None)
if not entry:
    raise SystemExit(f"release matrix has no entry for {pkg.name}")
actual_sha = hashlib.sha256(pkg.read_bytes()).hexdigest(); actual_size = pkg.stat().st_size
if entry.get("sha256") != actual_sha or int(entry.get("size", -1)) != actual_size:
    raise SystemExit(f"release matrix digest/size mismatch for {pkg.name}")
PY_MATRIX_CHECK
fi

name="gptadmin-lifecycle-${GITHUB_RUN_ID:-local}-$$"
image="gptadmin-systemd-acceptance:24.04"
cleanup(){
  status=$?
  if [ "$status" -ne 0 ]; then
    echo "=== lifecycle failure diagnostics ===" >&2
    docker exec "$name" systemctl status gptadmin-hub.service shellmcp.service gptadmin-grepmesh-mcp.service --no-pager -l >&2 || true
    echo "=== shellmcp journal ===" >&2
    docker exec "$name" journalctl -u shellmcp.service -n 120 --no-pager >&2 || true
    echo "=== hub journal ===" >&2
    docker exec "$name" journalctl -u gptadmin-hub.service -n 120 --no-pager >&2 || true
  fi
  docker rm -f "$name" >/dev/null 2>&1 || true
  return "$status"
}
trap cleanup EXIT

docker build -q -t "$image" "$repo/tests/e2e/systemd" >/dev/null
docker run -d --name "$name" --privileged --cgroupns=host \
  -v /sys/fs/cgroup:/sys/fs/cgroup:rw \
  -v "$repo:/work:ro" \
  "$image" >/dev/null

for _ in $(seq 1 30); do
  docker exec "$name" systemctl is-system-running >/dev/null 2>&1 && break
  sleep .5
done

docker exec "$name" env \
  CLI_URL=file:///work/cli.py \
  PKG_ALL_URL="file:///work/${package#$repo/}" \
  PKG_HUB_URL="file:///work/${package#$repo/}" \
  PKG_SHELLMCP_URL="file:///work/${package#$repo/}" \
  GPTADMIN_DOWNLOAD_QUIET=1 \
  GPTADMIN_INSTALL_ACTION=setup \
  GPTADMIN_INSTALL_SILENT=1 \
  GPTADMIN_SETUP_TUNNEL=none \
  GPTADMIN_AUTO_APPROVE_LOCAL_SHELLMCP=1 \
  bash /work/deploy/install.sh

docker exec "$name" systemctl is-active --quiet gptadmin-hub.service
docker exec "$name" systemctl is-active --quiet shellmcp.service
docker exec "$name" systemctl is-active --quiet gptadmin-grepmesh-mcp.service
docker exec "$name" test -s /etc/gptadmin/gptadmin.env
docker exec "$name" test -x /opt/gptadmin/bin/gptadmin_hub
docker exec "$name" test -x /opt/gptadmin/bin/shellmcp
docker exec "$name" test -x /opt/gptadmin/bin/grepmesh-mcp

docker exec "$name" python3 /work/tests/e2e/systemd/runtime_acceptance.py

# Exercise the installed updater against the same candidate artifact. This is
# deliberately after real execution: update must preserve a functioning install.
# The updater must accept ordinary local filesystem paths. It discovers the
# sibling gptadmin-release-matrix.json itself and still enforces digest/size;
# no file:// spelling or manifest bypass is allowed in this gate.
docker exec "$name" /usr/local/bin/gptadmin --system update --force \
    --pkg-all "/work/${package#$repo/}" \
    --pkg-hub "/work/${package#$repo/}" \
    --pkg-shellmcp "/work/${package#$repo/}"

docker exec "$name" systemctl is-active --quiet gptadmin-hub.service
docker exec "$name" systemctl is-active --quiet shellmcp.service
docker exec "$name" systemctl is-active --quiet gptadmin-grepmesh-mcp.service
docker exec "$name" env GPTADMIN_ACCEPTANCE_MARKER=gptadmin-post-update python3 /work/tests/e2e/systemd/runtime_acceptance.py

echo "ok: clean install + autoconfigure + real Custom GPT/MCP execution + update + real execution"
