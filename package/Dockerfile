# syntax=docker/dockerfile:1.7
FROM ubuntu:26.04@sha256:2260313b31c8c011cd2eebe728008efac1b3982be73eb71348ea2648d2c0e09b

ARG UBUNTU_SNAPSHOT=20260825T000000Z

ADD --checksum=sha256:6077d27c6b6f8b23590cb01ff877ed8c804a67a5442cc32b5a33da10d2bd0e90 \
    https://snapshot.ubuntu.com/ubuntu/20260825T000000Z/pool/main/c/ca-certificates/ca-certificates_20260601~26.04.1_all.deb \
    /tmp/ca-certificates.deb
ARG PASTURESTACK_IMAGE_VERSION=dev
ARG PASTURESTACK_IMAGE_REVISION=unknown
ARG PASTURESTACK_IMAGE_CREATED=1970-01-01T00:00:00Z
ENV DEBIAN_FRONTEND=noninteractive

RUN set -eux; \
    mkdir -p /tmp/ca-bootstrap /etc/ssl/certs; \
    dpkg-deb --extract /tmp/ca-certificates.deb /tmp/ca-bootstrap; \
    find /tmp/ca-bootstrap/usr/share/ca-certificates -type f -name '*.crt' | LC_ALL=C sort | while IFS= read -r certificate; do sed -e '$a\' "${certificate}"; done > /etc/ssl/certs/ca-certificates.crt; \
    rm -rf /tmp/ca-bootstrap /tmp/ca-certificates.deb; \
    rm -f /etc/apt/sources.list /etc/apt/sources.list.d/*.list /etc/apt/sources.list.d/*.sources; \
    printf 'Types: deb\nURIs: https://snapshot.ubuntu.com/ubuntu/%s\nSuites: resolute resolute-updates resolute-backports resolute-security\nComponents: main universe restricted multiverse\nSigned-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg\nSnapshot: no\n' "${UBUNTU_SNAPSHOT}" > /etc/apt/sources.list.d/pasturestack-snapshot.sources; \
    printf 'Acquire::Retries "5";\nAcquire::http::Timeout "30";\nAcquire::https::Timeout "30";\nAcquire::http::Pipeline-Depth "0";\nAcquire::https::CaInfo "/etc/ssl/certs/ca-certificates.crt";\nAcquire::https::Verify-Peer "true";\nAcquire::https::Verify-Host "true";\nAcquire::AllowInsecureRepositories "false";\nAPT::Get::AllowUnauthenticated "false";\n' > /etc/apt/apt.conf.d/80pasturestack-snapshot; \
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
