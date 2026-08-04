# Live acceptance runner

`tests/e2e/live_acceptance.py` is the first deployment-session gate for a
reachable Hub. It uses an existing short-lived scoped connection and never
prints the bearer value or response bodies.

```bash
GPTADMIN_LIVE_BASE_URL="$HUB_URL" \
GPTADMIN_LIVE_BEARER="$SCOPED_JWT" \
python3 tests/e2e/live_acceptance.py
```

The runner verifies, in order:

1. `/healthz` and `/version`;
2. `/connect.json` MCP/OAuth discovery;
3. canonical OAuth authorization and token endpoints;
4. generated `/actions/openapi.yaml`;
5. authenticated `tools/list` and the harmless `demo` MCP call.

For a deployment that changes OAuth, connector registration, or client-session
handling, this runner is necessary but not sufficient. The release evidence
must additionally include a freshly authorized real MCP client after its
defined restart/refresh boundary, completing a harmless `discover -> schema ->
execute` interaction without manual reconnect. Record a redacted immutable
client artifact and the ownership-specific automated regression named in
[Integration Control Contract](./INTEGRATION_CONTROL_CONTRACT.md#authorization-durability).

Before every deployment that changes authorization, key issuance, endpoints, or
relay behavior, run a redacted credential matrix against **every existing
supported credential**. Each credential must complete its harmless probe on the
custom endpoint, MCP remote endpoint, and relay/VRP path that it is entitled to
use. Do not substitute a newly issued token, a health check, or a single green
endpoint for that matrix. Record external-owner credentials that cannot be
probed as explicit deployment blockers.

Use `--required-tool NAME` for a profile-specific tool allowlist. This runner
does not claim successful webhook delivery, Network Tunnel data-plane
connectivity, file restore, profile mutation or external MCP-client
certification; those remain separate gates and must be recorded with their own
immutable evidence.

## Custom GPT browser contract

`tests/test_custom_gpt_browser_contract.py` is the release gate for the
Actions-specific contract. It creates a disposable Hub and checks the same
three connection paths a Custom GPT requires:

1. the imported `/actions/openapi.yaml` has one Bearer scheme and contains no
   optional proxy/webhook operations or unsupported approval headers;
2. a readonly Bearer issued by the Hub completes `GET /mcp-relay/servers`;
3. a real Chromium page submits the OAuth authorization form, returns through
   an Authorization Code callback, exchanges the code, and completes that same
   harmless discovery request. The test covers both PKCE-capable clients and
   the no-PKCE Custom GPT compatibility path, which is restricted to the exact
   ChatGPT Actions callback profile.

Run it locally with an isolated browser:

```bash
GPTADMIN_BROWSER_TESTS=1 \
  pytest -q tests/test_custom_gpt_browser_contract.py
```

To use a BrowserOS-managed Mac without using a personal editor session, expose
the Mac's Chrome CDP port through an authenticated SSH tunnel and make the
one-shot Hub reachable from that Mac. The Hub address is a disposable test
listener, never the production Hub URL or administrator credential:

```bash
GPTADMIN_BROWSER_TESTS=1 \
GPTADMIN_BROWSER_CDP_URL=http://127.0.0.1:19102 \
GPTADMIN_BROWSER_HUB_HOST=192.168.2.100 \
  pytest -q tests/test_custom_gpt_browser_contract.py
```

The test does not create or mutate a real Custom GPT draft. A final release
that claims ChatGPT importer certification must additionally retain a redacted
artifact from a dedicated test draft importing a disposable public test-Hub
URL. Do not reuse the owner’s GPT or a production public Hub for that check.

## Deployment runtime diagnosis

Before changing a deployed host, use the read-only runtime probe to emit a
small JSON report for the canonical service contract. It never restarts units,
reads credential values or returns remote stderr:

```bash
python3 tests/e2e/deployment_runtime.py \
  --kind hub --host "$HUB_HOST" --port "$SSH_PORT" --user "$SSH_USER"
python3 tests/e2e/deployment_runtime.py \
  --kind shellmcp --host "$SHELLMCP_HOST" --port "$SSH_PORT" --user "$SSH_USER"
```

The Hub probe requires a running `gptadmin-hub.service`, a real `/healthz`
response on the local port and no Tunnel `router config conflict`. The
ShellMCP probe requires the canonical `shellmcp.service`, rejects legacy
`rootd-go` binaries and reports queue authentication failures. A failed report
is diagnosis only; repair and the authenticated `live_acceptance.py` run still
require an explicitly authorized deployment session.
