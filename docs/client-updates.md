# Client self updates

Windows x64, Linux x64, and Linux ARM64 agents can install compatible packages
from their enrolled Vigara server. This does not update the Vigara server,
Android companion, operating system, Docker, or bundled Restic.

## Setup and controls

Run a new **Reinstall agent** bundle once on existing devices, or install a new
device bundle, with administrator/root access. This provisions the separate local
updater while preserving the device identity, settings, service environment,
queued reports, and backup configuration. The bundle must contain an agent release
built with self-update protocol `v1`; older bundles remain installable but cannot
provision self updates. The updater's enrollment copy is refreshed by reinstalling
after a credential rotation or server change.

In **Device info → Client updates**, administrators can choose **Update now** or
enable **Automatically install compatible client updates** per device. Automatic
installation defaults to off. Requests persist while a device is offline. The
helper checks approximately once a minute; no inbound device connection or
interactive elevation prompt is needed after initial setup. This is a queued
server instruction delivered by client pull, not an immediate WebSocket push.

Packages come from the server's selected verified release, using the existing
manual release directory or scheduled release downloads. The manifest must match
the server's agent protocol, pinned Restic version, and `selfUpdateProtocol: v1`.
Legacy packages without this marker cannot be installed automatically. Updates
converge on the server-selected package by digest, including same-version
rebuilds. Selecting an older compatible release on the server can downgrade
agents with automatic installation enabled.

An explicit request is bound to the package digest available when requested.
If that package is replaced before the device downloads it, the client never
installs different bytes under the same request. Request **Update now** again,
or enable automatic installation to follow the selected compatible release.

## Installation and recovery

The helper stages a bounded download, checks its exact size and SHA-256 digest,
and acquires an exclusive host-operation lock. Polling cycles, backup jobs, live
deployments, container operations, and open streams hold shared locks. Busy
devices defer installation until a later scan without cancelling active work.
Long-running terminals or log streams can therefore postpone updates.

It retains the previous executable, records a durable recovery journal, stops
the agent service, replaces the executable, and restarts the service. Success
requires a new server-observed poll with the target agent version within two
minutes. The baseline uses server time so client clock differences do not admit
stale check-ins. If startup or check-in fails, the helper waits for active work
to finish, restores the previous executable, and restarts it. A crash or reboot
during installation is recovered on the next scheduled run, before new updates.
Connectivity failure during health checking can also cause a rollback.

Failed installations and rolled-back packages are suppressed until an
administrator explicitly retries **Update now**, or a different package becomes
available. Transient download failures and busy hosts retry on later scans.
The dashboard shows the last reported state and updater check-in time; a stale
timestamp means the helper may be offline or unable to authenticate.

The updater changes the agent executable only. It never re-enrolls the device,
rewrites agent state, replaces Restic, or changes service configuration. Release
authors must preserve state compatibility with the previous binary to support
rollback. Scheduling windows, channels, fleet rollout batches, and independent
publisher signatures are not implemented in this first version.

## Trust and local services

Self updates require HTTPS with normal certificate verification, including on
private networks. Plain-HTTP monitoring remains supported, but the installer
disables self updates for that enrollment. Use a valid certificate trusted by
the machine; do not disable TLS validation.

The enrolled Vigara server is the update authority: enabling this facility
allows its administrators to install privileged agent code. Metadata and
packages are fetched from that fixed origin with the existing device credential;
redirects are rejected. Checksums authenticate downloaded bytes against the
TLS-protected server metadata. They are not independent publisher signatures.
Protect the server and its release storage accordingly.

Linux uses a root-owned `/usr/local/libexec/vigara-updater` copy, protected
configuration under `/var/lib/vigara-updater`, and `vigara-updater.timer` /
`vigara-updater.service`. The monitoring agent retains its restricted service
account. Only the operation-lock file is publicly readable; updater credentials
and the recovery journal are private to root.

Windows uses a protected `%ProgramFiles%\Vigara\updater` directory, a separate
`vigara-updater.exe` copy, and the `VigaraClientUpdate` scheduled task running as
LocalSystem. Task overlap is disabled. Executable replacement is performed only
after the Service Control Manager confirms `VigaraAgent` stopped.

The helper remains the version provisioned by the bundle installer and accepts
only its current update protocol. Reinstall a new bundle to refresh the helper
itself. Disable the timer/task locally to stop checks entirely. If recovery
cannot restart the service, retain the journal and `.update-old` file next to
the agent executable and inspect the helper's service/task result; a subsequent
run will retry recovery without silently discarding the previous executable.
