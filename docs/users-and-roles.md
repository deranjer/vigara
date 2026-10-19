# Users and roles

The first account created during setup is an administrator. Administrators can add further users with one of three roles. A role grants a fixed set of permissions; handlers check the specific permission an action needs.

| Permission | Viewer | Operator | Admin |
|---|---|---|---|
| View hosts and containers | yes | yes | yes |
| Manage containers, images, networks, and volumes | no | yes | yes |
| Open terminals in containers | no | yes | yes |
| Deploy stacks | no | no | yes |
| Reveal secret values (such as container environment values) | no | no | yes |
| Manage hosts, enrollment, and backups | no | no | yes |
| Manage users and read the audit log | no | no | yes |

Operators and administrators can start, stop, restart, pause, resume, kill, remove, and recreate containers on hosts that allow management. Every one of these actions, and every refused attempt, is recorded in the audit log as `container.start`, `container.stop`, `container.restart`, `container.pause`, `container.unpause`, `container.kill`, `container.remove`, or `container.recreate`. Every state-changing request outside a few self-service routes (sign-in, setup, sign-out, agent and live-channel routes) requires the admin role, so a viewer or operator can never reach an administrative endpoint by accident.

## Managing users

These endpoints require an administrator session:

| Method and path | Purpose |
|---|---|
| `GET /api/users` | List users (no credentials are returned) |
| `POST /api/users` | Create a user: `username`, `displayName`, `password` (at least 12 characters), and `role` |
| `PATCH /api/users/{id}` | Change a user's `role` |

The last administrator cannot be demoted. Lowering a user's role signs out all of their sessions so older sessions cannot keep elevated access; raising a role does not. A user management page is not part of the web UI yet.

## Audit log

Administrative and, later, container actions are recorded as who did what to which target, with an outcome of `success`, `failure`, or `denied`. Entries hold metadata only: no secret values, command output, or terminal keystrokes. Administrators read them with `GET /api/audit`, newest first, using `limit` (up to 200), `before` (an entry ID, for the next page), and optional `agentId`, `actorId`, and `action` filters.
