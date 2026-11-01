# Scheduled server tasks

Administrators can open **Admin → Scheduled tasks** to see every registered
periodic maintenance task, its enabled/configured state, interval, last start
and finish, last result/error, and next run. Times are displayed in the browser's
timezone. **Run now** starts a task asynchronously and continues after the page
closes. It also works for a task whose periodic scheduling is disabled.

Every enabled, configured task runs on each server startup, regardless of its
previous next-run time. Subsequent runs keep the interval from the last start,
without overlap. A manual run also starts a new interval. Scheduled failures
retry within one minute, or at their earlier normal due time. Settings and last-run metadata persist in
PostgreSQL. Disabling scheduling leaves an already running task to finish.

| Task | Normal interval | Function |
| --- | --- | --- |
| Scan agent bundles | 24 hours | Download newest compatible GitHub releases and apply cache retention |
| Telemetry retention | 1 minute | Roll up old samples and remove expired history |
| Evaluate alerts | 30 seconds | Evaluate configured alert rules |
| Schedule due backups | 30 seconds | Queue backup policies whose own schedules are due |
| OAuth expiration cleanup | 1 hour | Expire MCP codes/grants; available when MCP is configured |

Unconfigured tasks remain visible but cannot run. For example, bundle scans need
automatic release fetching configured on the API server. Backup policy schedules
remain in backup settings; this central task evaluates those policies rather
than replacing them. Notification delivery and deployment queues are continuous
workers, not periodic maintenance tasks.

The **Agent bundle retention** settings default to purging old cached releases
and keeping the newest **2** compatible versions. A version means the complete
platform/Restic asset set, not an individual executable. Keep 1–20 versions or
disable purging to retain downloaded history. Saving applies on the next scan;
choose Run now to apply immediately. Automatic selection requires the same
protocol major and Restic version as the server. An explicit release pin stays
pinned. Verified caches remain usable when GitHub is unreachable or scheduling
is disabled. With a disabled schedule and an empty cache, choose Run now to fetch.

Tasks are built-in server operations: administrators cannot submit arbitrary
commands or download URLs. Permission checks protect all list, run and settings
routes. PostgreSQL claims and per-run identities prevent overlapping execution
and stale completion updates. A still-leased run is not duplicated on startup;
after an unclean shutdown its bounded lease must expire before another worker
can reclaim it. Graceful shutdown cancels work and records the result first.

API routes:

- `GET /api/admin/scheduled-tasks`
- `POST /api/admin/scheduled-tasks/{id}/run`
- `PATCH /api/admin/scheduled-tasks/{id}` with `enabled` and `settings`

Bundle settings are `{"purgeOld":true,"keepVersions":2}`. Other built-in tasks
use an empty settings object. Changes affect future runs; a running task keeps
the settings captured when it started.
