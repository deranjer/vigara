# Metrics storage scale evaluation

## What this measures

The evaluation keeps the current PostgreSQL design as the baseline. It has two
modes because this checkout has no evidence of representative user telemetry
load:

- `audit` observes a database supplied explicitly by the operator. It reports
  PostgreSQL version, estimated live row counts, relation/table/index bytes,
  the raw observation window, and bounded read plans for device listing, recent
  raw samples, rollups, and retention candidates. The transaction is read-only,
  has a statement timeout, and runs `EXPLAIN (ANALYZE, BUFFERS)` only on SELECTs.
  PostgreSQL's row estimates may be stale. Timeout or missing-table results are
  reported as unavailable, not as zero.
- `synthetic` runs the existing Go telemetry `Store.Ingest` and `Store.Retain`
  methods in random, newly created schemas. Its default matrix is 12, 36, and 60
  devices; 30-second samples; raw retention at 25, 48, and 168 hours; and
  rollup retention at 90 and 365 days. Each scenario lasts ten simulated
  minutes by default. Sample interval, duration, device counts, and retention
  values can be changed within bounded limits.

Each synthetic snapshot includes eleven metrics across host CPU, memory, and
uptime; two filesystem mounts; one disk; and one network interface. Device
labels differ. This models ordinary host collection without using the maximum
512-series allowance for every host. Metrics are retained through the existing
PostgreSQL JSONB intake, deduplication, latest-snapshot, series registry,
rollup, and retention paths.

## Running it

Use an explicit DSN in the environment so credentials do not appear in command
history or output:

```powershell
$env:VIGIRA_METRICS_BENCH_DSN = 'postgres://user:password@localhost:5432/vigira_test'
go run ./apps/api/cmd/vigira-metrics-bench -mode audit
go run ./apps/api/cmd/vigira-metrics-bench -mode synthetic -confirm-synthetic
```

Synthetic mode accepts only loopback PostgreSQL hosts and database names with
a `test`, `dev`, or `bench` token (for example, `vigira_test`). Names marked
`prod`, `live`, or `public` are refused. It also requires
`-confirm-synthetic`. For
each case it creates a cryptographically random schema named
`vigira_metrics_bench_<token>`, places an ownership token in that schema, then
drops that exact schema only after verifying the token. It never truncates or
drops an existing schema or table. An interrupted run can leave an isolated
schema behind; inspect it and remove it manually if desired.

Useful bounded options include `-devices 12,36,60`, `-interval 30s`,
`-minutes 10`, `-raw-retentions 25h,48h,168h`, `-rollup-retentions 90d,365d`,
and `-statement-timeout 2s`. The tool rejects runs above 60 devices, 60 minutes,
one-hour sampling intervals, or statement timeouts longer than 30 seconds.

## Reading the results

JSON output includes server version/database identity, row and byte counts,
metric series cardinality, ingest batch median and p95, retention elapsed time,
read query median and p95, and text plans with shared-buffer hit/read counts.
Synthetic `first_post_ingest_read` is the first observed read after inserts;
the server and operating-system caches are not cleared, so it is not a true
storage-cold measurement. Warm results are seven repeated reads. Retention
duration measures the current bounded worker pass (up to ten batches of 500
raw snapshots); the default synthetic cases remain below that cap.

The short generated time series deliberately keeps the run bounded. It measures
the cost of current code paths and rough per-row relation growth, but it does
not populate the full raw or rollup retention horizon. Do not multiply these
small-run timings directly into production latency claims. A production
evaluation remains partial until a representative installation can be audited
over time. A separate metrics database is not proposed without observed
PostgreSQL capacity or query evidence that calls for it.

## Local measurement snapshot

The checked-in [JSON report](metrics-scale-results.json) was generated on
2026-10-11 against the local PostgreSQL 17.9 `postgres:17-alpine` fixture. It
contains the full plans, buffer counts, and all 18 combinations in the default
matrix. The 48h raw / 90d rollup cases reported:

| Devices | Synthetic snapshots | Raw rows before retention | Raw relation bytes before retention | 48h raw byte projection | 90d hourly rollup row projection | Ingest batch median / p95 | Retention elapsed |
|---:|---:|---:|---:|---:|---:|---:|---:|
| 12 | 240 | 240 | 540,672 | 155,713,536 | 285,120 | 5.78 / 7.81 ms | 22.15 ms |
| 36 | 720 | 720 | 1,441,792 | 415,236,096 | 855,360 | 8.34 / 13.24 ms | 93.33 ms |
| 60 | 1,200 | 1,200 | 2,326,528 | 670,040,064 | 1,425,600 | 4.95 / 6.19 ms | 96.65 ms |

All three cases rolled their raw rows into hourly aggregates. For the 60-device
case, the first post-ingest history read took 2.58 ms; seven repeated reads had
a 0.52 ms median and 1.51 ms p95. The table bytes are from only 10 simulated
minutes. The 48h raw estimate multiplies the measured raw-table bytes per row
by expected 30-second samples over 48 hours. The rollup estimate multiplies
observed series cardinality by hourly buckets over 90 days. These linear
estimates have wide uncertainty because actual metric labels, JSON payloads,
PostgreSQL storage settings, and host cache/resources vary. This run is a local
baseline, not a production capacity test or a reason to change database choice.
