# Git-backed stacks: setup and operation

The web UI has a **Compose stacks** section for administrators. **Keep draft** stores an editable draft in Vigara. **Save to branch** validates the stack on its assigned device, commits the raw files, and pushes to the configured Git branch. **Preview deployment** and **Deploy revision** apply an already-pushed commit. Saving never starts containers. Deployment jobs continue if the browser closes.

## Web management

The Compose stacks page provides Cards and Table views, search across stack/project/device/status, a device filter, sorting, and 20 stacks per page. Container counts come from the live Docker inventory and are matched by agent plus Compose project; offline or failed inventory is shown as unavailable, and truncated results are labeled as a lower bound. Deployment status reflects the latest recorded Vigara attempt, not an assertion about outside changes.

**Git settings** displays the repository URL and branch and can test or save them. Settings are stored in PostgreSQL and override the environment defaults without restarting the app. The connection test reads the selected branch; write access is checked on stack Save. Authentication continues to use service-account SSH keys or Git credential helpers. The browser does not accept embedded passwords/tokens or arbitrary local filesystem repositories; the existing environment-configured local test repository can be retained. Finish/discard stored drafts and resolve active or uncertain deployments before switching repository or branch. Switching sources does not migrate stacks or detach their device bindings.

**New stack** creates a named draft on an enrolled device with Docker deployment access. The updated agent provisions a new folder under its locally configured managed root (VIGIRA_COMPOSE_MANAGED_ROOT, default /var/lib/vigira-agent/compose-stacks). The root must be writable by the agent and allowed by its service sandbox. Existing folders, Compose project names and bindings cannot be adopted through this flow. Target markers persist across agent restarts. Creating a stack neither commits nor starts containers; edit the generated files, Save, then Preview and Deploy. Existing approved targets remain available from the same dialog.

## Repository setup

Use a dedicated repository with an existing branch and at least one initial commit. Configure these variables on the API server:

```text
VIGIRA_STACK_GIT_URL=ssh://git@example.com/home/compose-stacks.git
VIGIRA_STACK_GIT_BRANCH=main
VIGIRA_STACK_GIT_DIR=/var/lib/vigira/stacks.git
VIGIRA_STACK_TARGETS_FILE=/etc/vigira/stack-targets.json
VIGIRA_SOPS_BINARY=/usr/bin/sops
```

Git authenticates as the Vigara service account, using its normal SSH configuration or Git credential helper. Credentials and private keys are configured outside the browser. Configure known_hosts in advance; interactive prompts are disabled. Grant write access to the selected branch, without force-push or delete rights. A branch requiring pull requests cannot use this direct-save workflow.

The API image includes Git, SSH and SOPS. Persist `/var/lib/vigira/stacks.git` on a named volume or bind mount. The container runs as UID/GID 10001 and sets HOME to `/var/lib/vigira`; mount its protected SSH/credential configuration there, with appropriate ownership. The existing Android backup volume does not persist the Git directory. A minimal additional Compose mount is:

```yaml
services:
  app:
    volumes:
      - stack-git:/var/lib/vigira/stacks.git
volumes:
  stack-git:
```

The server target file binds stack IDs to enrolled agents and stable Compose project names:

```json
{
  "media": {
    "agentId": "00000000-0000-0000-0000-000000000001",
    "projectName": "media"
  }
}
```

Replace the example UUID with the actual enrolled agent ID. The first validation records the binding in PostgreSQL. Changing the target file cannot move a recorded stack to another agent or project; automatic ownership transfer is not implemented.

## Device setup

The first runner supports Linux. The agent needs the Docker CLI and a modern Compose plugin supporting `config --format json` and `up --wait`. Both the agent's local configuration and enrollment scope must grant `docker.deploy`:

```text
VIGIRA_DOCKER_ENABLED=true
VIGIRA_DOCKER_MODE=deploy
VIGIRA_DOCKER_HOST=unix:///var/run/docker.sock
VIGIRA_COMPOSE_TARGETS_FILE=/etc/vigira/compose-targets.json
VIGIRA_SOPS_BINARY=/usr/bin/sops
VIGIRA_COMPOSE_AGE_IDENTITY=/etc/vigira/keys/target-age.txt
```

Example device target file:

```json
{
  "media": {
    "projectName": "media",
    "directory": "/srv/compose/media"
  }
}
```

The directory must already exist, be writable by the agent, and not resolve through symlinks. For an existing stack, use its actual Compose project name and original project directory. This preserves relative bind paths and named-volume identities. Check the existing runtime project before enabling deployment. Docker access is highly privileged; these local target files are controlled by the device operator, not by Git.

## Stack layout and editing

```text
stacks/media/
  vigara-stack.yaml
  compose.yaml
  compose.override.yaml       # optional, explicitly listed
  config/app.yaml             # optional non-secret files
  secrets/runtime.env.sops    # optional encrypted interpolation input
```

Minimal metadata:

```yaml
apiVersion: vigara.io/v1
kind: Stack
stackId: media
displayName: Media server
projectName: media
compose:
  files: [compose.yaml]
  projectDirectory: .
  interpolationFiles: []
configurationRevisionPolicy: sha256-git-tree-and-placement-inputs-v1
```

Create an approved stack in the UI, or place reviewed files under this layout in Git. Multiple Compose files are passed in the listed order. File contents, including comments, are committed as written. Only regular UTF-8 files are supported: at most 64 files, 128 KiB per file and 384 KiB total per stack. Symlinks, executable files, submodules, unsafe paths, duplicate paths differing only by case, and plaintext `.env` files are refused. Inline secrets in YAML/configuration remain the administrator's responsibility; move them to encrypted interpolation inputs before importing. Repository and deployment access is administrator-only because source files may contain sensitive values.

Existing discovery/import preparation remains separate; there is no automatic adoption wizard or inferred ownership transfer in this release.

## Encrypted environment inputs

Configure three independent age recipient classes in the server target binding. Use actual public recipients and independently backed-up identities; these are illustrative references:

```json
"recipients": [
  {"class":"central-editor","keyRef":"editor-main","recipient":"<editor public age recipient>"},
  {"class":"target-agent","keyRef":"media-agent","recipient":"<target public age recipient>"},
  {"class":"operator-recovery","keyRef":"offline-recovery","recipient":"<recovery public age recipient>"}
]
```

The **Environment** tab accepts complete replacement dotenv contents. SOPS encrypts them in memory before returning ciphertext; the UI updates the interpolation descriptor and recipient references in `vigara-stack.yaml`. Use `${VARIABLE}` in Compose to consume these values. Existing secret values are never returned to this editor. The target decrypts them with its own protected age identity into a private temporary workspace, removed when Compose exits. The central editor and recovery private identities are provisioned and protected independently; this first UI does not reveal or decrypt existing values. See [the secret trust and recovery contract](compose-management.md#secret-editor-trust-and-recovery).

Drafts and job snapshots contain non-secret configuration and SOPS ciphertext. Protect PostgreSQL storage and backups with encryption at rest. Do not paste secrets into ordinary file fields. Raw Compose, Git and SOPS diagnostics are suppressed rather than stored in jobs or audit entries.

## Conflicts, saved versions and deployments

- Every Save checks the draft's base against the latest remote stack files. Unrelated changes to other stacks are retained. A conflicting edit blocks the save; no remote history is force-pushed.
- **Fetch saved version** preserves the editor contents and makes the remote version available for comparison. Resolve the differences before explicitly using that version as the draft base.
- A failed or unconfirmed push is shown as **Pending sync**. The draft is retained. Retry Save after Git is available. The unconfirmed commit is not deployable.
- Restoring a historical revision loads its files into the draft. Saving creates a new forward commit, preserving history.
- Deployment captures the selected commit and files in a durable database job. Later saves cannot change that job. Only ancestors of the configured remote branch are selectable; a remote history rewrite may make older commits unavailable for new deployments.
- The deployment preview validates Compose and existing bind paths on the device. A fingerprint detects local configuration changes between preview and execution. Each stack has at most one queued, running or uncertain job. Deployment requests are journaled on the device and completed requests are not executed again.
- The runner preserves the project directory, writes the selected files, optionally pulls images, then runs `compose up --detach --no-build --wait`. It never runs `down`, removes volumes, or removes orphaned containers. Runtime reconciliation is Compose's normal non-transactional behavior. Removed Git files are intentionally left on the device.
- Current service progress is displayed without raw command output. Success means Compose confirmed services running or healthy; services without health checks are only confirmed running.
- **Last deployed** is the last successful deployment recorded by Vigara, not a claim that no one has changed Docker outside Vigara. The stack file digest is distinct from the future complete configuration revision described in the broader contract: it does not include profile/assignment manifests.

## Interrupted deployments and current boundaries

The browser can reconnect to deployment history. A server restart or lost channel result marks an executing job **unknown**, rather than retrying it. **Check device result** reads the device's durable journal. A final success/failure releases the server-side block. If the device has no final result, the stack stays blocked for operator inspection; do not start another rollout or delete its `.vigara-jobs/active` lock until the old operation is proven stopped. There is no automatic recovery or ownership-transfer override in the UI.

This first runner supports image-based Compose services, ordinary networks/volumes and encrypted interpolation files. It rejects builds, includes, extends, lifecycle hooks, service `env_file`, file-based configs/secrets, and other service file inputs before Compose can read them. Existing relative bind storage must already exist. Parent-relative and interpolated storage sources are rejected; use explicit absolute paths or paths inside the approved project directory. Private registry access uses the device service account's Docker configuration. Full server-profile preflight, assignment manifests, replicated placements, image-digest release records, automatic deployment on Git push, and transactional rollback are not implemented. The broader [Compose management contract](compose-management.md) describes those subsequent capabilities.

Configuration history does not reverse application migrations or restore application data. Backups are separate.

## Verification

Run the Go suite with an isolated PostgreSQL database configured through `VIGIRA_TEST_DATABASE_URL`, plus the web build, lint and tests. Git integration tests create local disposable repositories and cover saving, unrelated updates, conflicts, history, restoration and rejected pushes. Linux runner tests cover input boundaries, storage-path validation, stale previews and idempotent jobs. Live deployments require the device setup above and are deliberately separate from unit tests.

### Compose editor

Compose files use a bundled Monaco editor. Tab inserts two spaces, Shift+Tab unindents, and Ctrl/Cmd+S uses Save to Git without deploying. Use Compare with saved version to review or edit changes in the diff view, and Expand editor for more room. Ctrl+M (Ctrl+Shift+M on macOS) toggles whether Tab leaves the editor.

YAML syntax checks and the bundled Compose schema supply inline feedback and suggestions without internet access. Custom Compose paths listed in the stack manifest receive the same schema support. Other YAML files receive syntax checks only. Server-side validation remains authoritative for Vigara’s supported deployment features. Encrypted files remain hidden and are replaced through the Environment tab.
