# Vigira

Self-hosted monitoring and backup for household devices. This repository contains the initial development scaffold; architecture and platform support are still being refined.

## Development

Requirements: Go (see `go.mod`), Node.js with pnpm, Docker Compose, and Task.

```sh
cp .env.example .env
task dev:db
task dev:api
```

In another terminal, `task dev:web` starts the Vite development server. The API health endpoint is `http://localhost:8080/healthz`; OpenAPI is served at `/docs`.

Useful commands: `task build`, `task check`, `task db:migrate`, and `task dev:down`.

## Layout

- `apps/api`: Go HTTP API and migrations
- `apps/agent`: Go host agent starter
- `apps/web`: React, TypeScript, Vite UI
- `internal/protocol`: versioned server/agent types
- `internal/modules`: module contracts
- `docs`: operator and architecture notes

The initial deployment uses one Go process, embedded web assets, and PostgreSQL. Enrollment, telemetry ingestion, backup execution, and authentication remain future implementation work.
