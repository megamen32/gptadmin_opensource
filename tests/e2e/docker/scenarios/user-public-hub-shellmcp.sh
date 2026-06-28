#!/usr/bin/env bash
set -euo pipefail
install_script="${INSTALL_SCRIPT:-/work/deploy/install.sh}"
hub_url="${HUB_PUBLIC_URL:-https://hub-public.example.test}"
rm -rf /home/app/.local/share/gptadmin /home/app/.local/bin/gptadmin /home/app/.config/gptadmin /home/app/.config/systemd/user/gptadmin-*.service
mkdir -p /home/app/.local/bin /home/app/.config/systemd/user
chown -R app:app /home/app
{ printf '1\n2\n%s\n1\n' "$hub_url"; sleep 1; } | su - app -c "env PATH=/e2e/fakebin:\$PATH GPTADMIN_DOWNLOAD_QUIET=1 script -qefc 'bash $install_script' /tmp/user-public-hub-shellmcp.typescript"

test -x /home/app/.local/bin/gptadmin
test -f /home/app/.config/gptadmin/gptadmin.env
grep -q "HUB_PUBLIC_URL=$hub_url" /home/app/.config/gptadmin/gptadmin.env
grep -q 'INSTALL_HUB=true' /home/app/.config/gptadmin/gptadmin.env
grep -q 'INSTALL_ROOTD=true' /home/app/.config/gptadmin/gptadmin.env
test -f /home/app/.config/systemd/user/gptadmin-hub.service
test -f /home/app/.config/systemd/user/gptadmin-shellmcp.service
grep -q 'GPTAdmin Shell MCP Agent' /home/app/.config/systemd/user/gptadmin-shellmcp.service
echo 'ok: user install + public hub + shellmcp service files'
