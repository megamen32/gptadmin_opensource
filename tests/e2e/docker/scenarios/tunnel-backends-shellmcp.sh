#!/usr/bin/env bash
set -euo pipefail
cd /work
python3 - <<'PY'
from pathlib import Path
from tunnels.cloudflare import CloudflareTunnel
from tunnels.frp import FrpTunnel

cf = CloudflareTunnel()
assert cf.is_available()
info = cf.start(9001)
assert info.backend == 'cloudflare'
assert info.public_url == 'https://shellmcp-e2e.trycloudflare.com'
info.stop()

cfg = Path('/e2e/out/frpc-shellmcp.toml')
frp = FrpTunnel(server_addr='frp.example.test', server_port=7000, token='secret', subdomain='shellmcp', domain='example.test', config_path=cfg)
assert frp.is_available()
info = frp.start(9001)
assert info.backend == 'frp'
assert info.public_url == 'https://shellmcp.example.test'
info.stop()
text = cfg.read_text()
assert 'serverAddr = "frp.example.test"' in text
assert 'subdomain = "shellmcp"' in text
print('ok: cloudflare + frp tunnel backends')
PY
