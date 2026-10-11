# Restic backup module

The agent includes a `restic.backup` module (`1.0.0`) for local and network-drive repositories. It invokes the locally installed Restic executable with argument arrays, checks the pinned `0.19.1` version before repository access and backup, validates the repository without initializing it, and parses bounded JSON-lines backup output. Output and error messages do not contain command output, repository addresses, credentials, or executable paths.

## Operator-owned profile

Backup destinations, source paths, credentials, and an optional executable path come from the trusted local operator's private agent state file. A server job can select only a profile ID; it cannot supply paths, credentials, environment variables, or executable paths. The state file is protected by the agent's private-file permissions and should be edited only by the service account or an administrator. Add a profile before enrollment if the agent should advertise the backup capability at enrollment. For example, add this field to the JSON state file before running `enroll`:

```json
"resticProfiles": {
  "home": {
    "enabled": true,
    "repository": "/mnt/backup/restic",
    "sourcePaths": ["/home/alice/Documents"],
    "environment": {"RESTIC_PASSWORD": "replace-with-a-local-secret"}
  }
}
```

On Windows, use a drive path or UNC network share. `local:` repository paths are also accepted. Backend URL/scheme repositories such as S3, REST, and SFTP are outside the current destination scope. The profile may include `"executable": "C:\\Program Files\\Restic\\restic.exe"` when the executable is not on the service account's PATH; omit it to use `restic` from PATH. Keep the state file and its containing directory private. Never place secrets in the repository, source paths, executable field, job settings, or logs. Environment keys that enable password commands or repository-file substitution are rejected.

The agent advertises `restic.backup` only when it has at least one enabled, valid local profile. The enrollment token must authorize that capability; an existing identity whose token did not include it must be enrolled again with an appropriately scoped token before the agent can advertise it. An enabled `restic.backup` entry in the received module configuration is also required before a job runs. Each job must use kind `backup`, module version `1.0.0`, a timeout from one second through seven days, a single `profileId` setting, and a `restic.backup` permission grant with scope `profile:<profileId>`.

## Results and boundaries

The bounded report output contains `resticStatus`, `backupOutcome`, `resticVersion`, `snapshotIds`, and an optional stable `errorCode`. Restic `succeeded` maps to protocol `succeeded`; `canceled` maps to protocol `cancelled`; an expired job deadline maps to `timed_out`. The v1 protocol has no `partial` or `interrupted` job status, so these runner outcomes map to protocol `failed` while retaining the precise Restic status and any valid snapshot IDs in structured output. Partial snapshots appear in authenticated backup history with a useful safe error message and require attention. Other failures also map to `failed` with a safe code. Reports are persisted in the bounded offline queue and retain their IDs across retries.

During execution the agent advertises optional progress support in its poll request. A supporting server opts that attempt into attempt-token-bound `BackupProgress` heartbeats, which contain only aggregate file and byte counts, are throttled, and are persisted to the private journal before transmission. The server accepts them only for the current active lease and extends that lease. If the heartbeat is rejected or cannot be confirmed, the agent stops the Restic child process. Older agents keep the existing two-hour execution limit; new agents also work with an older server that omits the opt-in response field. History distinguishes recent progress, stale leases, unknown outcomes, and terminal business outcomes.

The agent persists a private execution journal (job ID, attempt token, and stable result ID) before starting Restic. It saves the terminal result before clearing that journal. After restart, a leftover journal becomes an `unknown_outcome` report and is never executed again. The report queue retains result IDs across delivery retries. The server holds unknown attempts for operator reconciliation and does not blindly retry them. A lease expiry cannot prove that a remote Restic process stopped; Restic locks remain authoritative and are never removed automatically.

## Runner behavior

The `internal/restic` package invokes `restic --repo <repository> --json --quiet backup -- <paths...>`. The `--` separator prevents a source path from being interpreted as an option. Output capture is bounded to 16 MiB. Documented exit code 3 is partial only when valid summary output is available; exit code 130 is interrupted. Context cancellation is canceled and a deadline is interrupted at the runner boundary. Snapshot IDs are limited to 64 lowercase hexadecimal characters and retained when Restic returns a valid summary, including successful, partial, and interrupted outcomes. Updating `PinnedVersion` requires reviewing the [official releases](https://github.com/restic/restic/releases), [scripting documentation](https://restic.readthedocs.io/en/stable/075_scripting.html), and compatibility tests.
