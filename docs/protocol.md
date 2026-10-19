# Agent protocol and module contract (v1)

This document defines the first server/agent wire contract and the in-process
contract for built-in modules. The Go representations live in
`internal/protocol` and `internal/modules`. Transport is authenticated HTTPS;
enrollment and credential rotation are separate concerns.

## Versioning and identity

Every request and response carries `protocolVersion: "v1"`. A client and server
must reject an unsupported major version with a clear compatibility error.
Within v1, additions are optional or have safe defaults; removing or changing
the meaning/type of a field requires a new major version. Agent and module
versions use SemVer. Module IDs are stable lowercase identifiers (recommended
form: `vendor.product.module`); they must never be reused for a different
purpose. Configuration versions are opaque stable revision IDs. Job, telemetry
event, and result IDs are stable across retries. The server deduplicates
telemetry by event ID and results by result ID/job ID.

## Polling and desired configuration

An enrolled agent periodically posts an `AgentRequest` containing its stable
server-issued `agentId`, protocol and agent versions, OS platform,
architecture, capabilities, and optional known configuration version / last
seen job ID. The server replies with `AgentResponse`: its UTC server time, a
poll interval, an optional complete `AgentConfiguration` snapshot, and at most
one authorized `Job`. No job is represented by an omitted/null `job`. Poll
intervals should be bounded by server policy and clients should add jitter.

Configuration snapshots have an opaque version and generation time. Each
module entry names the exact module ID and version, enabled state, and typed
settings. A snapshot replaces the previous desired state atomically; agents
must not merge it as a patch. Unknown optional settings can be ignored only
when the module schema permits that behavior. Secret settings must never be
logged or included in ordinary diagnostic output.

## Jobs, grants, and failure reporting

A job has a stable ID, kind, module ID/version, creation/deadline timestamps,
positive `timeoutSeconds`, settings, and explicit permissions. Leased backup
jobs also carry an opaque `attemptToken` that binds execution and its result to
one server-issued attempt. The agent must retain it with queued results and
must not replay that attempt after restart. The agent must
match the requested module version and advertised capability before execution.
The deadline is authoritative; a runner creates a bounded context and stops
work at timeout. A module receives only the job's settings and permission
grants. Declaring a permission in a module descriptor does not grant it; every
access must be covered by a job grant, including any required scope.

The agent submits one `JobResult` with stable result/job/agent/module identity,
the attempt token when present, start and finish timestamps, status (`succeeded`, `failed`, `timed_out`, or
`cancelled`), optional output, and a structured failure code/message/retryable
flag when applicable. Retries preserve IDs and payload. Error messages must
not contain credentials or other secret values. A failed, timed-out, or
panicking module is isolated to that invocation; the agent remains alive and
continues polling and running unrelated modules.

An agent advertises optional progress support with
`AgentRequest.supportsBackupProgress`; the server opts the poll response in with
`AgentResponse.backupProgressEnabled`. Leased Restic backup agents may then
submit the optional `BackupProgress` heartbeat to
`/api/agent/backups/progress` using their existing agent bearer credential. It
is bound to the job and attempt token, carries a monotonically
increasing sequence, and contains only a phase and bounded aggregate file/byte
counters. Paths, file names, stderr, and credentials are forbidden. The server
accepts a heartbeat only while the matching lease is active; a heartbeat cannot
revive an expired or unknown attempt. Repeating the same sequence and payload
is idempotent; changing a payload at the same sequence is rejected. Agents
throttle progress updates and stop the child process if they cannot confirm
lease heartbeats. Older v1 agents may omit the capability and progress; the
server keeps the existing bounded execution deadline and reports the attempt
as stale because it cannot confirm recent activity. New agents also remain
compatible with an older server that omits the opt-in response field.

Restic's business outcome is recorded inside the result output because v1 job
statuses remain unchanged. In particular, `status: failed` with
`output.backupOutcome: "partial"` may include a usable `snapshotIds` value and
must be shown as a partial backup requiring attention, not discarded.

## Telemetry

Agents submit module `TelemetryBatch` values and host `TelemetrySnapshotBatch`
values using the existing bearer credential. Snapshot IDs remain stable across
retries and are deduplicated by agent identity. Host snapshots include OS,
version, architecture, agent version, a hardware summary, observation time,
supported collection capabilities, errors, and measurements with validated
names and canonical units. Missing or unsupported readings are omitted rather
than represented as zero. Counter-based throughput is emitted only after two
reads establish a real rate. See [host telemetry](telemetry.md) for the current
host metric names, queue limits, and ingestion rules.

Capability reports list protocol/agent versions, platform, architecture, and
the module capabilities actually present in that agent build. The report is
informational and does not grant permissions or authorize jobs by itself.

## Built-in module lifecycle

Each built-in module implements `modules.Module`: a stable `Descriptor` and an
`Execute(context.Context, JobContext)` method. The descriptor declares the
module ID/version, supported platforms, capabilities, configuration fields,
permission needs, and metric names/units. Validate configuration and grants
before invoking the implementation. Each invocation gets its own context and
deadline; implementations must honor cancellation and avoid shared mutable
invocation state. The runner recovers panics into a failed job result, records
the error, and proceeds with other work. Modules should not start untracked
background work or write outside their declared scope.

These contracts describe the planned boundary; they do not claim that transport,
enrollment, persistence, scheduling, or runtime isolation has been implemented.

## Live channel

The poll loop above is too slow for logs, terminals, and live container views, so each agent also keeps one persistent WebSocket open to the server. The agent always dials out, to `/api/agent/channel` on the same server URL it already uses (`wss` for HTTPS, `ws` for the private-network HTTP option). The server never connects to an agent. Polling continues unchanged for backups and as a fallback, and an older server that lacks the endpoint simply leaves the live features unavailable while the agent retries occasionally.

The agent authenticates with its bearer credential. Both sides then exchange JSON text messages shaped as `ChannelFrame` in `internal/protocol/channel.go`, at most 1 MiB each:

1. The agent sends `hello` with the channel version, its agent version, and its capabilities.
2. The server checks the version, looks up the capabilities it stored for that agent (the agent's own claim cannot raise them), and answers `welcome`.
3. The server sends `call` frames and receives one `result` each, or sends `stream_open` and receives ordered `stream_data` chunks of at most 32 KiB followed by `stream_end`. `cancel` abandons either.

Rules that keep this safe:

- A method must be listed in the protocol's method table together with the Docker tier it needs. Both server and agent refuse unknown methods and methods above the agent's tier, so the host's local setting is the final authority.
- Requests carry a deadline that the agent enforces. At most 32 requests (calls plus streams) are in flight per agent, and 64 on the agent side.
- A stream consumer that falls more than 256 chunks behind is cut off with a `slow_consumer` error instead of growing memory.
- Errors are structured with a code and a message; agents report only a generic message for unexpected failures so host details do not reach the server.
- A newer connection from the same agent replaces the older one, and the server pings each connection every 20 seconds.

The only methods so far are `channel.ping` and `channel.info`; Docker methods arrive with the container management phases. The Containers page lists connected agents and can test a connection through `GET /api/channel/agents` and `POST /api/channel/agents/{id}/ping`.

### Docker read methods

These methods and their types live in `internal/protocol/dockerchannel.go`. `docker.list` and `docker.inspect` are calls. `docker.logs` and `docker.stats` are streams whose chunks are newline-delimited JSON (`DockerLogLine` and `DockerStatsSample`); the agent never splits a record across chunks. The server relays each chunk to the browser unchanged over a same-origin WebSocket at `/api/containers/{agentId}/{container}/logs` or `/stats`, then sends a final `{"end":true}` message, with `code` and `message` when the stream failed. Closing the page cancels the agent's stream. Because the browser WebSocket upgrade is a `GET`, the server refuses it for a user without the container read permission and for any cross-origin page.
