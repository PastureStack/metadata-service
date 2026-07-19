# Compatibility

The current migration keeps these protocol surfaces stable until every recorded consumer has moved to a new contract:

- metadata versions `2015-12-19` and `2016-07-29`;
- the `metadata` service-discovery name;
- plain text, JSON, YAML, source-IP selection, and `default` fallback behavior;
- bounded long polling with `wait`, `value`, and `maxWait`;
- the `config.update` event and `metadata-answers` configuration item;
- `/v1/subscribe` and `/v1/configcontent/metadata-answers` for event/configuration compatibility;
- `/v2-beta/publish` for event acknowledgements, plus the applied-version acknowledgement on the configuration endpoint.

New deployment-facing settings use `PLATFORM_URL`, `PLATFORM_ACCESS_KEY`, `PLATFORM_SECRET_KEY`, and `PLATFORM_CA_ROOT`. New workload labels use the `io.pasturestack.*` namespace.

For an in-place upgrade, the service accepts `CATTLE_URL`, `CATTLE_ACCESS_KEY`, and `CATTLE_SECRET_KEY` only when the corresponding neutral setting is empty. It also checks `/var/lib/rancher/etc/ssl/ca.crt` only when neither `PLATFORM_CA_ROOT` nor the neutral default CA file is available. New templates and documentation must use the neutral contract; these aliases exist solely so an already-running environment can move without losing event delivery or its private CA.

The managed system image contract is also preserved: the container may start as root with `NET_ADMIN` to add `169.254.169.250/32`, but the metadata process runs as UID/GID `10001`. Standalone file-backed use runs as `10001` from startup and needs neither root nor `NET_ADMIN`.

Managed subscription mode additionally requires a stack created with `system=true`; adding system-related labels to an ordinary application stack does not change the persisted system flag. The provider instance must be agent-backed, running or starting, and carry the compatibility semantics for create-agent, metadata-provider, system-container, and global scheduling. Otherwise the service can start normally but `metadata-answers` remains unassigned and returns `404`.

The v0.9.10 managed contract was exercised in an isolated Server environment with a disposable Docker host. The proof covered control-plane credential injection, the initial configuration download, applied-version acknowledgement, a subsequent event-driven update, UID/GID `10001`, and HTTP endpoint reachability. Production catalog, multi-host, upgrade, and rollback verification remain separate release gates.

Compatibility is not a promise that every historical deployment detail is safe to retain. Obsolete init/monit packaging, legacy build automation, private image coordinates, and unused generated API clients are intentionally outside this contract.
