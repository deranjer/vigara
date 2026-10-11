# Recovery runbook

This runbook restores Vigira's PostgreSQL data and configuration alongside files protected by Restic. Restic is an independent recovery path: you can use its official command-line client even if Vigira is unavailable. Restic may create and remove normal repository lock files while checking or reading; the recovery steps do not change snapshot contents or restore over original source paths.

## What to protect

Keep a small, encrypted recovery set separate from the machine and storage that hold the live service. Restrict access to the people who need to restore it, and record where the key material is escrowed:

- A PostgreSQL logical dump, the PostgreSQL major version used for the server, and the Vigira release/build and migration level associated with the dump.
- The deployment configuration needed to start the same Vigira release: the `.env` or equivalent service environment, Compose file and any deployment-specific overrides, plus paths and permissions for mounted files. This includes `VIGIRA_DATABASE_URL`, `VIGIRA_ADDR`, `VIGIRA_COOKIE_SECURE`, and configured retention values. The repository's `.env.example` contains development defaults, not production secrets.
- The independent Restic repository location and its passphrase. Escrow any backend access credentials, client certificates, or network-share access required to reach that repository. Store these separately from the repository and from the host being backed up. Without the passphrase and any required backend credentials, encrypted snapshots cannot be opened.
- Each enrolled agent's private state file and the matching service account/ACL context, if continuity of its identity and local Restic profiles is needed. The state file contains the agent bearer credential, server URL, last configuration, queued reports/telemetry, and, in builds with the Restic agent integration, local profile paths and secrets such as `RESTIC_PASSWORD`. Protect it as a credential. By default, the CLI uses the OS user config directory under `vigira/agent.json`. The Linux systemd service example explicitly uses `/var/lib/vigira-agent/agent.json`; the Windows service example uses `C:\ProgramData\Vigira\agent.json`. A custom `--config` path or `VIGIRA_AGENT_CONFIG` value must also be included. If recovering the exact identity is unnecessary, enroll a replacement agent and revoke the old identity after service is restored.
- Any TLS private keys and certificates held outside the application configuration, plus administrator account recovery information. Vigira stores password hashes and session-token hashes in PostgreSQL; preserve the database to retain the local user account. Session cookies themselves are not a recovery secret and can be issued again by signing in.

Vigira currently has no separate application encryption/signing key in its configuration. Do not invent one or treat the agent credential as a server-wide key. Keep database credentials, Restic passphrases, backend credentials, and agent state under separate access controls where practical; a copy of the Restic repository alone is not sufficient to decrypt it.

The supported application target is PostgreSQL 17 in the checked-in Compose file. For a deployed instance, record and restore the actual PostgreSQL major version first; use a compatible client toolchain and application release. Apply only the migrations appropriate to the selected Vigira release. A dump from a newer schema may not be usable by an older server build.

## Make a database backup

For a live PostgreSQL server, `pg_dump` takes a transactionally consistent logical snapshot of one database while it is in use. Use the custom archive format so it can be inspected and restored into a fresh database. Supply credentials through your normal protected PostgreSQL mechanism (`.pgpass`/`PGPASSFILE` or a secret manager). The examples use separate libpq settings and do not put a password in command arguments or shell history.

Linux/macOS example (set these for the backup database; point `PGPASSFILE` to a protected file with the matching host, port, database, and user entry):

```sh
umask 077
export PGHOST=localhost PGPORT=5432 PGDATABASE=vigira PGUSER=vigira PGPASSFILE=/secure/escrow/pgpass
pg_dump --format=custom --file=/secure-backup/vigira-2026-10-10.dump
pg_restore --list /secure-backup/vigira-2026-10-10.dump
```

PowerShell example (create the destination directory first and restrict its ACL to the service/recovery administrators; point `PGPASSFILE` to a protected PostgreSQL password file):

```powershell
$env:PGHOST = 'localhost'
$env:PGPORT = '5432'
$env:PGDATABASE = 'vigira'
$env:PGUSER = 'vigira'
$env:PGPASSFILE = 'D:\SecureBackup\pgpass.conf'
pg_dump --format=custom --file='D:\SecureBackup\vigira-2026-10-10.dump'
if ($LASTEXITCODE -ne 0) { throw 'pg_dump failed' }
pg_restore --list 'D:\SecureBackup\vigira-2026-10-10.dump'
if ($LASTEXITCODE -ne 0) { throw 'Archive inspection failed' }
```

If a running database is in Docker Compose, run `pg_dump` against its published connection or execute it inside the container and copy the resulting archive out. Do not copy a live PostgreSQL data directory/volume as if it were a logical backup. A raw volume copy needs a database-aware, crash-consistent procedure; stop PostgreSQL cleanly before taking a cold copy. A VM/container snapshot is only useful if its storage snapshot is crash-consistent and the database can recover from it; coordinating a clean stop of Vigira API writers and PostgreSQL gives a simpler application recovery point. Preserve Compose configuration and mounted configuration files separately from the database dump.

For a coordinated recovery point across database records and other files, stop the Vigira API before the dump and keep it stopped until the related configuration/files have been captured. This prevents new account, agent, or report writes during that capture window. Agents can retain telemetry and reports while disconnected, but new scheduled Restic work requires the agent to poll the server; only a backup job already running when connectivity is lost may finish on its own. The agent's documented offline queues are bounded (100 telemetry snapshots and 500 job reports), so long outages can exhaust them. The database dump and a separate Restic snapshot are not atomically coordinated: record their capture times and use application/database-native consistency for any live data files.

The current Vigira schema uses PostgreSQL's `pgcrypto` extension, which is created by the migrations; the database dump does not by itself include cluster roles, global settings, or tablespaces. If your installation depends on custom roles or tablespaces, record and back them up separately using your PostgreSQL administrator procedure.

## Check and restore Restic snapshots

Install the official Restic `0.19.1` binary when recovering a repository written by the current managed runner; that runner rejects all other binary versions. Standalone Restic may support other compatible versions, but use the exact version recorded with the backup first. Set `RESTIC_REPOSITORY` to the repository and `RESTIC_PASSWORD_FILE` to a file containing the passphrase. Do not put the passphrase in a command argument, repository URL, or shell history. Restrict the password file (Linux mode `0600`, Windows ACL limited to the recovery account and administrators), and remove it securely according to local policy after recovery.

Linux/macOS:

```sh
export RESTIC_REPOSITORY=/path/or/mounted/share/to/restic-repository
export RESTIC_PASSWORD_FILE=/secure/escrow/restic-password
restic version
restic snapshots
restic check
```

For a full data read in addition to metadata/index checks, run `restic check --read-data`; this can take substantial time and bandwidth. If the command reports repository damage, preserve a copy of the repository before following Restic's repair guidance. Do not run `forget`, `prune`, `repair`, or `init` as routine recovery steps.

PowerShell:

```powershell
$env:RESTIC_REPOSITORY = 'D:\Backup\restic-repository'
$env:RESTIC_PASSWORD_FILE = 'D:\SecureBackup\restic-password'
restic version
restic snapshots
if ($LASTEXITCODE -ne 0) { throw 'Could not list snapshots' }
restic check
if ($LASTEXITCODE -ne 0) { throw 'Repository check failed' }
```

Choose a snapshot ID from `restic snapshots`, and restore into a new, empty alternate directory. The explicit snapshot ID prevents accidentally selecting a moving `latest` snapshot during an incident.

```sh
mkdir -m 0700 /srv/recovery/restore-2026-10-10
restic restore SNAPSHOT_ID --target /srv/recovery/restore-2026-10-10
```

On Windows, create a new destination directory that does not contain live data, then use `restic restore SNAPSHOT_ID --target 'E:\Recovery\restore-2026-10-10'`. Review the restored tree and compare representative files, ownership, permissions, and timestamps before copying anything into production. Restic's restore command does not recreate the original machine, services, or application consistency guarantees.

## Restore Vigira on a replacement host

1. Keep the affected production instance stopped or isolated. Provision a clean replacement host and install the PostgreSQL major version recorded for the backup, plus the matching Vigira release and its configuration files. Restore TLS certificates/keys and any referenced mounts from their protected recovery copies.
2. Create a new empty database with the intended owner. Restore the archive to that database; do not use `--clean` against a database that contains data. For example, from the PostgreSQL administrative environment:

   ```sh
   createdb --template=template0 --owner=vigira vigira_recovery
   pg_restore --exit-on-error --dbname=vigira_recovery /secure-backup/vigira-2026-10-10.dump
   ```

   Use the correct role and database name for your deployment. If the archive owner/ACL entries refer to roles absent from the replacement cluster, provision the intended roles first or have a PostgreSQL administrator review a restore with `--no-owner`/`--no-acl`. Do not blindly ignore restore errors.
3. Point the replacement Vigira configuration at the restored database. Apply the migrations required by that exact Vigira release before starting it, following the release's deployment instructions. Do not point an older binary at a newer schema. Start the matching application build in an isolated network and verify health and sign-in. If the database was restored to a differently named database, update `VIGIRA_DATABASE_URL` accordingly.
4. Reconnect agent state only after the server and network identity are correct. The server stores an agent credential hash; the private agent state has the corresponding bearer credential. Restoring only one side does not recreate the credential. If the database identity was lost, enroll a replacement agent with a new one-use token instead of copying an unrelated credential. Preserve/reinstall the state file with its private ACL and restore local profiles and their separately escrowed secrets. Agents that remained running during the outage can reconnect and submit queued reports/telemetry; review the bounded queue behavior and any gaps before returning them to routine operation.
5. Verify the restored inventory and recent data, login and logout, agent identities/revocation state, configuration, and application logs. Then restore user files from the selected Restic snapshot into their intended locations only after review. Resume normal writers and backups only when the replacement has been accepted.

## Consistency and recovery limits

The database dump is internally consistent at the time `pg_dump` took its snapshot, but it is not transactionally coordinated with a separate Restic file snapshot. If a backup source contains a live database, virtual machine, container volume, or files being actively edited, a file-level snapshot may capture an inconsistent mix of states. Quiesce the application or database, use its native backup/export method, or take a coordinated filesystem/VM snapshot before backing up those files. For Vigira's PostgreSQL database, prefer the logical dump above over Restic-copying the live data directory.

Restic restores files and directories. It does not provide bare-metal recovery, application-aware PostgreSQL recovery, or a Proxmox Backup Server integration. Current destination decisions cover local and network drives; off-site/object storage is deferred. Restore time, recovery point, and retention depend on the operator's schedule and repository; no RPO or RTO is promised.

## Practice recovery

At least once, use a disposable replacement environment and a recent backup copy to:

1. Inspect the repository and run `restic check` (and schedule a full `--read-data` check where practical).
2. Restore one chosen snapshot into an empty alternate path, verify a representative file, and document any required ACL/owner correction.
3. Restore a PostgreSQL dump into a newly created disposable database, start the matching Vigira version pointed only at that database, and verify health and sign-in.
4. Record elapsed time, missing credentials/configuration, version and migration details, and the precise steps needed to repeat successfully. Keep the test isolated from production and remove the disposable data when finished.

### Current implementation status

The checked-in server stores local users, session hashes, agent identities, enrollment tokens, reports, and (after migration `000012_android_file_backups`) Android selected-file metadata in PostgreSQL; its config is supplied through environment variables. If `VIGIRA_ANDROID_BACKUP_ROOT` is enabled, preserve that filesystem tree together with PostgreSQL. Android objects are stored as plaintext opaque files and are not automatically protected by Restic. See [Android companion setup](android.md) for the download API and storage limits. The managed Restic runner pins `0.19.1`, but it is not wired to the server's scheduling path on `main`. PR #10 (`feat(agent): integrate restic backup jobs`) adds local agent profiles and agent-side job execution; server-side job delivery remains unavailable until follow-up integration lands. ENG-148 policy/scheduling work is a durable configuration foundation: policy execution still depends on job delivery, exclusions are blocked/pending, retention is metadata, and no automatic repository pruning is performed. Treat those pieces as pending until they are present in the deployed release. Use the checked-in release and actual deployment configuration during recovery.

## References

- [Restic 0.19.1 scripting](https://restic.readthedocs.io/en/stable/075_scripting.html) and [restore](https://restic.readthedocs.io/en/stable/050_restore.html)
- [Restic repository checks and troubleshooting](https://restic.readthedocs.io/en/stable/077_troubleshooting.html)
- [PostgreSQL 17 `pg_dump`](https://www.postgresql.org/docs/17/app-pgdump.html), [`pg_restore`](https://www.postgresql.org/docs/17/app-pgrestore.html), and [database creation](https://www.postgresql.org/docs/17/manage-ag-createdb.html)
