# Managed device backups

Vigara can bundle Restic and provision one managed backup profile per Windows or
Linux device. Administrators select source folders, a repository, and a schedule
on the server. The agent applies revisions between jobs and reports readiness.
Existing operator-owned `resticProfiles` continue to work separately.

## Server setup

1. Set `VIGIRA_BACKUP_MASTER_KEY` to a base64-encoded, randomly generated 32-byte
   key. For example, generate one with Python:

   ```sh
   python -c "import base64,secrets; print(base64.b64encode(secrets.token_bytes(32)).decode())"
   ```

   Keep this key outside PostgreSQL and back it up separately. Do not replace it
   when upgrading. Existing encrypted passwords cannot be decrypted with a new
   key. An unset key disables managed provisioning and browsing; local profiles
   are unaffected. Secret storage uses AES-256-GCM with a fresh nonce.

2. Build and provision release artifacts:

   ```sh
   python scripts/package_agent_releases.py 0.1.0 ./agent-releases
   ```

   The packager builds the agent and pinned Restic `0.19.1` for Windows x64,
   Linux x64, and Linux ARM64. Restic is built from its upstream Go module using
   Go's public checksum database; its required Go toolchain is downloaded when
   necessary. The manifest records agent and Restic SHA-256 digests. The API
   verifies the Restic version and digest before issuing a backup-enabled
   bundle. The bundle also includes Restic's license. Set
   `VIGIRA_AGENT_RELEASE_DIR` and the usual reachable server URL settings.

3. For web browsing, install the same Restic version on the server. The Docker
   image includes it. A standalone server uses `restic` from PATH, or the
   operator-supplied `VIGIRA_RESTIC_EXECUTABLE` path.

4. Make backup storage accessible to the server and device. The two machines may
   see the same repository at different paths. Mount the repository into the API
   container and ensure its service account can open it and use normal Restic
   repository locks. Vigara does not mount or authenticate SMB/NFS shares.

## Add a device

In **Add device**, select **Enable managed backups**, then enter:

- Absolute source paths on the device. Enter each path and press Enter or choose
  **Add path**. Remove individual entries from the list as needed.
  **Browse local folders** opens a picker on the computer running the browser;
  browsers expose only folder names, so enter the full path before adding the
  selection. File contents are never read or uploaded.
- A dedicated repository path on the device, with its own **Browse local
  folders** option when setting up a repository. The same browser full-path
  restriction applies. For a new repository, its parent
  directory must already exist. Vigara creates the final directory and only
  initializes it if it is empty and Restic reports a missing repository.
- An optional server path to the same repository for browsing and downloads.
- Backup days, local time, timezone, and whether scheduling is enabled.

A new repository receives a generated password. For an existing repository,
clear **Create a new encrypted repository** and provide its existing password.
The bootstrap archive contains enrollment credentials and the management opt-in,
but does not contain repository passwords or the backup configuration. During
enrollment the server creates the policy and encrypted profile atomically.

Run the installer on the device. It installs the bundled Restic alongside the
agent and records the management opt-in in the agent's private state. Restic is
not independently upgraded to an arbitrary latest version; rebuild and reinstall
approved agent releases to update the pinned executable.

On Linux, backup-enabled installation explicitly installs a systemd override
running the agent as root, with `ProtectHome=false` and `ProtectSystem=false`, so
it can read selected host files and write the repository. Ordinary monitoring
installations retain the restricted service. This authorizes administrators to
select host backup paths. On Windows the standard service uses LocalSystem;
network shares must allow that service identity access. A user's mapped drive
or interactive share login is not sufficient for the service.

Under **Device → Backups**, wait for **Ready** and export recovery information.
The export includes the repository password: store it securely outside the host
and the repository. It is restricted to users with `secrets.reveal` permission.

## Edit paths and schedules

Use **Device → Backups → Edit backup settings**. Changes are delivered on the
next poll, normally within about 30 seconds when the agent is idle. Offline
devices show **Changes pending**. The UI displays the desired and applied
revisions, and reports setup errors without exposing command output or secrets.

The server issues work only after the agent acknowledges the current revision.
Edits require the latest revision and are rejected while a job is active or has
an unknown outcome. Pending jobs from the previous configuration are skipped.
An active job finishes with the profile it started with. Repository location and password can be corrected until the device first reports
readiness. After initialization, repository identity and password are fixed; server mount mappings can be
updated. The `managed` profile ID is reserved and cannot be modified through the
legacy policy API. Existing local profiles with that ID must be renamed before
opting into management.

For an existing backup-authorized device, download and install a fresh bundle
using **Device info → Reinstall agent** to install Restic and enable management,
then configure its managed profile in Backups. When migrating an existing local
profile, pause its old policy before enabling the managed schedule to avoid
duplicate backup runs. Reinstallation preserves the
existing capability ceiling; devices without backup authorization still require
enrollment with backup permission.

## Browse and download

When a server repository mapping is configured, **Browse snapshots** lists
snapshots and lets users navigate directories, download files, or download a
directory as ZIP. The device can be offline if its repository remains reachable
from the server. Repository contents are read on demand using the pinned Restic
client. A full snapshot ID is required; paths must be canonical absolute paths
inside that snapshot. Downloads do not restore over files on either machine.

Downloads are staged to private temporary files before HTTP success is sent,
limited to 2 GiB, and removed after delivery. At most two repository browsing or
download operations run concurrently per API process. Allow sufficient temporary
disk space. Metadata/listing output remains bounded. Authentication and
`backups.read` permission are required for browsing and downloading; editing
requires `hosts.manage`. Responses containing backup configuration or files use
private, no-store caching.

## API

- `GET /api/backups/devices/{deviceID}/managed`: settings without a password and
  desired/applied revision status.
- `PUT /api/backups/devices/{deviceID}/managed`: create or update settings, using
  `expectedRevision` (`0` for creation).
- `GET /api/backups/devices/{deviceID}/managed/recovery`: download recovery
  locations, password, and pinned Restic version.
- `GET /api/backups/devices/{deviceID}/snapshots`: list snapshots.
- `GET /api/backups/devices/{deviceID}/snapshots/{snapshotID}/entries?path=/`:
  list one directory.
- `GET /api/backups/devices/{deviceID}/snapshots/{snapshotID}/download?path=/`:
  download a file or directory ZIP.

Managed configuration is an additive, opt-in agent protocol extension. Old
agents and local profiles remain supported. Jobs contain only profile IDs and
permissions; secrets never enter immutable job snapshots. Exclusions, automatic
retention/pruning, remote Restic backends, agent-mediated browsing of inaccessible
local disks, and remote in-place restore remain outside this implementation.

## Verification

After packaging, run:

```sh
python scripts/test_managed_backups.py ./agent-releases
```

This verifies manifest digests and exercises automatic initialization, backup,
directory browsing, file downloads, ZIP downloads, and failure boundaries against
real Restic using disposable data. Database integration tests use
`VIGIRA_TEST_DATABASE_URL`; setting `VIGIRA_RESTIC_EXECUTABLE` additionally enables
the authenticated HTTP browser/download integration test.
