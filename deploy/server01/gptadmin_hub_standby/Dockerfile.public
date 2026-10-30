ARG BUILD_FROM=ghcr.io/home-assistant/aarch64-base:3.22
FROM $BUILD_FROM

ARG BUILD_VERSION
ARG BUILD_ARCH=aarch64

LABEL \
  io.hass.version="$BUILD_VERSION" \
  io.hass.type="app" \
  io.hass.arch="$BUILD_ARCH" \
  org.opencontainers.image.source="https://github.com/megamen32/gptadmin-haos-addons"

RUN apk add --no-cache bash ca-certificates curl jq python3 tzdata openssl docker-cli iproute2 iputils bind-tools coreutils procps shadow sudo

# The publisher assembles gptadmin_hub, frpc and public/ into this build
# context. They are intentionally not committed to the public source repo.
COPY gptadmin_hub /usr/local/bin/gptadmin_hub
COPY gptadmin-node /usr/local/bin/gptadmin-node
COPY frpc /usr/local/bin/frpc
COPY public /opt/gptadmin/public
COPY gptadmin_failover_watchdog.py gptadmin_frpc_processes.py gptadmin_failover_proxy.py \
  gptadmin_failover_runtime.py gptadmin_failover_config.py gptadmin_quorum_config.py gptadmin_single_writer.py /usr/local/bin/
COPY run.sh /run.sh

RUN chmod 0755 /usr/local/bin/gptadmin_hub /usr/local/bin/gptadmin-node /usr/local/bin/frpc \
      /usr/local/bin/gptadmin_failover_watchdog.py \
      /usr/local/bin/gptadmin_failover_proxy.py \
      /usr/local/bin/gptadmin_failover_runtime.py \
      /usr/local/bin/gptadmin_failover_config.py /run.sh \
    && mkdir -p /data/config /data/outputs /opt/gptadmin/build /opt/gptadmin/public

CMD ["/run.sh"]
