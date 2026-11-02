# Git-backed stacks: setup and operation

The web UI has a **Compose stacks** section for administrators. **Keep draft** stores an editable draft in Vigara. **Save to branch** validates the stack on its assigned device, commits the raw files, and pushes to the configured Git branch. **Preview deployment** and **Deploy revision** apply an already-pushed commit. Saving never starts containers. Deployment jobs continue if the browser closes.

## Web management

The Compose stacks page provides Cards and Table views, search across stack/project/device/status, a device filter, sorting, and 20 stacks per page. Container counts come from the live Docker inventory and are matched by agent plus Compose project; offline or failed inventory is shown as unavailable, and truncated results are labeled as a lower bound. Deployment status reflects the latest recorded Vigara attempt, not an assertion about outside changes.

**Git settings** displays the repository URL, branch and authentication method and can test or save them. Settings are stored in PostgreSQL and override the environment defaults without restarting the app. Choose native **GitHub App** authentication for GitHub, or **Server credentials** for service-account SSH keys and Git credential helpers. The connection test reads the selected branch; branch write access is checked on stack Save. The browser does not accept embedded passwords/tokens or arbitrary local filesystem repositories; the existing environment-configured local test repository can be retained. Finish/discard stored drafts and resolve active or uncertain deployments before switching repository or branch. Switching sources does not migrate stacks or detach their device bindings.

**New stack** creates a named draft on an enrolled device with Docker deployment access. The updated agent provisions a new folder under its locally configured managed root (VIGIRA_COMPOSE_MANAGED_ROOT, default /var/lib/vigira-agent/compose-stacks). The root must be writable by the agent and allowed by its service sandbox. Existing folders, Compose project names and bindings cannot be adopted through this flow. Target markers persist across agent restarts. Creating a stack neither commits nor starts containers; edit the generated files, Save, then Preview and Deploy. Existing approved targets remain available from the same dialog.

## Repository setup

### Native GitHub App registration

The Linux installer (`bash deploy/setup.sh`) generates a separate GitHub App
encryption key automatically and saves it in the private deployment `.env`.
It uses the HTTPS address entered during setup (or the Caddy hostname) for App
callbacks, forwards the key into the API container, and persists the Git cache
with the chosen named-volume or bind storage. After startup, follow the printed
**Compose stacks → Git settings → GitHub App → Connect with GitHub** instructions.
If setup has no HTTPS address yet, set `VIGIRA_PUBLIC_URL` in the existing `.env`
before connecting. Preserve this file and its keys during upgrades; rerunning the
initial installer is not an upgrade path.

Configure these settings once on the API server:

```text
VIGIRA_PUBLIC_URL=https://vigara.example.com
VIGIRA_GITHUB_APP_MASTER_KEY=<base64-encoded random 32-byte key>
```

Generate the master key with `openssl rand -base64 32`. Keep it in your server's secret configuration, use the same key on every API replica, and back it up separately from PostgreSQL. Keep it unchanged across restarts. The App's RSA private key is encrypted with AES-256-GCM before it is stored in PostgreSQL, including during unfinished registration. Losing the master key requires reconnecting an App; replacing it does not re-encrypt existing credentials. This key is separate from the managed-backup master key. GitHub App setup is disabled when it is unset; server-credential authentication continues to work.

Sign in as a Vigara administrator at the configured HTTPS origin. In **Compose stacks → Git settings**:

1. Enter the HTTPS GitHub repository URL, for example `https://github.com/deranjer/vigara-derajnet.git`, and an existing branch such as `main`.
2. Select **GitHub App**. For an organization-owned App, enter its organization name; otherwise leave the owner field empty.
3. Click **Connect with GitHub**. GitHub asks you to create the private App from Vigara's manifest. It requests repository **Contents: read and write**, with implicit Metadata read permission; it does not request workflow, administration or pull-request permissions. Webhooks and user OAuth authorization are disabled.
4. Install the App on the owning account and choose **Only select repositories**, selecting the stack repository. A private App is installable only on its owning account, so create it under the account that owns your repository.
5. GitHub returns you to Vigara. Test the connection, then **Save settings**. Completing App installation alone does not change the active repository, branch or authentication method.

Registration callbacks are bound to the administrator and initiating login session, expire after one hour, and cannot be replayed. Use the same browser and remain signed in throughout. A same-site continuation page preserves Vigara's strict login-cookie policy when returning from GitHub; if scripts are disabled, click **Continue GitHub setup**. **Resume App installation** continues an interrupted installation during this window. Starting over may leave an unused App on GitHub; remove abandoned Apps in GitHub's developer settings. An installation callback is verified against the expected repository using the registered App's signed JWT rather than trusting the browser's installation ID.

Vigara obtains repository-scoped installation tokens with Contents write permission, keeps them only in memory, and renews them before their one-hour expiry. Git uses these tokens through per-process environment configuration, with redirects disabled. Tokens and private keys are never put into repository URLs, process arguments, Git repository configuration, browser API responses, or audit records. Existing SSH/credential-helper setups remain available through **Server credentials**.

Saves push directly to the selected branch. Branch rules that require pull requests or other checks before a direct push are unsupported; a successful connection test does not prove that branch rules permit a Save. Deployment remains a separate action.

**Disconnect App** deletes Vigara's stored App credentials and switches authentication to server credentials while preserving the repository and branch. Uninstall or delete the App in GitHub separately to revoke its GitHub access. Use the GitHub repository-access link to change which repositories an existing App installation can access. This integration currently supports GitHub.com; other Git hosts use server credentials.

References: [GitHub App manifest flow](https://docs.github.com/en/apps/sharing-github-apps/registering-a-github-app-from-a-manifest), [installation authentication](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/authenticating-as-a-github-app-installation).

### Server credentials and repository storage

Administrators can prepare logical server profiles and assignment manifests through **Profiles and assignments**, save them to Git, and explicitly bind a saved profile to an enrolled agent. Initial enabled exclusive assignments at ownership epoch 1 are supported. See [server profiles and assignment manifests](stack-profiles.md). Ownership transfer, replicated placements and subsequent epoch changes remain separate work.

Use a dedicated repository with an existing branch and at least one initial commit. Configure these variables on the API server:

```text
VIGIRA_STACK_GIT_URL=ssh://git@example.com/home/compose-stacks.git
VIGIRA_STACK_GIT_BRANCH=main
VIGIRA_STACK_GIT_DIR=/var/lib/vigira/stacks.git
VIGIRA_STACK_TARGETS_FILE=/etc/vigira/stack-targets.json
VIGIRA_SOPS_BINARY=/usr/bin/sops
VIGIRA_COMPOSE_EDITOR_AGE_IDENTITY=/etc/vigira/keys/editor/editor.txt
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

The **Environment** tab masks values by default. **Reveal and edit** first stores the current ciphertext draft, then explicitly decrypts its declared encrypted input with the central editor identity. The API requires administrator deployment permission and successfully records a metadata-only audit event before returning plaintext. It selects the exact user-owned draft version and file digest (or an immutable Git revision through the API); the browser cannot submit arbitrary ciphertext or private-key paths for decryption. This operation uses the API server and works while the target device is offline. Configure `VIGIRA_COMPOSE_EDITOR_AGE_IDENTITY` as an absolute path to the operator-provisioned editor identity: a service-owned `0600` file in a service-owned `0700` directory on Linux. Mount it separately from Git, PostgreSQL and recovery media. A missing or incorrect key fails without exposing SOPS diagnostics. Secret file operations fail closed on Windows until service-account ACL support exists.

**Enter replacement** accepts complete replacement dotenv contents without revealing existing values. **Encrypt into draft** encrypts in memory to the configured editor, target and recovery recipient policy, checks the exact source draft again, and returns ciphertext plus the updated interpolation descriptor and recipient references in `vigara-stack.yaml`. It clears the plaintext form as encryption starts, including on failure. Save the draft to commit the encrypted result. Secret requests and responses use `no-store`; plaintext is held only in the active form, outside persisted drafts, browser storage, the source editor and query caches. Hide, tab/stack changes, other stack operations, and page visibility changes clear the form and invalidate pending reveal responses. Browser and server runtimes may retain old string allocations until garbage collection; clearing the form is not a secure memory-erasure guarantee. Use `${VARIABLE}` in Compose to consume the values. Dotenv editing is limited to 64 KiB.

The target independently decrypts with its own protected age identity into a private temporary workspace, removed when Compose exits. Central editor, target and recovery identities are provisioned, backed up and rotated by the operator; the UI does not create, escrow, distribute or automatically rotate keys. To rotate recipients, update the operator-managed recipient binding, explicitly reveal/edit and encrypt each affected input into a new saved commit, then verify decryption using each intended identity before retiring old recipients. Keep old keys available under operator custody for historical revisions that still need them. A new central key cannot reveal old ciphertext unless it was already a recipient; use the prior editor or recovery identity in an operator-controlled recovery procedure. Removing a recipient does not revoke old Git history or plaintext copies. See [the secret trust and recovery contract](compose-management.md#secret-editor-trust-and-recovery).

Drafts and job snapshots contain non-secret configuration and SOPS ciphertext. Protect PostgreSQL storage and backups with encryption at rest. Do not paste secrets into ordinary file fields. Raw Compose, Git and SOPS diagnostics are suppressed rather than stored in jobs or audit entries.

## Conflicts, saved versions and deployments

- Every Save checks the draft's base against the latest remote stack files. Unrelated changes to other stacks are retained. A conflicting edit blocks the save; no remote history is force-pushed.
- **Fetch saved version** preserves the editor contents and makes the remote version available for comparison. Resolve the differences before explicitly using that version as the draft base.
- A failed or unconfirmed push is shown as **Pending sync**. The draft is retained. Retry Save after Git is available. The unconfirmed commit is not deployable.
- Restoring a historical revision loads its files into the draft. Saving creates a new forward commit, preserving history.
- Deployment captures the selected commit and files in a durable database job. Later saves cannot change that job. Only ancestors of the configured remote branch are selectable; a remote history rewrite may make older commits unavailable for new deployments.
- The deployment preview checks the saved profile and assignment, Compose configuration, required mounts and devices, existing bind paths, and preexisting external networks and volumes on the device. It resolves each service image to an immutable digest and shows the resulting image references for review. The API stores the exact image map with the preview for 15 minutes; deploy must submit that same map and uses it without resolving newer tags. A fingerprint detects local configuration changes between preview and execution. Each stack has at most one queued, running or uncertain job. Deployment requests and their image digest map, configuration revision and readiness result are journaled on the device; completed requests are not executed again.
- The runner preserves the project directory, writes the selected files, optionally pulls images, then runs `compose up --detach --no-build --wait`. It never runs `down`, removes volumes, or removes orphaned containers. Runtime reconciliation is Compose's normal non-transactional behavior. Removed Git files are intentionally left on the device.
- Current service progress is displayed without raw command output. The `compose-wait-passed` readiness result means Compose confirmed services running or healthy according to each service's health check; services without health checks are only confirmed running. It does not verify application data or restore correctness.
- **Last deployed** is the last successful deployment recorded by Vigara, not a claim that no one has changed Docker outside Vigara. Deployment records retain the normative configuration revision covering the captured stack and placement inputs, plus the reviewed image digest map. The separate stack-file digest used for editor comparisons covers stack files only.

## Interrupted deployments and current boundaries

The browser can reconnect to deployment history. A server restart or lost channel result marks an executing job **unknown**, rather than retrying it. **Check device result** reads the device's durable journal. A final success/failure releases the server-side block. If the device has no final result, the stack stays blocked for operator inspection; do not start another rollout or delete its `.vigara-jobs/active` lock until the old operation is proven stopped. There is no automatic recovery or ownership-transfer override in the UI.

This runner supports image-based Compose services, ordinary networks/volumes, encrypted interpolation files, saved profile bindings and initial exclusive assignments at epoch 1. Profile preflight checks required mounts/devices and preexisting external networks/volumes before preview and apply. It does not create managed resources, execute capability requirements or startup dependency ordering, transfer ownership, or support replicas. It rejects builds, includes, extends, lifecycle hooks, service `env_file`, file-based configs/secrets, and other service file inputs before Compose can read them. Existing relative bind storage must already exist. Parent-relative and interpolated storage sources are rejected; use explicit absolute paths or paths inside the approved project directory. Private registry access uses the device service account's Docker configuration. Automatic deployment on Git push and transactional rollback are not implemented. The broader [Compose management contract](compose-management.md) describes subsequent capabilities.

Configuration history does not reverse application migrations or restore application data. Backups are separate.

## Verification

Run the Go suite with an isolated PostgreSQL database configured through `VIGIRA_TEST_DATABASE_URL`, plus the web build, lint and tests. Git integration tests create local disposable repositories and cover saving, unrelated updates, conflicts, history, restoration and rejected pushes. Linux runner tests cover input boundaries, storage-path validation, stale previews and idempotent jobs. Live deployments require the device setup above and are deliberately separate from unit tests.

### Compose editor

Compose files use a bundled Monaco editor. Tab inserts two spaces, Shift+Tab unindents, and Ctrl/Cmd+S uses Save to Git without deploying. Use Compare with saved version to review or edit changes in the diff view, and Expand editor for more room. Ctrl+M (Ctrl+Shift+M on macOS) toggles whether Tab leaves the editor.

YAML syntax checks and the bundled Compose schema supply inline feedback and suggestions without internet access. Custom Compose paths listed in the stack manifest receive the same schema support. Other YAML files receive syntax checks only. Server-side validation remains authoritative for Vigara’s supported deployment features. Encrypted files remain hidden and are replaced through the Environment tab.
