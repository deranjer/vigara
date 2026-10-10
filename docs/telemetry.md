# Agent host telemetry

Enrolled agents post a bounded `TelemetrySnapshotBatch` to
`POST /api/agent/telemetry` using their existing bearer credential. The server
checks that the credential is active and owns every snapshot, then stores the
snapshot JSON with a server receive time. `(agent_id, report_id)` is unique, so
retries with the same snapshot ID are safe. Revoked credentials cannot ingest
reports.

Each snapshot has an observation time, OS and OS version, architecture, agent
version, CPU model and logical CPU count, memory and uptime summary, numeric
measurements with canonical names and units, capabilities collected, and
collection errors. A missing measurement means no valid value was available;
the agent does not substitute zero. A capability is listed only when a
collector successfully returned data. Throughput measurements use differences
between two cumulative counter reads and are omitted until a baseline exists.

Current measurements are `cpu_used_percent` (`%`), memory totals and
availability (`By`), `memory_used_percent` (`%`), `uptime_seconds`
(`s`), filesystem total and free bytes (`By`), and disk and network
read/write or receive/send rates (`By/s`). Filesystem, disk, and
network measurements carry mount, device, or interface labels. Collection is
bounded to 64 filesystems/devices/interfaces per snapshot. The agent keeps at
most 100 unacknowledged snapshots in its private local state and sends them in
batches of at most five, preserving IDs across retries.

The endpoint validates finite non-negative values, canonical units, percentage
bounds, item limits, identity, and timestamps. It accepts historical
observations for offline delivery and rejects timestamps more than five minutes
in the future. `last_seen_at` reflects server receipt time. This issue adds
durable raw ingestion; retention, rollups, and query APIs are separate work.

Migration `000005_telemetry` depends only on agent identities from `000003`;
its SQL does not use the inventory schema. If inventory migration `000004` is
also pending at deployment, apply the migrations together in numeric order.
