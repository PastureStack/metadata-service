# Metadata Service

Metadata Service is a source-IP-aware HTTP service for environment, host, service, and workload metadata. It supports dated API views, JSON/YAML/plain-text responses, long polling, file reloads, and an optional platform event subscription.

PastureStack is an independent community effort to preserve, audit, and modernize the Rancher 1.6 ecosystem. It is not affiliated with or endorsed by Rancher Labs or SUSE.

**Upstream:** [`rancher/rancher-metadata`](https://github.com/rancher/rancher-metadata). This GitHub fork retains the upstream Git history, authorship, dates, and license notices unchanged; PastureStack maintenance is consolidated into one commit after the preserved upstream boundary.

## Compatibility contract

- The dated `2015-12-19` and `2016-07-29` views remain available while dependent components migrate.
- The deployment must expose the neutral service-discovery name `metadata`.
- Client-specific answers are selected by source IP, with an optional `default` answer set.
- Long polling uses `wait=true`, `value=<previous-value>`, and an optional bounded `maxWait` value.
- `X-Forwarded-For` is ignored unless `--xff` is explicitly enabled.

See [COMPATIBILITY.md](COMPATIBILITY.md) before changing paths, answer shapes, event names, or long-poll behavior.

## Run from an answers file

```bash
docker run --rm -p 8080:80 \
  --mount type=bind,src="$PWD/example/answers.json",dst=/var/lib/pasturestack-metadata/answers.json,readonly \
  ghcr.io/pasturestack/metadata-service:v0.9.11 \
  metadata-service --answers /var/lib/pasturestack-metadata/answers.json
```

The top level of an answers file is a map of dated versions. Each dated version maps client IP addresses (or `default`) to arbitrary JSON/YAML values. Examples are available under `example/`.

The image runs as UID/GID `10001` by default and can bind port 80 through a file capability. A managed system deployment starts the container as root with `NET_ADMIN` only long enough to add `169.254.169.250/32`, then immediately drops to UID/GID `10001`. The image does not require root for file-backed standalone use.

## Platform subscription mode

Set the following environment variables and add `--subscribe`:

- `PLATFORM_URL`
- `PLATFORM_ACCESS_KEY`
- `PLATFORM_SECRET_KEY`
- `PLATFORM_CA_ROOT` (optional; the container default is `/var/lib/pasturestack/etc/ssl/ca.crt`)

The client uses the compatible `/v1` surface for event subscription and `configcontent/metadata-answers`, while acknowledgements are published through `/v2-beta`. A root URL, `/v1`, or `/v2-beta` input is normalized into those two purpose-specific endpoints. Responses and error messages are bounded and do not include response bodies.

A managed orchestration deployment must be installed by a reviewed system catalog or an authenticated system API with the stack property `system=true`. The ordinary application-stack form does not set that property. The control plane assigns `metadata-answers` only to an agent-backed metadata provider whose instance is both a system instance and running or starting. A regular stack can appear active while the configuration endpoint still returns `404`. The deployment template must also preserve create-agent, metadata-provider, system-container, and global-scheduling semantics in its compatibility layer.

Existing installations may continue to inject `CATTLE_URL`, `CATTLE_ACCESS_KEY`, and `CATTLE_SECRET_KEY` during an in-place upgrade. The neutral `PLATFORM_*` names always take precedence. The historical CA mount at `/var/lib/rancher/etc/ssl/ca.crt` is also accepted only when the neutral default is absent. These aliases are compatibility inputs, not names for new deployments.

## Build and test

The minimum reviewed toolchain is Go 1.26.5. Earlier 1.26 patch releases contain reachable standard-library vulnerabilities for this service.

```bash
install -m 0755 /reviewed/docker-cli-29.6.2-linux-amd64 \
  dist/dependencies/docker-cli-29.6.2-linux-amd64
make ci
```

The containerized build pins Go 1.26.5 by SHA-256, verifies the pre-reviewed Docker 29.6.2 client, runs race tests, `go vet`, formatting, module integrity, license checks, privacy checks, and then packages from an allow-listed temporary context. CI/CD remains disabled until the migration acceptance gates are complete.

## Security

Do not expose the loopback reload listener, enable forwarded-address trust without a trusted proxy, or commit answer files containing credentials. See [SECURITY.md](SECURITY.md).

## License and attribution

The inherited project remains licensed under the terms in [LICENSE](LICENSE). PastureStack does not claim authorship of inherited work. Source provenance is recorded in [ORIGIN.md](ORIGIN.md), and bundled dependency notices are in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) and `LICENSES/`.
