# Starting deployed stacks without Vigara

After deployment with a recovery-capable agent, each stack's original server
directory contains an executable `start.sh`. With shell access to that Docker
host, run:

```sh
cd /home/deranjer/vigara-stacks/cookira
./start.sh
```

Use the actual stack directory on your host. Run as the account that deployed
the stack, or as root. The launcher starts the full stack and creates missing
containers using standard Docker Compose. It needs no running Vigara server or
agent, Git access, age identity, SOPS, or password extraction from containers.
It preserves the original project name and project directory, Compose file
order, interpolation input order, service environment files, and the reviewed
image-digest override. Relative storage paths and named-volume identities stay
the same. Images must already exist locally: recovery does not pull or build.
Shell variables and default `.env` files cannot override the saved inputs.
Compose may recreate containers whose configuration differs; application data
migrations are not rolled back. Existing host mounts, devices, networks and
external volumes must still be available.

Deployment retains decrypted interpolation inputs in `.vigara-recovery/` with
directory mode `0700` and file mode `0600`; the launcher is mode `0700` and
contains paths, not secret values. Service `env_file` inputs remain at their
declared original targets, also mode `0600`. These plaintext files are protected
local runtime state: protect host access and backups accordingly. Each generated
input bundle ignores its contents in Git. Git remains authoritative for the
encrypted source; runtime plaintext must not be committed or uploaded.
After a new launcher is published, older generated interpolation bundles are
removed. Temporary validation workspaces are still deleted on every outcome.

The launcher is published after on-host configuration validation and before
pull/apply, so it is also available for a failed deployment attempt. It describes
the latest prepared configuration, not an automatic rollback to the last healthy
release. Interrupted deployments may leave partially updated original files;
inspect the stack before recovery in that case. Import alone does not generate
recovery files. **Existing stacks need one explicit deployment with the updated
agent** to gain this recovery path. Upgrading the central server alone does not
backfill files. `start.sh` and `.vigara-recovery/` are reserved for generated
runtime files; deployment refuses to replace a user-authored `start.sh` or follow
symlinks at those locations. Rename an existing custom launcher before deploying.

# Compose offline recovery plan

`compose-recovery` builds a deterministic, metadata-only plan from a local
checked-out configuration Git repository. It reads the current commit with
`git rev-parse`, rejects tracked edits under the portable profile, assignment,
stack, and release inputs, and validates the saved manifests through the
existing placement and stack validators.

```powershell
go run ./cmd/compose-recovery --repo C:\recovery\vigara-config --profile dns-primary `
  --snapshot-file C:\recovery\nightly-snapshot.json `
  --release 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
```

The snapshot file is optional for profiles without declared persistent
resources. For a persistent mount, volume, or stack asset, select a snapshot
and supply a reference for every backup policy. A missing reference blocks plan
generation. `snapshotId` accepts a lowercase slug label or a full 64-character
lowercase hexadecimal snapshot ID. References are opaque identifiers made of
letters, digits, `.`, `_`, `:`, or `-`; do not put credentials or repository
URLs in them.

```json
{
  "snapshotId": "nightly-2026-10-25",
  "profileId": "dns-primary",
  "backupReferences": {
    "application-data": "restic:nightly-2026-10-25"
  },
  "recoveryKeyRefs": ["operator-recovery-main"]
}
```

The optional `--release` flag accepts a comma-separated list of selected
64-character release IDs. Each must match a committed
`releases/<profile>/<stack>/<releaseId>.yaml` record for a stack in the profile.
Output contains only the release ID, stack ID, Git commit, and outcome; image
references and container identifiers are omitted.

The JSON plan lists profile-declared networks, mounts, volumes, devices, and
startup dependencies. It orders assigned stacks deterministically according
to those dependency edges and lists Compose service names, assignment IDs,
project names, persistent paths, backup policy IDs, and opaque recovery-key
references. It never reads host mount paths. Encrypted `*.sops` blobs are not
read; validation uses an internal presence marker. Plain `.env` files and
manifest paths that resemble private-key material are rejected.

Stack `configurationAssets` are validated separately against their bounded
metadata shape for this offline plan, while the existing stack validator checks
the remaining stack fields. This does not add `configurationAssets` support to
the live deployment runner.

## Plan boundary

Every result is marked `NOT_EXECUTABLE` with `executable: false`. It contains
no shell commands, Compose commands, restic commands, restore destinations, or
deployment instructions. It does not validate live backups or decrypt secrets.
Before any real recovery, an operator must collect current replacement-host
evidence, validate backup integrity and a restore point, verify independent
offline recovery-identity access, prove the prior owner is fenced and advance
ownership safely, then enroll and bind a replacement agent. These prerequisites
remain marked `required` in the plan; the tool cannot mark them complete.

The plan includes every assignment declared for the selected profile, even if
an assignment is currently disabled. Review the assignment set and the
committed revision before relying on the output. The tool only reads the local
repository and does not fetch, commit, stage, deploy, restore, reboot, or alter
configuration.
