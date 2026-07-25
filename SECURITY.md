# Security Policy

## Supported state

This repository is under migration review and is not release-ready. Do not use an unreviewed worktree build in production.

## Sensitive data

- Answers and downloaded deltas may contain environment topology. Store them with least privilege and never commit live data.
- Keep `PLATFORM_ACCESS_KEY` and `PLATFORM_SECRET_KEY` outside source control and logs. The upgrade-only `CATTLE_ACCESS_KEY` and `CATTLE_SECRET_KEY` aliases require the same handling.
- Keep the reload listener bound to loopback unless an authenticated local proxy is explicitly designed and tested.
- Enable `--xff` only behind a trusted proxy that replaces, rather than appends untrusted, forwarding headers.
- Error handling intentionally reports status codes without echoing platform response bodies.
- The published image defaults to UID/GID `10001`. Grant `NET_ADMIN` and a root startup user only to the managed system deployment that must configure the link-local metadata address; the startup wrapper drops privileges before launching the service.
- The Windows Server 2022 variant requires `ContainerAdministrator` because
  link-local IP assignment is a privileged Windows networking operation. Do
  not add host filesystem, named-pipe, or Docker daemon mounts to that service.

## Reporting

Report suspected vulnerabilities through the repository's private security advisory channel. Do not include live credentials, private registry addresses, or production metadata in an issue.
