FROM golang:1.26-bookworm AS build
WORKDIR /src/go-hub
COPY go-hub/go.mod go-hub/go.sum ./
COPY go-shellmcp/go.mod go-shellmcp/go.sum ../go-shellmcp/
COPY go-hub/third_party/qjs/go.mod go-hub/third_party/qjs/go.sum ./third_party/qjs/
RUN go mod download
COPY go-hub/ ./
COPY go-shellmcp/ ../go-shellmcp/
ENV CGO_ENABLED=0 GOMAXPROCS=2
RUN go build -o /out/gptadmin_hub ./cmd/gptadmin-hub \
 && go build -o /out/gptadmin-node ./cmd/gptadmin-node
COPY tests/e2e/failover/fake-frpc.c /src/fake-frpc.c
RUN cc -Wall -Wextra -Werror -O2 /src/fake-frpc.c -o /out/fake-frpc

FROM python:3.12-slim-bookworm
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl haproxy jq openssl \
 && rm -rf /var/lib/apt/lists/*
RUN pip install --no-cache-dir mcp==1.28.1
COPY --from=build /out/gptadmin_hub /usr/local/bin/gptadmin_hub
COPY --from=build /out/gptadmin-node /usr/local/bin/gptadmin-node
COPY scripts/gptadmin_node_manage.py /src/scripts/
COPY --chmod=0755 scripts/gptadmin_reader_route.sh /src/scripts/
COPY tests/e2e/node/member_outage_run.py /src/tests/e2e/node/
COPY scripts/gptadmin_failover_watchdog.py scripts/gptadmin_frpc_processes.py scripts/gptadmin_failover_proxy.py scripts/gptadmin_failover_reclaim_push.py /usr/local/bin/
COPY --from=build /out/fake-frpc /e2e/fake-frpc
COPY tests/e2e/failover/ingress.py tests/e2e/failover/run.sh /e2e/
RUN chmod 0755 /usr/local/bin/gptadmin_hub /usr/local/bin/gptadmin_failover_*.py /e2e/fake-frpc /e2e/run.sh
CMD ["/e2e/run.sh"]
