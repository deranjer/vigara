# Workload-aware backup preparation

The managed agent can prepare a PostgreSQL logical archive before running a Restic backup. This first workload uses `pg_dump` custom format and checks the resulting archive with `pg_restore --list`. Restic receives the finished archive only after both commands succeed. A preparation error or cancellation fails the job; an older dump is never reused.

Preparation is opt-in per private, operator-managed agent profile. Server job settings can select a profile ID but cannot set database connection values, staging paths, or executable paths. Configure libpq connection details in the private agent state and place credentials in a protected `PGPASSFILE`. Passwords, DSN values containing passwords, and free-form command arguments are not supported.

Example PostgreSQL-only profile:

```json
"resticProfiles": {
  "vigira-db": {
    "enabled": true,
    "repository": "D:\\Backups\\vigira-db",
    "sourcePaths": [],
    "postgresql": {
      "enabled": true,
      "environment": {
        "PGHOST": "127.0.0.1",
        "PGPORT": "5432",
        "PGDATABASE": "vigira",
        "PGUSER": "vigira",
        "PGPASSFILE": "C:\\ProgramData\\Vigira\\pgpass.conf",
        "PGSSLMODE": "verify-full"
      },
      "pgDumpExecutable": "C:\\Program Files\\PostgreSQL\\17\\bin\\pg_dump.exe",
      "pgRestoreExecutable": "C:\\Program Files\\PostgreSQL\\17\\bin\\pg_restore.exe",
      "stagingDirectory": "D:\\VigiraPrivate\\staging"
    }
  }
}
```

The optional staging directory must already exist and be private to the agent service account. Each attempt creates its own unique child directory, applies owner/service-account and administrator-only access on Windows (or mode `0700` on Unix), writes a `0600` archive, validates it, then renames it to the finished archive name. That exact attempt directory is removed after Restic finishes. A cleanup failure makes the job fail and reports a safe error code. Concurrent jobs therefore cannot share or replace each other's archive.

The PostgreSQL archive is a transactionally consistent logical snapshot of one database at `pg_dump` time. It does not include cluster roles, globals, or tablespaces. If the profile also lists ordinary `sourcePaths`, those files are included in the Restic snapshot but are not transactionally coordinated with the database dump. Do not list PostgreSQL data directories, VM disks, or live container volumes and treat their file copies as database backups. The workload support here makes no consistency claim for those files.

Use a client toolchain compatible with the server's PostgreSQL major version; Vigira's checked-in deployment uses PostgreSQL 17. Record the actual server and client versions with recovery procedures. Restore the selected archive into a new empty database and verify application data before using it. The agent never stops a database or API service.

## Disposable PostgreSQL restore drill

The opt-in Go drill exercises the real `backupprep.PostgreSQL` path, then restores its archive and checks a representative table and row. It requires PostgreSQL 17 server/client tools (`pg_dump` and `pg_restore`) and a PostgreSQL user allowed to create and drop databases. With the variable unset, the test reports a skip. When enabled, missing client tools fail explicitly; all other connection, dump, restore, and verification failures fail the test.

The drill accepts only a loopback administrative connection URL with `sslmode=disable` in `VIGIRA_POSTGRES_DRILL_URL`. It connects to the `postgres` maintenance database and creates uniquely named source and restore databases itself. The URL's database component is ignored. It never takes a destination database name from configuration, and drops both databases on exit. Use a disposable local PostgreSQL 17 instance; do not point this at a production or shared cluster. The URL password is placed in a private temporary `PGPASSFILE` for the client tools and is not passed in command arguments.

Run the drill in Docker from the repository root (PowerShell). This keeps PostgreSQL and its client tools in disposable containers, so no PostgreSQL installation is needed on the host:

```powershell
$fixtureName = 'vigira-pg17-drill-' + [guid]::NewGuid().ToString('N')
$repo = (Resolve-Path '.').Path
$fixture = $null
try {
  $fixture = docker run --rm -d --name $fixtureName -e POSTGRES_PASSWORD=vigira postgres:17-alpine
  if ($LASTEXITCODE -ne 0) { throw 'Could not start disposable PostgreSQL 17 fixture' }
  do { Start-Sleep -Seconds 2; docker exec $fixture pg_isready -U postgres } until ($LASTEXITCODE -eq 0)
  docker exec $fixture postgres --version
  docker run --rm --network "container:$fixture" --mount "type=bind,source=$repo,target=/src,readonly" --workdir /src golang:1.24-alpine sh -c "apk add --no-cache postgresql17-client && go version && pg_dump --version && pg_restore --version && VIGIRA_POSTGRES_DRILL_URL='postgres://postgres:vigira@127.0.0.1:5432/postgres?sslmode=disable' go test ./apps/agent/internal/backupprep -run '^TestPostgreSQLRestoreDrill$' -count=1 -v"
  if ($LASTEXITCODE -ne 0) { throw 'PostgreSQL restore drill failed' }
} finally {
  if ($fixture) { docker stop $fixture | Out-Null }
}
```

The fixture uses the PostgreSQL 17 image already selected by `docker-compose.yml`; the Go 1.24 container installs the matching PostgreSQL 17 client package. The test logs the PostgreSQL server and client versions and removes both test databases before the fixture container stops. Run the ordinary `go test ./apps/agent/internal/backupprep` for the fast helper-based tests; the real-server drill stays opt-in.
