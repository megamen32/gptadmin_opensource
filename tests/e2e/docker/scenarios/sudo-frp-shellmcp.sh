#!/usr/bin/env bash
set -euo pipefail
install_script="${INSTALL_SCRIPT:-/work/deploy/install.sh}"
rm -rf /opt/gptadmin /etc/gptadmin /etc/systemd/system/gptadmin-*.service
{ printf '1\n1\n1\n'; sleep 1; } | su - app -c "env PATH=/e2e/fakebin:\$PATH GPTADMIN_DOWNLOAD_QUIET=1 script -qefc 'sudo -E env PATH=/e2e/fakebin:$PATH bash $install_script' /tmp/sudo-frp-shellmcp.typescript"

test -x /usr/local/bin/gptadmin
test -f /etc/gptadmin/gptadmin.env
grep -q 'FRP_ENABLE=true' /etc/gptadmin/gptadmin.env
grep -q 'INSTALL_HUB=true' /etc/gptadmin/gptadmin.env
grep -q 'INSTALL_SHELLMCP=true' /etc/gptadmin/gptadmin.env
test -f /etc/systemd/system/gptadmin-hub.service
test -f /etc/systemd/system/gptadmin-shellmcp.service
test -f /etc/systemd/system/gptadmin-frpc.service
test -f /etc/gptadmin/frpc.toml
grep -q 'GPTAdmin Shell MCP Agent' /etc/systemd/system/gptadmin-shellmcp.service
grep -q 'subdomain =' /etc/gptadmin/frpc.toml
echo 'ok: sudo install + FRP + shellmcp service files'
