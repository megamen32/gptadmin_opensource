#!/usr/bin/env bash
# Real black-box Linux install test for the public GPTAdmin installer.
#
# Boots the tests/e2e/install/Dockerfile image (ubuntu + systemd as PID1),
# runs the REAL install.sh exactly as a new user would fetch it, in silent
# system mode against no tunnel, and verifies the installed product with
# real operations only:
#
#   * the CLI is on PATH and executes
#   * /etc/gptadmin/gptadmin.env exists and carries a locally generated
#     SHELLMCP_TOKEN (self-service credential, never operator-supplied)
#   * the Hub systemd unit is active and /healthz answers ok:true
#   * the published installation runs the executable matching its recorded role
#
# Usage: tests/e2e/install/run-install-e2e.sh [installer-url]
# Requires: docker. Exits non-zero on the first failed real assertion.

set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
INSTALL_URL="${1:-https://became.bezrabotnyi.com/install.sh}"
TAG="gptadmin-e2e-installer"

docker build -q -t "$TAG" "$HERE"

CID="$(docker run -d --privileged --cgroupns=host -v /sys/fs/cgroup:/sys/fs/cgroup:rw --stop-timeout 15 "$TAG")"
trap 'docker rm -f "$CID" >/dev/null 2>&1 || true' EXIT

for _ in $(seq 1 30); do
  state="$(docker exec "$CID" systemctl is-system-running 2>/dev/null || true)"
  case "$state" in running|degraded) break ;; esac
  sleep 2
done
docker exec "$CID" systemctl is-system-running

# The bootstrap script, its CLI and its packages must refer to one published
# release. While this push is in CI, the next full release does not exist yet.
release_env=()
if [[ $# -eq 0 ]]; then
  repo_root="$(cd "$HERE/../../.." && pwd)"
  version="$(cd "$repo_root" && python3 -c 'import cli; print(cli._published_full_release()[0])')"
  release_env=(-e "CLI_URL=https://raw.githubusercontent.com/megamen32/gptadmin_opensource/v$version/cli.py"
               -e "RELEASES_URL=https://github.com/megamen32/gptadmin_opensource/releases/download/v$version")
fi
docker exec "${release_env[@]}" \
  -e GPTADMIN_INSTALL_SILENT=1 \
  -e GPTADMIN_SETUP_TUNNEL=none \
  -e GPTADMIN_SETUP_GREPMESH=0 \
  -e GPTADMIN_INSTALL_MODE=system \
  "$CID" bash -ceu "curl -fsSL '$INSTALL_URL' -o /tmp/install.sh && bash /tmp/install.sh"

docker exec "$CID" bash -ceu '
  set -euo pipefail
  test -x /usr/local/bin/gptadmin
  test -d /opt/gptadmin/bin
  test -f /etc/gptadmin/gptadmin.env
  /usr/local/bin/gptadmin --help >/dev/null
  grep -q "^SHELLMCP_TOKEN=" /etc/gptadmin/gptadmin.env
  echo INSTALLER_OUTPUT_OK
'

for _ in $(seq 1 30); do
  if docker exec "$CID" curl -fsS -m 2 http://127.0.0.1:9001/healthz 2>/dev/null | grep -q "\"ok\":true"; then
    break
  fi
  sleep 2
done
docker exec "$CID" systemctl is-active gptadmin-hub
docker exec "$CID" curl -fsS -m 3 http://127.0.0.1:9001/healthz | grep -q '"ok":true'
echo HUB_HEALTHZ_OK

# This smoke test follows the currently published release. The separate exact
# candidate lifecycle always requires a unified process, including before release.
docker exec "$CID" bash -ceu '
  if grep -qx GPTADMIN_INSTALL_ROLE=node /etc/gptadmin/gptadmin.env; then
    pid=$(systemctl show -p MainPID --value gptadmin-hub.service)
    test "$(readlink "/proc/$pid/exe")" = /opt/gptadmin/bin/gptadmin-node
    ! systemctl is-active --quiet shellmcp || exit 1
    echo PUBLISHED_UNIFIED_NODE_OK
  else
    ! grep -q "^GPTADMIN_INSTALL_ROLE=" /etc/gptadmin/gptadmin.env || exit 1
    systemctl is-active --quiet shellmcp
    pgrep -f "/opt/gptadmin/bin/shellmcp" >/dev/null
    echo PUBLISHED_LEGACY_INSTALLATION_OK
  fi
'

echo "REAL_LINUX_INSTALL_E2E_PASS"
