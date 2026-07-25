FROM ubuntu:26.04

ARG UBUNTU_MIRROR=http://archive.ubuntu.com/ubuntu
ARG PASTURESTACK_IMAGE_VERSION=dev
ARG PASTURESTACK_IMAGE_REVISION=unknown
ARG PASTURESTACK_IMAGE_CREATED=1970-01-01T00:00:00Z
ENV DEBIAN_FRONTEND=noninteractive

RUN set -eux; \
    find /etc/apt -type f \( -name '*.list' -o -name '*.sources' \) -exec sed -i \
        -e "s|http://security.ubuntu.com/ubuntu|${UBUNTU_MIRROR}|g" \
        -e "s|http://archive.ubuntu.com/ubuntu|${UBUNTU_MIRROR}|g" {} +; \
    printf 'Acquire::Retries "5";\nAcquire::http::Timeout "30";\nAcquire::https::Timeout "30";\nAcquire::http::Pipeline-Depth "0";\n' > /etc/apt/apt.conf.d/80pasturestack-retries; \
    apt-get update && \
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        bash ca-certificates iproute2 iptables libcap2-bin util-linux && \
    rm -rf /var/lib/apt/lists/* && \
    rm -f /usr/bin/pebble

COPY metadata-service start.sh platform-entrypoint.sh update-platform-ca /usr/bin/
COPY licenses /licenses/
RUN set -eux; \
    chmod 0755 /usr/bin/metadata-service /usr/bin/start.sh /usr/bin/platform-entrypoint.sh /usr/bin/update-platform-ca; \
    groupadd --gid 10001 pasturestack; \
    useradd --uid 10001 --gid pasturestack --home-dir /home/pasturestack --create-home --shell /usr/sbin/nologin pasturestack; \
    mkdir -p /var/lib/pasturestack-metadata; \
    setcap cap_net_bind_service=+ep /usr/bin/metadata-service; \
    chown -R 10001:10001 /home/pasturestack /var/lib/pasturestack-metadata

WORKDIR /var/lib/pasturestack-metadata

LABEL org.opencontainers.image.title="metadata-service" \
      org.opencontainers.image.description="Source-IP-aware metadata service for PastureStack" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.source="https://github.com/PastureStack/metadata-service" \
      org.opencontainers.image.version="${PASTURESTACK_IMAGE_VERSION}" \
      org.opencontainers.image.revision="${PASTURESTACK_IMAGE_REVISION}" \
      org.opencontainers.image.created="${PASTURESTACK_IMAGE_CREATED}"

USER 10001:10001

ENTRYPOINT ["/usr/bin/platform-entrypoint.sh", "/usr/bin/start.sh"]
CMD ["metadata-service"]
