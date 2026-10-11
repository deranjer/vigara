# Deployment and recovery decisions

Status: **initial decisions recorded**. These are the user-confirmed choices and the proposed defaults the user asked us to adopt for remaining points. Revisit deferred items when their scope is discussed.

## Decision register

| # | Topic | Decision | Status |
|---|---|---|---|
| 1 | Device scale and hardware | Plan for a few dozen devices initially and keep sampling/retention configurable. Build native Windows and Linux agents; select concrete CPU architecture targets during release planning. | Adopted default; architecture packaging remains an implementation detail |
| 2 | Device models and versions | Prefer supported APIs, SNMP, or agents, and document support per model/firmware. Do not promise uniform Synology or TP-Link support without the actual model and version. | Adopted default |
| 3 | Server placement and reachability | Run the Vigira server on the local network. Use a VPN for remote access/reachability for now; agents initiate outbound HTTPS to the server. | Decided |
| 4 | Backup destinations | Support local drives and network drives for the initial backup target. Off-site/object storage is outside the current destination decision. | Decided |
| 5 | Recovery scope | Focus on file backup and file restore for now. Do not plan bare-metal recovery in the current scope. | Decided |
| 6 | Recovery objectives and retention | Make retention a backup-policy setting. No fixed RPO/RTO target is set yet; avoid promising one until a workload needs it. | Retention policy decided; RPO/RTO deferred |
| 7 | Proxmox Backup Server (PBS) | Skip Proxmox backup integration entirely for now. Research and discuss it later before adding scope. | Deferred |
| 8 | Notifications and sleeping devices | Use configurable grace periods and maintenance windows so sleeping devices do not constantly alert. Use Gotify as the first notification destination and support Discord alongside it; enable either or both explicitly in server configuration. | Adopted default |
| 9 | User authentication and OIDC | Use local login; do not add OIDC now. On first visit, show a one-time setup page for the first user. After setup completes, never show that setup page again. Keep agent identity separate from user login. | Decided |
| 10 | Credential isolation and maintenance authority | Keep secrets out of logs, scope credentials to the minimum required, and separate destructive repository maintenance from routine backup execution where the backend permits it. | Adopted default |
| 11 | Compose stack contract | Keep standard Compose files portable; store Vigara stack/profile/assignment metadata in versioned sidecar YAML and validate it with JSON Schema. Use stable explicit Compose project names. | Contract v1 recorded in `docs/compose-management.md`; implementation pending |
| 12 | Git and runtime authority | Git owns saved definitions, encrypted config, and assignments; the database owns drafts, enrollment binding, jobs, runtime observations, and history. Save pushes a revision; deploy selects a pushed revision and never saves implicitly. | Contract v1 recorded; implementation pending |
| 13 | Stateful ownership and replacement | Exclusive/stateful assignments have one active owner, transferred only through a fenced and epoch-incrementing transition. Logical profiles persist across replacement hardware; agent enrollment identity does not. | Contract v1 recorded; implementation pending |
| 14 | Compose settings and imports | Use explicit interpolation inputs and preserve imported root-shared `.env` dependencies per stack; service `env_file` and `environment` retain Compose semantics. Encrypt imported dotenv contents in full initially. | Contract v1 recorded; validate against actual Compose during adoption |
| 15 | SOPS/age trust | Central editor, per-agent target, and offline operator recovery recipients are separate. Private keys stay out of Git; provisioning, ACL, escrow, restore tests, rotation, and revocation are operator prerequisites. | Contract v1 recorded; live key workflow pending |

## First-run setup requirements

- With no user account configured, the first browser visit presents the initial-user setup page.
- The setup flow creates the first local-login user and marks setup complete atomically, so concurrent first visits cannot create multiple initial users.
- After setup completes, the setup page is unavailable on future visits and server restarts. Normal login is shown instead.
- OIDC is not part of the initial authentication scope.

## Deferred follow-up

- Confirm concrete CPU architectures for release builds.
- Identify Synology and TP-Link models/firmware before making model-specific support claims.
- Gotify and Discord selected on 2026-10-11; configure destination credentials locally when enabling delivery.
- Set workload-specific RPO/RTO targets if needed.
- Research Proxmox/PBS integration before reopening that scope.
- Confirm profile and assignment contracts against the second server inventory before broad adoption; no second-host layout is inferred in examples.
