# Vigira

Self-hosted monitoring and backup for household devices. This repository contains the initial development scaffold; architecture and platform support are still being refined.

## Development

Requirements: Go (see `go.mod`), Node.js with pnpm, Docker Compose, Task, and the `migrate` and `sqlc` CLIs.

```sh
cp .env.example .env
task dev:db
task db:migrate
task dev:api
```

In another terminal, `task dev:web` starts the Vite development server. The API health endpoint is `http://localhost:8080/healthz`; OpenAPI is served at `/docs`.

Set `VIGIRA_DATABASE_URL` to override the local PostgreSQL URL. Set `VIGIRA_COOKIE_SECURE=true` when serving through HTTPS. The server refuses to start before migrations have been applied. Run `task db:generate` after changing sqlc queries/schema. Useful commands: `task build`, `task check`, and `task dev:down`.

## Layout

- `apps/api`: Go HTTP API and migrations
- `apps/agent`: Go host agent starter
- `apps/web`: React, TypeScript, Vite UI
- `internal/protocol`: versioned server/agent types
- `internal/modules`: module contracts
- `docs`: operator and architecture notes

The initial deployment uses one Go process, embedded web assets, and PostgreSQL. Enrollment, telemetry ingestion, backup execution, and authentication remain future implementation work.
