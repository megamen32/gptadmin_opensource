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
#   * the ShellMCP unit is active with a live process
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

# The real installer, byte-for-byte as served publicly.
docker exec \
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

for _ in $(seq 1 20); do
  if docker exec "$CID" systemctl is-active shellmcp >/dev/null 2>&1; then
    break
  fi
  sleep 2
done
docker exec "$CID" systemctl is-active shellmcp
docker exec "$CID" pgrep -f '/opt/gptadmin/bin/shellmcp' >/dev/null
echo SHELLMCP_SERVICE_OK

echo "REAL_LINUX_INSTALL_E2E_PASS"
