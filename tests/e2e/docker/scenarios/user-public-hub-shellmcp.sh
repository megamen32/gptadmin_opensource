#!/usr/bin/env bash
set -euo pipefail
install_script="${INSTALL_SCRIPT:-/work/deploy/install.sh}"
hub_url="${HUB_PUBLIC_URL:-https://hub-public.example.test}"
rm -rf /home/app/.local/share/gptadmin /home/app/.local/bin/gptadmin /home/app/.config/gptadmin /home/app/.config/systemd/user/gptadmin-*.service
mkdir -p /home/app/.local/bin /home/app/.config/systemd/user
chown -R app:app /home/app

# The installer now verifies the local Hub before enabling dependent units. This
# scenario validates installer wiring rather than Hub runtime, so provide the
# smallest deterministic health contract on the expected local port.
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
  printf '1\n2\n%s\n' "$hub_url"
  # The bootstrap downloads the CLI before entering its second interactive
  # phase. Keep the transport answer available after that phase starts so the
  # PTY-backed `script` wrapper cannot consume it while curl is still running.
  sleep 2
  printf '1\n'
  sleep 1
  printf 'y\n'
  sleep 1
} | su - app -c "env PATH=/e2e/fakebin:\$PATH CLI_URL=file:///work/cli.py GPTADMIN_DOWNLOAD_QUIET=1 script -qefc 'bash $install_script' /tmp/user-public-hub-shellmcp.typescript"

test -x /home/app/.local/bin/gptadmin
test -f /home/app/.config/gptadmin/gptadmin.env
grep -q "HUB_PUBLIC_URL=$hub_url" /home/app/.config/gptadmin/gptadmin.env
grep -q 'INSTALL_HUB=true' /home/app/.config/gptadmin/gptadmin.env
grep -q 'INSTALL_SHELLMCP=true' /home/app/.config/gptadmin/gptadmin.env
test -f /home/app/.config/systemd/user/gptadmin-hub.service
test -f /home/app/.config/systemd/user/shellmcp.service
grep -q 'GPTAdmin Shell MCP Agent' /home/app/.config/systemd/user/shellmcp.service
echo 'ok: user install + public hub + shellmcp service files'
