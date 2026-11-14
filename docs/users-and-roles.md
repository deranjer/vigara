# Users and roles

The first account created during setup is an administrator. In **Settings → Users & access**, administrators can create accounts with access to selected resources, or assign one of the three global roles below. Existing global roles retain their permissions.

| Permission | Viewer | Operator | Admin |
|---|---|---|---|
| View hosts and containers | yes | yes | yes |
| Manage containers, images, networks, and volumes | no | yes | yes |
| Open terminals in containers | no | yes | yes |
| Deploy stacks | no | no | yes |
| Reveal secret values (such as container environment values) | no | no | yes |
| Manage hosts, enrollment, and backups | no | no | yes |
| Manage users and read the audit log | no | no | yes |

Operators and administrators can also open terminals in containers on hosts that allow them (see [Terminals](docker.md#terminals)). They can start, stop, restart, pause, resume, kill, remove, and recreate containers on hosts that allow management. Every one of these actions, and every refused attempt, is recorded in the audit log as `container.start`, `container.stop`, `container.restart`, `container.pause`, `container.unpause`, `container.kill`, `container.remove`, or `container.recreate`. Terminals are recorded as `container.exec.open` and `container.exec.close`, and changes to images, networks, and volumes as `image.pull`, `image.remove`, `image.prune`, `network.remove`, `network.prune`, `volume.remove`, and `volume.prune`. Every state-changing request outside a few self-service routes (sign-in, setup, sign-out, agent and live-channel routes) requires the admin role, so a viewer or operator can never reach an administrative endpoint by accident.

## Managing users

These endpoints require an administrator session:

| Method and path | Purpose |
|---|---|
| `GET /api/users` | List users (no credentials are returned) |
| `POST /api/users` | Create a user: `username`, `displayName`, `password` (at least 12 characters), and `role` (`restricted`, `viewer`, `operator`, or `admin`) |
| `PATCH /api/users/{id}` | Change a user's `role` |
| `GET /api/users/{id}/resources` | Read the user's selected resource grants |
| `PUT /api/users/{id}/resources` | Replace selected resource access with `{ "grants": [...] }` |

The last administrator cannot be demoted. Lowering a user's role signs out all of their sessions so older sessions cannot keep elevated access; raising a role does not. Changing account type clears previous resource grants. Saving selected resource access signs out the affected user's sessions, including connections linked to those sessions.

## Selected resource access

Choose **Selected resources** when creating an account, then add stacks, Compose services, or standalone containers. New accounts have no resource access until grants are saved. Each selection includes View and independently allows **Read raw logs**, **Restart**, **Recreate**, and **Pull & recreate**. Overlapping grants add permissions; an unchecked box does not override another grant that allows the same action.

The Read only preset disables the optional permissions. The Operator preset enables restart, recreate, and pull-and-recreate; it does not automatically expose raw logs. Raw logs may contain secrets, so shared [saved log values](log-values.md) can be viewed without that permission.

Grants are tied to the agent plus Compose project/service labels. A stack grant covers every service in that project on that host, including future services. A service grant follows replacement container IDs. Standalone grants use a full immutable container ID and must be reassigned after replacement. Renaming or moving a project does not transfer access automatically. Display names and short container IDs cannot be used to execute restricted actions.

Selected-resources users see a shared dashboard with only their permitted resources. Stack controls process the currently listed, permitted containers sequentially and stop on failure; they do not edit or deploy Compose source. Recreate preserves the current configuration. Pull & recreate uses the existing image reference, so digest-pinned images remain pinned. Host-side Docker management must also be enabled.

These accounts cannot view host inventory, backups, Compose source, arbitrary Docker resources, environment values, mounted files, terminals, or administrative APIs. They cannot stop, delete, kill, or force-remove containers. Access checks apply to direct URLs and live streams, not just visible controls. Existing log/stat streams recheck sessions and grants every five seconds. The global MCP tools are unavailable to selected-resources accounts; this prevents their broader inventory and management tools from bypassing resource grants.

Example grant:

```json
{
  "grants": [{
    "agentId": "11111111-1111-4111-8111-111111111111",
    "kind": "service",
    "resource": "dragonwilds",
    "service": "dragonwilds",
    "logs": false,
    "restart": true,
    "recreate": false,
    "pullRecreate": false
  }]
}
```

For a stack grant, use `kind: "stack"`, the project name as `resource`, and omit `service`. For a standalone grant, use `kind: "container"` and the full container ID as `resource`.

## Audit log

Administrative and, later, container actions are recorded as who did what to which target, with an outcome of `success`, `failure`, or `denied`. Entries hold metadata only: no secret values, command output, or terminal keystrokes. Administrators read them with `GET /api/audit`, newest first, using `limit` (up to 200), `before` (an entry ID, for the next page), and optional `agentId`, `actorId`, and `action` filters.
