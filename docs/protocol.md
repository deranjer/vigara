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
