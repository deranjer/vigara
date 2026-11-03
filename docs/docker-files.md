# Browse and edit mounted files

Administrators can open **Containers → container → Overview → Mounts → Browse files**, or **Volumes → volume → Browse files**. The browser supports folders, text previews and downloads of the saved text. **Edit text → Review changes → Save file** shows a comparison before writing. Caddyfiles, YAML, JSON, shell and INI files have syntax coloring; syntax coloring does not validate service configuration.

## Enable access on a host

This feature requires the agent installed directly on the native Linux Docker host, access to its local Unix socket, and **exec** or **deploy** Docker access granted in the device enrollment and enabled on the agent. An administrator account is required because mounted configuration can contain credentials. Existing container viewing permissions alone do not grant file access.

Add one of these settings to the agent service's environment and restart the service:

```ini
# Browse and download text; editing disabled.
VIGIRA_DOCKER_FILES=read

# Or allow text edits with a backup before every save.
# VIGIRA_DOCKER_FILES=write
```

The variable is unset by default, which disables all mount file methods. It does not change the existing Docker management tier. Agent Docker configuration still needs `VIGIRA_DOCKER_ENABLED=true`, `VIGIRA_DOCKER_HOST=unix:///var/run/docker.sock` (or the actual local socket) and `VIGIRA_DOCKER_MODE=exec` or `deploy`. Keep these settings on the host service, rather than the central Vigara server. Reinstallation may replace the service environment; preserve custom settings when reinstalling.

## Supported storage

- Bind-mounted directories and individual regular files.
- Ordinary Docker **local** named or anonymous volumes, including unused local volumes. The native agent accesses the Docker-inspected local mountpoint using its operating-system permissions. It does not create helper containers or pull images.
- UTF-8 text files up to **64 KiB**, including empty files. Each directory listing returns at most **500** entries and indicates when truncated.

Docker Desktop, Windows hosts, remote daemons, volume plugins, local volumes with driver options (such as NFS), volume subpaths, symlinks and special files are unsupported. The agent resolves the selected container mount or volume through Docker on every request. Clients cannot supply host source paths; filesystem operations stay within that storage using `os.Root`.

File editing is a separate host permission. A mount marked read-only **inside the container** may still be edited by an administrator through the native host agent when `VIGIRA_DOCKER_FILES=write` is enabled and the host filesystem permits writes.

## Saving and recovery

Saving requires the version returned when opening the file. If its contents, identity, ownership, modification time or mode changed in the meantime, saving is refused and the draft remains available. Reopen the saved file and reconcile the changes. Saves through Vigara are serialized on the agent. External programs can still write during a save; coordinate edits with other operators and applications.

The agent writes through the existing file descriptor and truncates to the new length, preserving its inode, ownership and ordinary permissions. This keeps single-file bind mounts attached to the updated file. The write is not an atomic file replacement: a simultaneous reader can observe a partially written configuration, and interrupted writes can need recovery. Services that require a controlled reload should be reloaded after a successful save.

Before writing, the agent saves the exact previous bytes outside application storage. It keeps **one previous version per source and relative path** in `/var/lib/vigira-agent/mount-file-backups`. Set `VIGIRA_DOCKER_FILES_BACKUP_DIR` to an absolute private directory to override this location. The directory must be accessible only to its owner (mode `0700`), and backup files use mode `0600`. A backup failure leaves the original untouched. Backups are plaintext and should remain in this protected host directory; they are not sent to Git or the central server.

Backup filenames are the lowercase SHA-256 of the exact Docker-inspected source path, a NUL byte, and the mount-relative file path, followed by `.bak`. For a single-file bind, the browser sends its basename as that relative path. Recovery is a host operation: identify the backup from that source/path, review it and copy its bytes back to the original file in place. There is no restore button in this first version. The agent attempts to restore the previous bytes after a failed write, while retaining the backup if the filesystem refuses restoration.

File requests are authenticated, checked for same-origin access, and audited with the operator, operation, Docker target and outcome. File contents, filenames, version tokens and backup paths are excluded from audit entries. Browser responses use `Cache-Control: no-store`.

## Caddy configuration

For example, bind-mount a configuration directory into Caddy, browse that mount, open `Caddyfile` and edit a manual `reverse_proxy` route. With caddy-docker-proxy, point `CADDY_DOCKER_CADDYFILE_PATH` at the base Caddyfile so manual routes can coexist with routes generated from labels. Edit the base input rather than the generated autosave. See the [Caddy Docker Proxy options](https://github.com/lucaslorentz/caddy-docker-proxy#options).

Saving changes the file on the deployed host. Validate and reload Caddy using your normal workflow when ready; this feature does not automatically validate, restart or reload services. If the configuration is supplied by a Git-backed deployment, update that source as well so a later deployment preserves the change.
