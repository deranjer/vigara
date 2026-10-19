# Container management plan

Status: **P0 foundations and P1 (read-only Containers page) implemented; later phases planned**. This document records the design direction for a dedicated Containers area in the web UI. The long-term goal is to replace Portainer's viewing and management features (details, logs, stats, console, lifecycle actions, images, networks, volumes) and to deploy Compose stacks from the web UI. The [Docker inventory module](docker.md) remains read-only today. The P0 foundations (live channel, Docker tiers, roles and audit, deep-linkable routing, and a Containers page that lists connected hosts) are in place; see [users and roles](users-and-roles.md) and the live channel section of the [protocol](protocol.md).

## Decisions

| Topic | Decision |
|---|---|
| Live transport | Each agent opens one persistent **outbound WebSocket** to the server. It multiplexes RPC calls, log and stats streams, and exec terminals. Polling remains for backups and as a fallback. Agents never accept inbound connections. |
| Stack source of truth | Stack deployment follows the [Compose contract v1](compose-management.md): Git authoritative, SOPS-encrypted secrets, profiles and fenced ownership. There is no separate database-only stack store. |
| Users | Add `viewer`, `operator`, and `admin` roles. Viewers read; operators run lifecycle actions and terminals on hosts that allow them; admins manage hosts, users, and agent capability settings. |
| Docker authority | Write access to the Docker socket is effectively root. It is an explicit, local, per-agent opt-in that the server can never enable on its own. |

## What exists today

- Agents poll outbound every 30 seconds and receive at most one job per poll. This is too slow for logs, stats, or a terminal.
- The Docker collector is read-only. It reports at most 32 containers and never calls inspect or reads environment variables, mounts, command arguments, or labels.
- There is no WebSocket or server-sent-event support, and no terminal library in the web app.
- Users have a single `admin` role. There is no audit log.
- The web app switches sections with local state and has no router, so containers cannot be deep-linked yet.
- The Compose contract, adoption discovery, SOPS secret handling, and resource preflight exist; deployment execution does not.

## Architecture

### Agent capability tiers

Each tier is enabled locally on the host, for example with `VIGIRA_DOCKER_MODE`. A higher tier includes the lower ones.

| Tier | Allows |
|---|---|
| `read` | Today's collection, plus live inspect, logs, and stats on request |
| `manage` | Start, stop, restart, pause, unpause, kill, remove, pull, recreate, and image, network, and volume maintenance |
| `exec` | Interactive terminals inside containers |
| `deploy` | Running Compose operations for assigned stacks |

The server reports each agent's enabled tier and refuses requests above it. The agent also refuses them, because the local setting is the authority.

### Server and agent channel

- The agent connects to a WebSocket endpoint with its existing bearer credential. Reconnects use bounded backoff with jitter.
- Messages are versioned and carry a request ID. Requests have deadlines; long-lived streams have explicit open and close messages and flow control.
- The browser connects to the server over its own WebSocket. The server authorizes each request against the user's role, writes the audit record, then relays to the agent. Log and terminal bytes pass through the server and are not stored.
- The plain-HTTP private-network option applies here too: the same server URL policy governs the channel.

### Docker client

Replace the raw REST calls with the official Docker client library. It supports the stream and hijacked-connection handling that exec and attach need. Keep the Engine API minimum version already documented.

### Security and audit

- Every state-changing action and every terminal session is recorded: user, agent, target, action, outcome, and time. Terminal audit records open and close, not keystrokes.
- Inspect output hides environment values by default. Revealing them needs a permission and is audited.
- Destructive actions require confirmation in the UI. Terminals have idle and maximum-duration limits and a per-agent opt-in.
- Requests keep the existing same-origin check, and WebSocket upgrades enforce the same origin rules.

## Phases

Each phase is independently shippable. P1 does not depend on the write tiers.

### P0: Foundations (implemented)

- WebSocket channel between server, agent, and browser, with an RPC and stream message format.
- Capability tiers and local configuration on the agent.
- Audit table and role model; migrate the `role` constraint to allow the three roles.
- Web routing so containers and hosts can be deep-linked.

### P1: Containers page (read-only) (implemented)

- A **Containers** sidebar item listing containers across all enrolled hosts, with search, host and state filters, and grouping by Compose project.
- Pagination in place of the 32-container cap.
- Detail view with live inspect, streaming logs (tail, follow, search, download), and live CPU, memory, network, and disk stats.
- Compose project grouping from the existing Compose observation, which resolves the "Compose stack details are not yet available" notice.

### P2: Lifecycle actions

- Start, stop, restart, pause, unpause, kill, and remove, including bulk selection.
- Pull and recreate, with image digest shown before and after.
- Permission checks, confirmations, and audit entries for every action.

### P3: Terminal

- Interactive exec using xterm.js over a PTY relay, with resize, shell selection, idle timeout, and per-agent opt-in.

### P4: Images, networks, and volumes

- List, inspect, pull, remove, and prune, with usage and unused indicators.
- Volume and network details with the containers that use them.

### P5: Stacks

- Discover and adopt existing Compose projects using the existing adoption preview.
- View, edit, and validate Compose files, then deploy, redeploy, stop, and remove through the contract's preflight, assignment, ownership, and release-record flow.
- Secret editing through the SOPS workflow; plaintext never reaches logs or the browser after save.
- The agent shells out to `docker compose` with an explicit project name, so hosts need the Compose plugin installed. Preflight should report a missing plugin clearly.

### P6: Later

- Docker events feed, image update detection, registry credentials, redeploy webhooks, app templates, and alerts on container state.

## Open questions

- Pagination and storage: whether the container list stays inside the telemetry snapshot or moves to a dedicated table.
- Whether container metrics history needs a longer retention than host metrics.
- How to present multi-host stacks that share a profile.
- Windows container hosts: out of scope until a Windows Docker host exists.
