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

A rejected telemetry batch stays queued and is surfaced as a delivery warning;
it does not prevent the agent's normal configuration and job poll. The agent
retries the same snapshot IDs on the next cycle, including after a server or
protocol upgrade resolves the rejection. The queue is capped at 100 snapshots.

The endpoint validates finite non-negative values, canonical units, percentage bounds, item limits, identity, and timestamps. It accepts observations up to 48 hours old for offline delivery and rejects timestamps more than five minutes in the future. Observations older than 48 hours are acknowledged and discarded, so stale offline queues can drain without recreating data after deduplication expires. `last_seen_at` reflects server receipt time.

Migration `000005_telemetry` depends only on agent identities from `000003`;
its SQL does not use the inventory schema. If inventory migration `000004` is
also pending at deployment, apply the migrations together in numeric order.

## Retention and intake limits

Batch ingestion is atomic and idempotent by `(agent_id, report_id)`. Each agent can submit at most 10 snapshots per one-minute window; a rejected request returns HTTP 429 with `Retry-After: 60`. The 512-series limit applies to new metric name and canonical label combinations after migration. Existing series are grandfathered at migration so an installation already above the limit can keep reporting its current metrics. The agent credential state, deduplication keys, rate counters, and series registry are stored in Postgres.

Raw snapshots are retained for 30 days by default. `VIGARA_TELEMETRY_RAW_RETENTION_HOURS` accepts 25 through 720 hours; the 25-hour minimum leaves room for alert windows up to 24 hours plus their freshness margin. After the raw window, hourly UTC summaries retain history through day 365 by default. `VIGARA_TELEMETRY_ROLLUP_RETENTION_DAYS` accepts the raw-retention duration rounded up to days through 365. These are ages measured from observation time, not an additional year after raw expiration. Existing `VIGIRA_*` names remain supported; explicit `VIGARA_*` values take precedence. Existing deployments with explicit shorter settings retain those settings. Previously expired detail cannot be recovered by increasing retention.

Rollups contain count, sum, minimum, and maximum for each agent, metric, label set, and hour. Combining hours uses `sum / count`, never an average of hourly averages; peaks survive through min/max. Network and disk throughput metrics arrive as rates, so those rates can be aggregated directly. New cumulative counters must be converted to reset-aware rates before applying this policy. A latest host snapshot and its observation time remain separately available as device state after historical samples expire.

The worker runs on startup and every minute, draining eligible rows in transactions of at most 500 raw snapshots. Each pass has a 45-second deadline, so a backlog can continue on the next minute without monopolizing the database. Rollup writes and deletion of exactly the corresponding raw rows commit atomically. Cleanup statements delete at most 10,000 expired rollups or dedup keys per transaction and continue even when no raw rows need processing. Concurrent passes serialize each transaction using an advisory lock. Expiration is eventual: outages or an overloaded database can delay cleanup, which is reported as backlog. The 48-hour-and-five-minute dedup horizon exceeds the maximum accepted observation age and future allowance, preventing replay from recreating rolled-up data.

Structured server logs report `telemetry retention healthy` or `telemetry retention backlog`, including `retention_duration_ms`, `retention_lag_seconds`, and capped raw/rollup/dedup backlog counts. Counts stop at 10,001; that value means **at least** 10,001 rows. A lag exceeding five minutes or any saturated count produces a warning; errors and unavailable measurements are logged explicitly. `telemetry table storage` reports table/index bytes, estimated live/dead rows, and last automatic vacuum/analyze times. Migration `000020_telemetry_storage_maintenance` sets vacuum/analyze scale factors to 0.02/0.01 on the four high-churn tables. Keep autovacuum enabled: deletion makes space reusable after vacuuming, and does not normally shrink files immediately.

At 25 hosts reporting every 30 seconds, 30 days holds about 2.16 million snapshots. At an estimated 2–4 KB per JSON snapshot, that is roughly 4.3–8.6 GB before PostgreSQL indexes and row overhead. If each host averages 30 metric series, a full year of hourly buckets would be 6.57 million rows; normal operation retains raw data instead of hourly rows during the first month. These are planning estimates; actual payloads, labels and TOAST compression determine disk use. Use the read-only [metrics audit](metrics-scale.md) on a representative installation and monitor disk headroom, query latency, vacuum progress, and retention lag.

History API presets include 30 days, 90 days, and 365 days; custom intervals may span at most 365 days. Charts preserve the 20,000-point/64-series limits by combining whole hours for wide multi-series requests. Zooming restores available minute detail in the raw window or hourly detail afterward. All stored hours remain available even when the overview chart combines them. History requests have a ten-second database deadline.

Keep ordinary indexed PostgreSQL tables for now. If measured retention/vacuum costs justify partitioning later, migrate historical tables to time ranges and expire complete partitions only after their samples have been aggregated. A one-year cap does not require daily aggregates or another database service.

Migration `000006_telemetry_retention` follows `000005_telemetry`. Alert policy migration `000007_alerts` follows `000006`.

## Optional local network checks

An operator can enable HTTP and TCP health checks by setting
VIGIRA_NETWORK_CHECKS_CONFIG to a local JSON file. The file is not downloaded
from the server or included in agent state:

```json
{
  "checks": [
    {"name":"router-health","type":"http","url":"http://192.168.1.1/health"},
    {"name":"switch-https","type":"tcp","address":"192.168.1.2:443","timeoutMs":1000}
  ]
}
```

Checks are disabled when the setting is empty. At most 16 uniquely named checks
are accepted. Targets must use literal private, loopback, or link-local IP
addresses; hostnames and DNS resolution are not supported. HTTP permits only
HTTP(S), rejects URL credentials and query strings, disables proxies and
redirects, and reads at most 1 KiB of response content. Per-check timeouts are
100–3000 ms (1500 ms default), with a 5-second total batch deadline and a 15-second minimum interval between probes. A successful
HTTP 2xx response or TCP connection reports network_check_success (1 or 0)
and network_check_latency_milliseconds (ms). Labels contain only the
configured check name and protocol. Configuration errors do not include file
paths or endpoint values.

These checks do not establish support for any specific TP-Link model or
firmware. Model-specific TP-Link, SNMP, SSH, ICMP, and Prometheus collection
remain deferred until requirements and supported hardware are identified.
