# Server profiles and assignment manifests

Administrators can open **Compose stacks → Profiles and assignments** to prepare logical server profiles and placements in the configured stack repository. This release supports manifest authoring, validation and Git persistence. Profile-to-agent binding, assignment-aware deployment, ownership transfer and automatic storage provisioning are not implemented. The editor requires `enabled: false` on assignments. Existing direct stack-to-device bindings continue to run independently of these disabled manifests.

## Repository structure

```text
stacks/adguard-primary/
  vigara-stack.yaml
  compose.yaml
profiles/dns-primary/
  vigara-profile.yaml
assignments/dns-primary/
  adguard-primary.yaml
```

Stack definitions retain their existing `stacks/<stack-id>/` paths. A profile describes a logical server role, not an enrolled physical machine. Agent IDs, credentials and private keys do not belong in these manifests. Two independently configured AdGuard installations should have distinct stack IDs and assignments; both may keep the display name AdGuard and Compose project name `adguard` when placed on different hosts.

## Prepare a profile

Use **Add a server profile**, choose an immutable lowercase slug and a display name, and optionally enter the expected data-disk mountpoint. For example:

```yaml
apiVersion: vigara.io/v1
kind: ServerProfile
profileId: dns-primary
displayName: Primary DNS server
requirements:
  mounts:
    - hostPath: /mnt/appdata
      required: true
      purpose: Application data disk
      persistent: true
      requireMountedFilesystem: true
  networks: []
  volumes: []
  startupDependencies: []
```

Use the YAML editor for additional requirements supported by the [profile schema](https://github.com/deranjer/vigara/blob/main/schemas/compose-management/server-profile.schema.json), including volumes, devices, capabilities and startup dependencies. A declared mount is an expectation; creating a directory does not satisfy the exact filesystem mount requirement. The [preflight helper](compose-preflight.md) implements this distinction, but the profile editor does not collect readiness evidence or enforce these requirements on existing direct deployments. A `backupPolicyId` is a reference to an independently configured backup policy, not a command to create or run a backup.

## Prepare an assignment

Select a profile and an already-saved stack, then choose a stable assignment ID:

```yaml
apiVersion: vigara.io/v1
kind: DeploymentAssignment
assignmentId: adguard-primary-dns-primary
stackId: adguard-primary
profileId: dns-primary
environment: production
ownership:
  mode: exclusive
  ownershipEpoch: 1
enabled: false
```

An assignment's path and IDs must agree. All assignments, including disabled ones, must reference existing profiles and valid saved stack definitions. Assignment interpolation paths resolve inside the assigned stack directory and must reference existing files. They must have purpose `deployment`, cannot duplicate stack input paths or Compose files, and encrypted inputs require the independent editor, target and recovery recipient references. The editor does not create or decrypt assignment secrets.

The validator also understands the future enabled-placement contract: exclusive ownership cannot have concurrent active placements; stateless replicas require distinct replica keys, profiles and effective project names. Active project names cannot collide within a profile. Startup dependencies must reference stacks assigned to that profile and cannot form cycles. Declaring stateless ownership does not verify that an application is stateless.

## Save and review

**Save to branch** validates and commits the complete profile/assignment set atomically, preserving YAML comments and unrelated repository files. Removing a manifest from the draft removes it from Git on save; remove referencing assignments as well when removing a profile. Git history retains the previous files. Manifest deletion does not erase application storage or remove runtime bindings.

The editor holds unsaved changes in browser memory. Closing it with changes prompts before discarding them; there is no server-side manifest draft recovery. A concurrent change to the saved manifest set returns a conflict and preserves the editor contents. Copy any changes you need to retain before explicitly reloading the saved manifests. Unrelated stack changes are preserved, and references are revalidated against the latest stack definitions during save.

A push is saved only after the remote confirms it. **Pending sync** retains the local editor contents; keep it open and retry Save. Saving neither deploys nor moves containers. Active or uncertain deployment jobs must be resolved before saving manifests. Limits are 256 manifests, 64 KiB per file and 512 KiB total, also subject to the API's 768 KiB encoded request limit. Only regular non-executable UTF-8 files are accepted. YAML aliases, merge keys, multiple documents, unknown schema fields and unsafe paths are rejected. Non-YAML files in these directories are preserved.

An enabled assignment introduced outside the editor blocks legacy preview, deployment submission and queued-job dispatch for that stack. Selecting an older stack revision does not bypass this check. Malformed manifest trees also block the direct deployment path. This prevents silently deploying while ignoring profile requirements or assignment ownership. Disable the assignment explicitly while preparing it; activation will require a later implementation of agent bindings and deployment fencing.

See the [broader Compose contract](compose-management.md) for the intended runtime binding, storage readiness, ownership epoch and recovery semantics.
