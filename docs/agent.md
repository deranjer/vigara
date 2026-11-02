# Agent enrollment and services

## Device hardware inventory

Device info shows a used/free capacity bar for every filesystem in the latest
telemetry snapshot. Bars represent each drive's own total capacity; missing free
space remains unknown. Operating systems have platform-specific icons.

Updated agents collect static hardware inventory on their first report and then
refresh it hourly. The inventory has its own collection timestamp. Windows uses
CIM to report graphics adapter names and driver versions, memory module slots,
capacities and configured speeds, baseboard manufacturer/model/version, and BIOS
manufacturer/version. GPU VRAM is omitted because the CIM AdapterRAM field cannot
reliably represent modern adapter capacities. PowerShell runs without a visible
window and has a 12-second timeout.

Linux reads motherboard and BIOS information from sysfs and identifies display
adapters by PCI vendor/device IDs and their bound driver. Memory module details
require an installed `dmidecode` and permission to read SMBIOS data; the agent
does not elevate privileges. This command has a three-second timeout. Other
platforms report these inventory categories as unsupported. Missing tools,
permission restrictions, or unavailable firmware tables leave individual
categories unavailable without failing ordinary monitoring. Serial numbers are
not included. Older agents continue to work and show unreported hardware fields.

## Enrollment

The API creates one-use enrollment tokens that are limited to a set of module
capabilities. Sign in as an administrator, then create a token with the
authenticated `POST /api/agents/enrollment-tokens` endpoint:

```json
{"capabilities":[],"expiresInHours":24}
```

The response includes the token once and its expiry. For monitoring-only agents, use an empty list. Backup-capable enrollment tokens
must include `restic.backup`; managed backup bundles handle this automatically. Keep the token private
and use it once. Enrollment creates a distinct device record and agent
identity; the agent identity receives a unique random credential, stored by the
server only as a hash. Revoke an identity with the authenticated
`POST /api/agents/{agentID}/revoke` endpoint. Revocation takes effect on the
next request.

Enrollment is interactive so the one-time token does not appear in process
arguments. The agent asks for it on standard input and saves its returned
credential in its private state file:

```sh
vigara-agent enroll --server https://vigara.example --name "Kitchen PC"
```

The agent accepts HTTPS server URLs. HTTP is accepted only for localhost and
loopback development. Redirects are never followed, which prevents a server
redirect from forwarding the bearer credential to another endpoint. The agent
polls outbound, reconnects with bounded backoff and jitter, saves complete
configuration snapshots atomically, and retains up to 500 job reports while
offline. It rejects requests to exceed the capabilities allowed by its
enrollment token.

## Linux systemd

Create the service account and state directory, enroll while running as that
account, install the supplied unit, and enable it:

```sh
sudo useradd --system --home /var/lib/vigara-agent --shell /usr/sbin/nologin vigara-agent
sudo install -d -o vigara-agent -g vigara-agent -m 0700 /var/lib/vigara-agent
sudo -u vigara-agent /usr/local/bin/vigara-agent --config /var/lib/vigara-agent/agent.json enroll --server https://vigara.example --name "Kitchen PC"
sudo install -m 0644 deploy/systemd/vigara-agent.service /etc/systemd/system/vigara-agent.service
sudo systemctl daemon-reload
sudo systemctl enable --now vigara-agent
```

The unit runs with a dedicated account, a private state directory, a read-only
system view, and no new privileges. Review the permissions needed by the
modules you choose to deploy before granting them on a host.

## Windows service

Run enrollment from an elevated terminal, enter the one-time token at the
prompt, then register the service:

```powershell
New-Item -ItemType Directory -Force 'C:\Program Files\Vigara'
& 'C:\Program Files\Vigara\vigara-agent.exe' --config 'C:\ProgramData\Vigara\agent.json' enroll --server https://vigara.example --name 'Kitchen PC'
sc.exe create VigaraAgent binPath= '"C:\Program Files\Vigara\vigara-agent.exe" --config "C:\ProgramData\Vigara\agent.json" run' start= auto
sc.exe start VigaraAgent
```

The Windows agent runs in the background as the `VigaraAgent` service; it does not currently have a tray icon. Run the bundle installer from PowerShell opened **as Administrator**. The installer keeps its window open to show success or failure (use `-NonInteractive` only for automated installs), verifies service startup, and can reuse a saved enrollment when repairing an interrupted installation.

To inspect an installation from an elevated PowerShell terminal:

```powershell
Get-Service VigaraAgent
& 'C:\Program Files\Vigara\vigara-agent.exe' --config 'C:\ProgramData\Vigara\agent.json' run
```

Stop the service before running the agent in the foreground for diagnosis. Running the downloaded binary without `--config` looks for a separate per-user state file under `%APPDATA%\vigira\agent.json`, which can report “not enrolled” even when the service is enrolled. A device listed as enrolled only confirms the server accepted enrollment; it does not confirm local credential persistence or service startup. If enrollment was accepted but no state file was saved, its one-use token cannot be reused: download a fresh bundle.

The state directory and file use a protected Windows ACL for the enrolling
account, local administrators, and LocalSystem, so the service can read the
credential while ordinary users cannot. The service handles stop and shutdown
signals and reconnects after transient network failures.

The current agent build advertises compiled module capabilities that are
enabled by valid local operator configuration; a capability report does not
grant execution by itself. A job still needs enabled desired configuration and
an explicit scoped permission grant. The agent refuses jobs it cannot support.
When choosing a custom `--config` path, place it in a dedicated private
directory that the service account can access; the agent secures newly created
state directories and always protects the credential file itself.

## Downloading configured client bundles

The Devices page offers Windows x64, Linux x64, and Linux ARM64 bundles when
the server has verified release artifacts. Build them from the repository root
and copy the output directory to a private, operator-managed location readable
by the API process:

```sh
python scripts/package_agent_releases.py 0.1.0 ./agent-releases
```

Published server images can automatically fetch matching agent assets; see
[automatic release downloads](deployment/docker.md#automatic-agent-release-downloads).
The manual procedure below remains supported for offline or private deployments.

Set `VIGIRA_AGENT_RELEASE_DIR` to that directory and `VIGIRA_PUBLIC_URL` to the
reachable HTTPS base URL of this Vigara instance (for example
`https://vigara.example`). The directory must contain the generated
`manifest.json` and fixed artifact filenames. The API verifies each SHA-256
digest on availability checks and downloads. Missing or invalid artifacts are
shown as unavailable; the server does not build binaries during downloads.

### Server addresses and plain HTTP

Bundles point agents at a server URL chosen from addresses the operator configures:

- `VIGIRA_PUBLIC_URL` is the default address.
- `VIGIRA_PUBLIC_URLS` is an optional comma-separated list of alternates, for example a LAN address and a Tailscale address. When more than one address is configured, **Add device** lets the administrator pick which one the new device should use. A request can only select a configured address; it cannot supply its own.

HTTPS addresses are always accepted. Plain HTTP is accepted only when you set `VIGIRA_ALLOW_INSECURE_AGENT_HTTP=true` **and** the host is a private-network address: loopback, RFC 1918 (`10/8`, `172.16/12`, `192.168/16`), IPv6 unique-local or link-local, the Tailscale/CGNAT range `100.64.0.0/10`, a `.local` or `.ts.net` name, or a single-label name such as `nas`. A public IP or dotted public hostname must use HTTPS even with the flag on, and entries that break the rule are ignored with a startup warning.

Plain HTTP means the agent's credential, telemetry, and inventory cross the network unencrypted, so anyone who can observe or alter that traffic can impersonate the agent or read its data. Use it only on a network you trust or over a VPN such as Tailscale (whose traffic is already encrypted), and never across the internet. The Add device dialog shows a warning whenever the selected address is not encrypted, and the installers repeat it. Prefer HTTPS where practical: for example put Caddy in front with `tls internal`, or use `tailscale serve` for a real certificate on a `*.ts.net` name.

A bundle for an HTTP address passes `--allow-insecure-private-http` to `enroll`, which stores that choice in the agent's private state file so later polls keep working. To enroll by hand, add the same flag:

```sh
vigara-agent --config /var/lib/vigara-agent/agent.json enroll --server http://192.168.1.97:8080 --name "Kitchen PC" --allow-insecure-private-http
```

Without the flag the agent still accepts HTTP only for `localhost` and loopback addresses.

Authenticated bundle creation issues a single-use enrollment token that expires
after 30 minutes. The archive contains a bootstrap file, so handle it like a
secret. Run its installer on the target device; after successful enrollment the
installer removes the extracted bootstrap file and the agent stores its
per-device credential in protected state. Canceling a waiting enrollment
revokes its token. Backup-enabled bundles install pinned Restic and enable server-managed backup
configuration. Selecting **Enable Docker access** also bundles the endpoint and
access level and configures the agent service during installation. See [Managed device
backups](managed-backups.md).

### Configure Docker during installation

In **Add device**, select **Enable Docker access** and choose view-only,
management, container terminals, or (Linux only) stack deployment. Linux defaults
to `unix:///var/run/docker.sock`; a loopback HTTP IP and port can be selected
instead. Windows requires an already configured loopback HTTP endpoint; named
pipes are not supported. The installer never enables a network listener on Docker.

On Linux the installer writes a private `/etc/vigara-agent/docker.env`, adds a
systemd override, and grants the service account membership in the selected
socket's group without changing socket permissions. The service permits Unix
sockets. It probes Docker as that account before reporting success. For stack
deployment it checks the Compose CLI, creates the selected managed root without
changing existing files, and runs as root with that root writable in the service
filesystem restrictions. Encrypted stacks still require an operator-provisioned
SOPS binary and age identity. `VIGIRA_COMPOSE_TARGETS_FILE` is optional for new
managed stacks; set it only to approve existing deployment targets.

Windows installs the selected `VIGIRA_DOCKER_ENABLED`, `VIGIRA_DOCKER_HOST`, and
`VIGIRA_DOCKER_MODE` values in the service environment, preserving unrelated
settings, after checking the local endpoint. Both installers preserve existing
Docker settings when the option is not selected. A failed endpoint check is an
installation error; correct the local prerequisites and rerun the installer.

Docker socket or unrestricted API access can control the host, even when Vigara
is configured for view-only access. Only select it for a trusted agent installation.

## Reinstalling and deleting devices

Device names in Add device must be unique (ignoring case and surrounding whitespace). For an existing device, use **Device info → Reinstall agent**. This issues a new 30-minute, one-use bundle bound to the existing agent and platform, preserving its identity, policies, and history. Permissions remain unchanged unless you select **Configure Docker access during reinstall**. That explicit choice updates only Docker permissions when the bundle is enrolled; backup permissions are preserved. Downloading alone changes neither the current credential nor its permissions. Successful enrollment replaces the credential and applies the approved permissions. A newer reinstall bundle cancels earlier pending reinstall bundles.

For Windows, extract the bundle and run **install.cmd**. It requests administrator permission and opens a PowerShell window that remains visible. Enrollment accepted by the server does not itself prove that the service was installed or is running.

To move a Windows computer from a lab server to production, download a fresh **Add device** bundle from production, extract it, and run **install.cmd**. When a saved enrollment is found, the installer shows the saved and bundle server addresses and offers a reset. Type exactly **RESET** after reading the warning; pressing Enter preserves the saved enrollment. You can also run `./install.ps1 -ResetEnrollment` from an administrator PowerShell terminal to explicitly request the confirmation. Reset is unavailable with `-NonInteractive` or a **Reinstall agent** bundle.

Reset creates a new identity on the target server. History and policies stay with the old device record; local settings, managed-backup configuration, and queued reports are not carried over. Backup files are not deleted. The installer saves the previous state as `C:\ProgramData\Vigara\agent.before-reset-<unique-id>.json` in the protected state directory, and restores it if enrollment fails before credentials are saved. If enrollment succeeds but service installation fails, it keeps the new credentials so rerunning the installer can repair the service. The old server record is not deleted automatically.

Linux x64 and ARM64 use the same reset flow. With a fresh **Add device** bundle,
run `sh install.sh --reset-enrollment` and type exactly `RESET`. Interactive
installation also offers the choice when saved enrollment exists. Enter keeps
the saved enrollment; `--non-interactive`, missing bootstrap settings, and
**Reinstall agent** bundles cannot authorize a reset. The previous state is
backed up privately as `/var/lib/vigara-agent/agent.before-reset-<unique-id>.json`.
Failed enrollment restores it and restarts the previously running service;
successful enrollment retains its new credentials even if service startup fails.

Administrators can permanently remove a device under **Device settings → Delete device**. The server requires the exact current device name. Deletion removes the device record, agent credentials, monitoring data, backup policies and job history, and stack deployment records. An audit entry remains. Active or uncertain backup jobs and stack deployments must be resolved first. Deletion does not uninstall software, stop containers, modify Git repositories, or erase backup files in their storage. Adding the computer again creates a new record.

## Migrating the original Vigira name

New downloads contain `vigara-agent.exe` (Windows) or `vigara-agent` (Linux). The Windows service is `VigaraAgent`, with credentials in `C:\ProgramData\Vigara\agent.json` and its binary in `C:\Program Files\Vigara`. The Linux unit is `vigara-agent.service`, with state in `/var/lib/vigara-agent`.

Installers recognize the original `VigiraAgent` / `vigira-agent.service` installations and migrate their saved state without creating a new device. Original files remain available for rollback. Windows migration preserves the service environment and supports the original LocalSystem installation; a custom account or binary path requires manual migration. Linux migration retains the legacy service account so operator-managed backup files and configuration remain accessible.

Use `VIGIRA_*` environment names for all configuration. Existing database names, server storage directories, session cookies, and protocol identifiers remain compatible and are not renamed automatically.

Windows capacity collection covers mounted local drives. Protected system/recovery volumes do not suppress other measurements. A drive without disk-performance support has no throughput series; actual access failures still identify the affected drive rather than reporting a false zero.
