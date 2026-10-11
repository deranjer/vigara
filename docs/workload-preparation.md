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
