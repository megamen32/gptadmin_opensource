FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates curl gzip python3 python3-requests sudo tar util-linux \
 && rm -rf /var/lib/apt/lists/*

RUN useradd -m -s /bin/bash app \
 && usermod -aG sudo app \
 && printf 'app ALL=(ALL) NOPASSWD:ALL\n' >/etc/sudoers.d/99-app-nopasswd \
 && chmod 0440 /etc/sudoers.d/99-app-nopasswd

WORKDIR /work
COPY deploy/install.sh /work/deploy/install.sh
COPY tests/e2e/docker/fakebin/ /e2e/fakebin/
COPY tests/e2e/docker/scenarios/ /e2e/scenarios/
COPY tunnels/ /work/tunnels/
RUN chmod +x /work/deploy/install.sh /e2e/fakebin/* /e2e/scenarios/*.sh

ENV PATH=/e2e/fakebin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
CMD ["/e2e/scenarios/run-all.sh"]
