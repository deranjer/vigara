# Backup policies

Backup policies describe desired work for an enrolled agent and one operator-managed Restic profile. The API stores the repository destination, source paths, exclusions, retention choices, schedule, and missed-run and disconnected-device behavior. It never stores Restic credentials and never sends arbitrary paths, executable names, or environment values to an agent.

The policy's `profileId` must match a local `resticProfiles` entry on the agent. The authenticated API checks that the selected agent belongs to the selected device, is active, and was enrolled with the `restic.backup` capability. The server cannot inspect the agent's private profile, so a policy without unsupported exclusions is reported as `awaiting_local_profile`; this status does not claim that a backup can run. The profile-template endpoint returns repository and source paths without credentials. An operator must apply it to the private agent state file and configure `RESTIC_PASSWORD` locally.

The current runner applies the local profile's `sourcePaths` and repository. It does not support policy exclusions or retention. A policy with any exclusions is marked `blocked_unsupported_exclusions` and produces no scheduled jobs. Retention values remain recorded for future maintenance support; the server does not run `forget` or `prune`. Repository destinations are local paths or network shares. Remote Restic backend URLs are outside this scope.

Schedules use named IANA time zones, weekdays, and local `HH:MM` times. A wall-clock time that does not exist during the spring daylight-saving transition is skipped for that date. An ambiguous time during the fall transition produces one occurrence, using the single occurrence chosen by Go's time-zone rules; the second matching instant is not scheduled separately. `skip` marks an occurrence skipped when it is more than one minute late; `run_once` creates at most one late occurrence and advances to the next future schedule. Devices seen within five minutes count as connected. If a device is disconnected, `skip` records a skipped occurrence and `wait` records a durable waiting job using the original scheduled time.

The scheduler creates at most 100 records per pass and uses row locks to avoid duplicate generation across API instances. Each job has a durable UUID and an immutable policy revision snapshot. Policies are retired rather than deleted, preserving job and attempt history. The authenticated agent poll endpoint leases at most one backup attempt and returns its immutable job ID, opaque attempt token, profile ID, deadline, and `restic.backup` profile-scoped grant. Poll retries return the same active attempt. Results must match the authenticated agent, module version, job, and token; identical result retries are idempotent and conflicting payloads are rejected. Attempts are capped at 100. The server serializes jobs for an exact destination string and one active job per agent; Restic's repository lock remains the protection for path aliases or other external writers.

Expired leases and agent-reported restart interruptions are recorded as `unknown`, never automatically requeued. Unknown work blocks later jobs for that agent and exact destination pending explicit operator reconciliation. Expiry does not cancel a process that may still be running on a disconnected device. The server does not unlock Restic repositories. Maintenance operations such as `forget` and `prune` remain unsupported and receive no execution permission.

Authenticated endpoints:

- `GET /api/backups/repositories` lists configured agent-profile destinations.
- `GET|POST /api/backups/policies` lists or creates policies.
- `GET|PUT|DELETE /api/backups/policies/{id}` reads, replaces, or retires a policy.
- `GET /api/backups/policies/{id}/jobs?limit=100` reviews up to 100 recent durable jobs.
- `GET /api/backups/policies/{id}/profile-template` returns a local profile fragment when exclusions are empty.
