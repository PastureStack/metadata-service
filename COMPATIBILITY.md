# Compatibility

The current migration keeps these protocol surfaces stable until every recorded consumer has moved to a new contract:

- metadata versions `2015-12-19` and `2016-07-29`;
- the `metadata` service-discovery name;
- plain text, JSON, YAML, source-IP selection, and `default` fallback behavior;
- bounded long polling with `wait`, `value`, and `maxWait`;
- the `config.update` event and `metadata-answers` configuration item;
- `/v1/subscribe` and `/v1/configcontent/metadata-answers` for event/configuration compatibility;
- `/v2-beta/publish` for event acknowledgements, plus the applied-version acknowledgement on the configuration endpoint.

New deployment-facing settings use `PLATFORM_URL`, `PLATFORM_ACCESS_KEY`, `PLATFORM_SECRET_KEY`, `PLATFORM_ALLOWED_ORIGINS`, and `PLATFORM_CA_ROOT`. New workload labels use the `io.pasturestack.*` namespace.

The upstream `v0.10.4` multi-subscriber contract is retained. Credentials discovered from the primary metadata stream may register additional environment sources. Same-origin sources are accepted; cross-origin sources require an explicit canonical-origin entry in `PLATFORM_ALLOWED_ORIGINS` (or the upgrade-only `CATTLE_ALLOWED_ORIGINS` alias). URL user information, fragments, queries, non-canonical paths, unsupported schemes, malformed hosts, and cross-origin redirects fail closed. Credential rotation removes the previous subscriber before its later callbacks can alter the active snapshot.

For an in-place upgrade, the service accepts `CATTLE_URL`, `CATTLE_ACCESS_KEY`, and `CATTLE_SECRET_KEY` only when the corresponding neutral setting is empty. It also checks `/var/lib/rancher/etc/ssl/ca.crt` only when neither `PLATFORM_CA_ROOT` nor the neutral default CA file is available. New templates and documentation must use the neutral contract; these aliases exist solely so an already-running environment can move without losing event delivery or its private CA.

The managed system image contract is also preserved: the container may start as root with `NET_ADMIN` to add `169.254.169.250/32`, but the metadata process runs as UID/GID `10001`. Standalone file-backed use runs as `10001` from startup and needs neither root nor `NET_ADMIN`.

The Windows variant targets the Windows Server 2022 (`ltsc2022`) container ABI.
It uses the same API and subscription contract, assigns
`169.254.169.250/32` through Nano Server's `netsh`, and requires
`ContainerAdministrator` for that link-local setup. It does not expose a
separate metadata protocol or answer format.

Managed subscription mode additionally requires a stack created with `system=true`; adding system-related labels to an ordinary application stack does not change the persisted system flag. The provider instance must be agent-backed, running or starting, and carry the compatibility semantics for create-agent, metadata-provider, system-container, and global scheduling. Otherwise the service can start normally but `metadata-answers` remains unassigned and returns `404`.

The managed contract was exercised in an isolated Server environment with a disposable Docker host. The proof covered control-plane credential injection, the initial configuration download, applied-version acknowledgement, a subsequent event-driven update, UID/GID `10001`, and HTTP endpoint reachability. The `0.10.5` source gates additionally cover multi-source merging, origin policy, credential rotation, rejected-snapshot rollback, bounded long polling, cancellation, and restart persistence. Production catalog, multi-host, upgrade, and rollback verification remain separate release gates.

Compatibility is not a promise that every historical deployment detail is safe to retain. Obsolete init/monit packaging, legacy build automation, private image coordinates, and unused generated API clients are intentionally outside this contract.
