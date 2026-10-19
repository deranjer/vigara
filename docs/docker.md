# Docker inventory module

The Docker collector is disabled unless `VIGIRA_DOCKER_ENABLED=true` is set on
the agent. When enabled, set `VIGIRA_DOCKER_HOST` to an explicit endpoint such
as `unix:///var/run/docker.sock` or `http://127.0.0.1:2375`. HTTP endpoints are
restricted to loopback IP addresses. There is no default socket discovery,
remote host, credential, or automatic permission grant. Invalid enabled
configuration is reported as a collection error and does not stop host
telemetry.

Docker Engine access is powerful: membership in the group that can open the
Docker socket generally grants effective root control of the host. The module's
`docker.read` permission describes its application capability; it does not
reduce the operating-system authority of a Docker socket. Prefer a local
read-only API proxy configured to allow only `GET /containers/json` and
`GET /containers/{id}/stats`, deny all other methods and paths, and bind it to
loopback. The agent rejects non-loopback HTTP endpoints. Protect the socket or
proxy with operating-system permissions appropriate to the host.

The module advertises ID `docker_inventory`, version `1.0.0`, capabilities
`docker_inventory` and `docker_container_resources`, and required permission
`docker.read` scoped to `configured-endpoint`. It is disabled by default and
only calls read endpoints. Job settings cannot select an endpoint or pass
credentials. These capability names describe telemetry collected by the local
opt-in path; the agent does not advertise Docker as a remotely executable job
capability. For an embedding that invokes the `modules.Module` adapter, an
explicit `docker.read` job grant is required. In automatic collection, the
local `VIGIRA_DOCKER_ENABLED=true` setting is the operator's opt-in grant. The
Engine API v1.44 `one-shot=true` stats mode is used to avoid
waiting for two internal samples; the collector retains its own prior sample
and emits CPU percentage and network byte-per-second rates only after a valid,
monotonic delta exists. CPU percentage is normalized by online CPU capacity
and therefore stays within the server's 0–100 percent contract.

Inventory is capped at 32 containers in stable ID order. The agent reports
container ID, first name, image reference, and state, plus resource gauges for
memory usage/limit and process count. CPU and network rates are omitted until
their second sample. A failed container stats request is isolated from other
containers and host collection. Each API response is capped at 1 MiB, each
collection has a 10-second total budget, per-request timeout is 3 seconds, and
the capability returns at most 32 container errors. The collector never calls
inspect and never reads environment variables, mounts, command arguments,
labels, or secret configuration.

The server stores `dockerContainers` as an optional part of the latest raw
telemetry snapshot and validates at most 32 unique IDs and bounded strings.
Docker metrics use canonical units `By`, `%`, `1`, and `By/s`; resource series
carry only the stable container ID label. The Devices detail page shows the
latest bounded inventory. Upgrade the server before enabling Docker collection:
older servers reject unknown snapshot fields, while the default-disabled agent
omits `dockerContainers` entirely.

The module requires Docker Engine API v1.44 (Docker Engine 25 or newer). This also supports Engine 29.0–29.2, whose minimum API is v1.44. Older daemons fail as an isolated collection error; the module does not weaken daemon protections.

The implementation follows Docker's [Engine API reference](https://docs.docker.com/reference/api/engine/) and its
[v1.44 API version history](https://docs.docker.com/reference/api/engine/version-history/).

## Management tiers

Collection stays read-only. Management features use separate tiers that the host's own operator enables locally, in addition to `VIGIRA_DOCKER_ENABLED=true`:

| `VIGIRA_DOCKER_MODE` | Capabilities offered | Allows |
|---|---|---|
| unset or `read` | none | Today's collection, plus read-only views |
| `manage` | `docker.manage` | Lifecycle actions, image pulls, and network and volume maintenance |
| `exec` | `docker.manage`, `docker.exec` | Everything in `manage`, plus terminals inside containers |
| `deploy` | all three | Everything in `exec`, plus Compose stack deployment |

A tier takes effect only when both sides agree. The agent offers it because its operator set `VIGIRA_DOCKER_MODE`; the server accepts it because the device's enrollment scope includes the matching capability. Choose the scope in the **Add device** dialog ("Container management allowed for this device") or list the capabilities when creating an enrollment token. A report that exceeds the enrollment scope is rejected by the server. In that case the agent logs a warning, stops offering the tiers, and keeps monitoring; re-enroll with the needed access to enable them. An unrecognized `VIGIRA_DOCKER_MODE` offers nothing.

These tiers gate the features described in the [container management plan](containers-management.md). Management features are not available yet; the tiers and the live channel are the foundation they build on. Because Docker socket access is effectively root on the host, grant the highest tier only where you need it.

## Live container views

With Docker collection enabled, the agent also answers read-only requests from the server over the [live channel](protocol.md#live-channel), which power the **Containers** page. They need no management tier and issue only `GET` requests to the same configured endpoint:

| Method | Returns |
|---|---|
| `docker.list` | Every container, running or not, with its Compose project and service (up to 2000 per host; the 32-container limit above applies only to telemetry snapshots) |
| `docker.inspect` | A curated view of one container: state, command, ports, mounts, networks, limits, labels, and environment variable names |
| `docker.logs` | A stream of log lines (stdout and stderr), following if asked, from the last 100 to 5000 lines |
| `docker.stats` | A stream of CPU, memory, network, disk, and process readings, about one per second |

A host that has not set `VIGIRA_DOCKER_ENABLED=true` does not register these methods, and the Containers page reports that Docker is not enabled there. Container references are validated as plain Docker names or IDs before any request is made, responses are size-limited, and log lines over 16 KiB are cut and marked.

Environment variable **values** are not sent unless an administrator asks to reveal them; that request is recorded in the [audit log](users-and-roles.md#audit-log) as `container.env.reveal`. Command arguments, labels, and mounts are shown, so avoid putting secrets there.
