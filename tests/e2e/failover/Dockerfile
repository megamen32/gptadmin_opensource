FROM golang:1.26-bookworm AS build
WORKDIR /src/go-hub
COPY go-hub/go.mod go-hub/go.sum ./
COPY go-shellmcp/go.mod go-shellmcp/go.sum ../go-shellmcp/
RUN go mod download
COPY go-hub/ ./
COPY go-shellmcp/ ../go-shellmcp/
RUN CGO_ENABLED=0 go build -o /out/gptadmin_hub ./cmd/gptadmin-hub
COPY tests/e2e/failover/fake-frpc.c /src/fake-frpc.c
RUN cc -Wall -Wextra -Werror -O2 /src/fake-frpc.c -o /out/fake-frpc

FROM python:3.12-slim-bookworm
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl jq \
 && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/gptadmin_hub /usr/local/bin/gptadmin_hub
COPY scripts/gptadmin_failover_watchdog.py scripts/gptadmin_frpc_processes.py scripts/gptadmin_failover_proxy.py scripts/gptadmin_failover_reclaim_push.py /usr/local/bin/
COPY --from=build /out/fake-frpc /e2e/fake-frpc
COPY tests/e2e/failover/ingress.py tests/e2e/failover/run.sh /e2e/
RUN chmod 0755 /usr/local/bin/gptadmin_hub /usr/local/bin/gptadmin_failover_*.py /e2e/fake-frpc /e2e/run.sh
CMD ["/e2e/run.sh"]
