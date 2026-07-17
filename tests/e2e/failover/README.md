# GPTAdmin failover Docker black-box suite

Run from the repository root:

```bash
docker compose -f tests/e2e/failover/docker-compose.yml up --build --abort-on-container-exit --exit-code-from failover-e2e
```

The suite runs up to three real Go hubs, the real failover watchdog and proxy,
and a controlled ingress. It verifies independent failure modes:

- tunnel failure while the primary hub is healthy;
- primary hub failure while the tunnel remains live;
- primary hub and tunnel failure together, followed by tunnel recovery.
- a live generic stdio MCP relay re-registering and serving a tool call after
  Hub failover;
- signed reclaim after primary recovery, which demotes the fallback route;
- rank 1 promotion fencing a rank 2 fallback;
- rank 2 promotion when the rank 1 node is unavailable.

The ingress and FRP client are test doubles because an external FRP server is
not part of this repository. The FRP double exposes only the observable
contract required by the watchdog: promotion routes public ingress to fallback
and demotion removes that route. The MCP relay, Hub authentication and MCP
JSON-RPC client calls are real processes and requests.
