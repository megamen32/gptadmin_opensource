FROM golang:1.24-bookworm AS build
WORKDIR /src
COPY go-hub/go.mod ./
RUN go mod download
COPY go-hub/ ./
RUN CGO_ENABLED=0 go build -o /out/gptadmin_hub ./cmd/gptadmin-hub

FROM python:3.12-slim-bookworm
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl jq \
 && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/gptadmin_hub /usr/local/bin/gptadmin_hub
COPY scripts/gptadmin_failover_watchdog.py scripts/gptadmin_failover_proxy.py scripts/gptadmin_failover_reclaim_push.py /usr/local/bin/
COPY agents/generic_stdio_mcp_relay/generic_stdio_mcp_relay.py /e2e/generic_stdio_mcp_relay.py
COPY tests/e2e/failover/fake-frpc tests/e2e/failover/fake_mcp_stdio.py tests/e2e/failover/ingress.py tests/e2e/failover/run.sh /e2e/
RUN chmod 0755 /usr/local/bin/gptadmin_hub /usr/local/bin/gptadmin_failover_*.py /e2e/fake-frpc /e2e/fake_mcp_stdio.py /e2e/run.sh
CMD ["/e2e/run.sh"]
