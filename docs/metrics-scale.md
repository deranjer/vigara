# Metrics storage scale evaluation

## What this measures

The evaluation keeps the current PostgreSQL design as the baseline. It has two
modes because this checkout has no evidence of representative user telemetry
load:

- `audit` observes a database supplied explicitly by the operator. It reports
  PostgreSQL version, estimated live row counts, relation/table/index bytes,
  a bounded observed-volume report, the raw observation window, and bounded
  read plans for device listing, recent raw samples, rollups, and retention
  candidates. The transaction is read-only, has a statement timeout, and runs
  `EXPLAIN (ANALYZE, BUFFERS)` only on SELECTs. PostgreSQL's row estimates may
  be stale. Timeout or missing-table results are reported as unavailable, not
  as zero.
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

Each device also receives three expired snapshots at distinct hourly offsets
and keeps the generated snapshots that fall inside the raw window. This makes
the executed current-time history query combine multiple hourly rollup buckets
with recent raw samples. The anchors are a small query fixture, not a populated
retention horizon; the reported projections still use the generated short run.

## Running it

Use an explicit DSN in the environment so credentials do not appear in command
history or output:

```powershell
$env:VIGIRA_METRICS_BENCH_DSN = 'postgres://user:password@localhost:5432/vigira_test'
go run ./apps/api/cmd/vigira-metrics-bench -mode audit
go run ./apps/api/cmd/vigira-metrics-bench -mode synthetic -confirm-synthetic
```

For deployment evidence, point `VIGIRA_METRICS_BENCH_DSN` at the intended
installation and run `-mode audit` with an account that can read the telemetry
tables. Preserve the JSON report with the collection time and workload context;
repeat during representative normal and peak periods when making a capacity
decision. Do not use fixture or synthetic output as a substitute for those
observations.

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
`history_points_returned` counts rows from the executed 30-day history query;
each scenario must return at least three points and retain both raw rows and
multiple rollup buckets.

Audit JSON's `observed_volume` section reports exact raw `snapshot_rows`,
`metric_datapoints` from each stored snapshot's `metrics` array, and
`distinct_reporting_agents` for the recent hour and recent 24 hours. It also
includes 24 consecutive one-hour buckets. Every window uses the same
`anchor_utc` and UTC
half-open bounds `[start_utc,end_utc)`, so a sample at a start belongs to that
window and one at its end belongs to the next (or is excluded at the anchor).
`min_observed_at` and `max_observed_at` describe observed rows within that
window. `available`, `empty`, and `unavailable` distinguish measured counts,
successfully measured zero rows, and a query that could not be measured.

These are counts of raw rows still retained by PostgreSQL. Retention can make a
24-hour window incomplete, and ingest gaps, delayed agent reports, or host clock
skew can also affect the observed history. The audit therefore reports counts
and observed timestamp bounds instead of dividing by a requested duration and
presenting that as an ingestion rate or sampling cadence. The window results
describe only the explicitly selected database; a local fixture or synthetic
workload is not evidence of deployment load.

The PostgreSQL boundary, empty-data, missing-table, and statement-timeout
regression runs when `VIGIRA_METRICS_BENCH_TEST_DSN` points to a local
`test`/`dev`/`bench` database. It creates uniquely named temporary schemas and
drops them during cleanup. For example, set this variable to the disposable
`vigira_test` database before running the command-package Go tests.
Synthetic `first_post_ingest_read` is the first observed read after inserts;
the server and operating-system caches are not cleared, so it is not a true
storage-cold measurement. Warm results are seven repeated reads. The checked-in retention timings used the former worker cap of ten batches of 500
raw snapshots. The current worker drains all eligible work within its context
deadline; the default synthetic cases remain below the former cap.

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

| Devices | Synthetic snapshots | Raw rows before retention | Raw rows after retention | Rollup rows | History points returned | Raw relation bytes before retention | 48h raw byte projection | 90d hourly rollup row projection | Ingest batch median / p95 | Retention elapsed |
|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 12 | 240 | 240 | 192 | 528 | 5 | 540,672 | 155,713,536 | 285,120 | 9.45 / 16.97 ms | 29.39 ms |
| 36 | 720 | 720 | 576 | 1,584 | 5 | 1,441,792 | 415,236,096 | 855,360 | 7.49 / 10.21 ms | 68.67 ms |
| 60 | 1,200 | 1,200 | 960 | 2,640 | 5 | 2,326,528 | 670,040,064 | 1,425,600 | 4.15 / 5.50 ms | 84.38 ms |

All three cases retained raw rows and several hourly rollup buckets, and the
actual history query returned five points in each case. For the 60-device case,
the first post-ingest history read took 1.51 ms; seven repeated reads had a
0.94 ms median and 1.29 ms p95. The measured generated workload covers only 10
simulated minutes, plus the three query fixture anchors per device. The 48h
raw estimate multiplies the measured raw-table bytes per snapshot
by expected 30-second samples over 48 hours. The rollup estimate multiplies
observed series cardinality by hourly buckets over 90 days. These linear
estimates have wide uncertainty because actual metric labels, JSON payloads,
PostgreSQL storage settings, and host cache/resources vary. This run is a local
baseline, not a production capacity test or a reason to change database choice.

## Current retention policy

The runtime defaults are now 720 hours of raw history and 365 days total history,
with hourly summaries after the raw window. The checked-in 48h/90d figures above
describe the old policy and are not capacity evidence for the longer defaults.
The existing short synthetic matrix still exercises shorter configurations; use
representative retained data with audit mode before making capacity claims.
See [telemetry retention](telemetry.md#retention-and-intake-limits) for sizing
and storage-health logs.
