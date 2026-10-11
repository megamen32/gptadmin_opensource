ARG BUILD_FROM=ghcr.io/home-assistant/aarch64-base:3.22
FROM $BUILD_FROM
RUN apk add --no-cache bash ca-certificates curl jq python3 tzdata
COPY gptadmin_hub /usr/local/bin/gptadmin_hub
COPY frpc /usr/local/bin/frpc
COPY gptadmin_failover_watchdog.py gptadmin_failover_proxy.py gptadmin_failover_runtime.py gptadmin_failover_config.py /usr/local/bin/
COPY failover_config.json failover_state.json /opt/gptadmin/failover/
COPY build /opt/gptadmin/build
COPY public /opt/gptadmin/public
COPY run.sh /run.sh
RUN chmod 0755 /usr/local/bin/gptadmin_hub /usr/local/bin/frpc \
      /usr/local/bin/gptadmin_failover_watchdog.py \
      /usr/local/bin/gptadmin_failover_proxy.py \
      /usr/local/bin/gptadmin_failover_runtime.py \
      /usr/local/bin/gptadmin_failover_config.py /run.sh \
    && chmod 0600 /opt/gptadmin/failover/failover_config.json /opt/gptadmin/failover/failover_state.json \
    && mkdir -p /data/config /data/outputs /opt/gptadmin/build /opt/gptadmin/public
CMD ["/run.sh"]
