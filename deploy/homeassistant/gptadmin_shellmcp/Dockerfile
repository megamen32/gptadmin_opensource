ARG BUILD_FROM=ghcr.io/home-assistant/aarch64-base:3.22
FROM $BUILD_FROM
RUN apk add --no-cache bash jq openssl python3 curl ca-certificates docker-cli iproute2 iputils bind-tools coreutils procps shadow sudo
COPY shellmcp-go /usr/local/bin/shellmcp-go
COPY hub_ed25519.pub /opt/gptadmin/hub_ed25519.pub
COPY run.sh /run.sh
RUN chmod 0755 /usr/local/bin/shellmcp-go /run.sh \
    && mkdir -p /opt/gptadmin
CMD ["/run.sh"]
