#!/usr/bin/env bash
set -euo pipefail
install_script="${INSTALL_SCRIPT:-/work/deploy/install.sh}"
rm -rf /opt/gptadmin /etc/gptadmin /etc/systemd/system/gptadmin-*.service
python3 - <<'PY' >/e2e/out/local-hub.log 2>&1 &
from http.server import BaseHTTPRequestHandler, HTTPServer


class HealthHandler(BaseHTTPRequestHandler):
    def do_GET(self):
        body = b'{"name":"gptadmin_hub"}'
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_args):
        pass


HTTPServer(("127.0.0.1", 9001), HealthHandler).serve_forever()
PY
health_pid=$!
trap 'kill "$health_pid" 2>/dev/null || true' EXIT
{
  printf '1\n1\n1\n'
  sleep 2
  printf 'y\n'
  sleep 1
} | su - app -c "env PATH=/e2e/fakebin:\$PATH CLI_URL=file:///work/cli.py GPTADMIN_DOWNLOAD_QUIET=1 script -qefc 'sudo -E env PATH=/e2e/fakebin:$PATH CLI_URL=file:///work/cli.py bash $install_script' /tmp/sudo-frp-shellmcp.typescript"

test -x /usr/local/bin/gptadmin
test -f /etc/gptadmin/gptadmin.env
grep -q 'FRP_ENABLE=true' /etc/gptadmin/gptadmin.env
grep -q 'INSTALL_HUB=true' /etc/gptadmin/gptadmin.env
grep -q 'INSTALL_SHELLMCP=true' /etc/gptadmin/gptadmin.env
test -f /etc/systemd/system/gptadmin-hub.service
test -f /etc/systemd/system/shellmcp.service
test -f /etc/systemd/system/gptadmin-tunnel-frpc.service
test -f /etc/gptadmin/frpc.toml
grep -q 'GPTAdmin Shell MCP Agent' /etc/systemd/system/shellmcp.service
grep -q 'subdomain =' /etc/gptadmin/frpc.toml
echo 'ok: sudo install + FRP + shellmcp service files'
