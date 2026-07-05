ARG BUILD_FROM=ghcr.io/home-assistant/aarch64-base:3.22
FROM $BUILD_FROM
RUN apk add --no-cache bash ca-certificates curl jq tzdata
COPY gptadmin_hub /usr/local/bin/gptadmin_hub
COPY build /opt/gptadmin/build
COPY public /opt/gptadmin/public
COPY run.sh /run.sh
RUN chmod 0755 /usr/local/bin/gptadmin_hub /run.sh \
    && mkdir -p /data/config /data/outputs /opt/gptadmin/build /opt/gptadmin/public
CMD ["/run.sh"]
