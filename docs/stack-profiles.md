# Server profiles and assignment manifests

Administrators can open **Compose stacks → Profiles and assignments** to author and save profiles, bind a saved profile to its existing approved device, and activate exclusive assignments at ownership epoch 1. Deployments capture the exact profile and assignment manifests with the stack. Ownership transfer, replicas and automatic resource provisioning remain unavailable. Disabled assignments are preparation only; the existing direct deployment path continues independently of them.

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

The schema supports future capabilities and startup dependencies as well as mounts, devices, networks and volumes. The first runtime checks pre-existing external networks/volumes, required device nodes, and absolute symlink-safe paths. An exact mount check uses the agent's mount namespace; a directory alone is insufficient. The agent must see the storage used by Docker. Managed resource creation, nonempty capability requirements, startup dependencies and nonlocal Docker endpoints are refused until implemented. A `backupPolicyId` is only a reference to an independently configured policy; it does not execute a backup or establish restore readiness.

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

Save the profile and disabled assignments first. Select the profile and the device already approved for all its stacks, then **Bind profile**. Binding requires an online agent with deployment access and is immutable. Enable each supported assignment with `enabled: true`, leaving `ownership.mode: exclusive` and `ownershipEpoch: 1`, and save again. Preview each stack before deploying. The agent repeats resource checks before apply. No profile collection deployment or ownership transfer is implied. Devices owning a profile cannot be deleted through ordinary device deletion.

**Save to branch** validates and commits the complete profile/assignment set atomically, preserving YAML comments and unrelated repository files. Removing a manifest from the draft removes it from Git on save; remove referencing assignments as well when removing a profile. Git history retains the previous files. Manifest deletion does not erase application storage or remove runtime bindings.

The editor holds unsaved changes in browser memory. Closing it with changes prompts before discarding them; there is no server-side manifest draft recovery. A concurrent change to the saved manifest set returns a conflict and preserves the editor contents. Copy any changes you need to retain before explicitly reloading the saved manifests. Unrelated stack changes are preserved, and references are revalidated against the latest stack definitions during save.

A push is saved only after the remote confirms it. **Pending sync** retains the local editor contents; keep it open and retry Save. Saving neither deploys nor moves containers. Active or uncertain deployment jobs must be resolved before saving manifests. Limits are 256 manifests, 64 KiB per file and 512 KiB total, also subject to the API's 768 KiB encoded request limit. Only regular non-executable UTF-8 files are accepted. YAML aliases, merge keys, multiple documents, unknown schema fields and unsafe paths are rejected. Non-YAML files in these directories are preserved.

An enabled assignment requires a matching runtime binding and exact manifests from the selected saved commit. Selecting an older stack revision cannot bypass newer requirements. Changed or malformed current manifests block queued jobs before dispatch. Profile and assignment bytes are included in the preview token and configuration revision. Explicitly disabling an assignment returns that stack to its existing direct binding; disabled manifests do not enforce profile requirements. Deleting Git manifests does not remove the immutable runtime profile binding or transfer ownership.

See the [broader Compose contract](compose-management.md) for the intended runtime binding, storage readiness, ownership epoch and recovery semantics.
