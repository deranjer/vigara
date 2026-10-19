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

## Changing containers

Hosts at the `manage` tier or above also answer methods that change containers. They are registered only when `VIGIRA_DOCKER_MODE` is `manage`, `exec`, or `deploy`, and the channel refuses them below that tier, so a read-only host can never run them.

| Method | What it does |
|---|---|
| `docker.action` | One lifecycle action: `start`, `stop`, `restart`, `pause`, `unpause`, `kill` (with a signal from a fixed list), or `remove` (optionally forced, optionally deleting anonymous volumes) |
| `docker.recreate` | Replaces a container with a fresh copy built from its own settings, optionally pulling its image first, and streams progress |

Actions return Docker's own explanation when it refuses, for example removing a running container without force. Starting a container that is running, or stopping one that is stopped, is reported as already in that state rather than as an error.

**Recreate** builds the replacement while the original keeps running, so a failure to create it, for example because the image is missing or the pull fails, leaves the original untouched. It then stops the original, moves the names, starts the replacement, and waits a couple of seconds to confirm it is still running before removing the original. If anything fails after the swap begins, the replacement is removed and the original is renamed back and started again. Once the swap begins it always finishes or rolls back, even if the browser disconnects. A container that was stopped stays stopped.

The replacement keeps the original's image reference, environment, command, labels, mounts and bind mounts, ports, restart policy, limits, and network attachments (including static addresses and aliases). Docker-assigned values such as the generated hostname, container ID, and dynamic addresses are not copied. Volumes are reused, so data in them carries over. Pulls use no registry credentials, so private registries are not supported yet. Images pinned by digest are not pulled. A container managed by Docker Compose keeps its Compose labels, but recreating it here does not apply changes made to the Compose file; redeploy the stack for that.

## Terminals

A host at the `exec` tier (`VIGIRA_DOCKER_MODE=exec` or `deploy`, with the matching enrollment scope) can open an interactive shell inside a running container from the Containers page. The agent creates a Docker exec with a terminal attached, upgrades the connection to a raw two-way stream, and runs only the chosen shell: `sh`, `bash`, or an automatic choice that prefers bash and falls back to sh. Optionally the shell runs as another user, given as a name or number with an optional group; anything else is rejected before Docker is contacted. A container with no shell reports Docker's own explanation.

Treat a terminal as full control of the container, and often as root inside it. That is why it is a separate tier from managing containers, and why only operators and administrators can use it.

Limits and records:

- A terminal closes after 15 minutes without keystrokes, after 4 hours in total, when the browser tab or page is left, and when the shell exits. Output alone does not keep a session alive.
- One person can hold at most 8 terminals open at a time.
- Opening and closing a terminal are recorded in the [audit log](users-and-roles.md#audit-log) as `container.exec.open` and `container.exec.close`, with the shell, user, duration, byte counts, and why it ended. What you type and what appears is never recorded, so avoid typing secrets only if you do not trust the terminal's own history inside the container.
- A refused attempt, such as a viewer trying to open a terminal, is audited as denied.
- Paste size is bounded: a message can be up to 1 MiB and is delivered to the host in pieces of at most 64 KiB.
