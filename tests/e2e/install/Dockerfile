FROM ubuntu:24.04
ENV container=docker DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
      systemd systemd-sysv dbus curl ca-certificates python3 sudo util-linux iproute2 procps \
    && rm -rf /var/lib/apt/lists/* \
    && rm -f /lib/systemd/system/multi-user.target.wants/getty.target
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
