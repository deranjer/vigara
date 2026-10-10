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

Task loads settings from `.env`; exported environment variables take precedence. Set `VIGIRA_DATABASE_URL` to override the local PostgreSQL URL. Set `VIGIRA_COOKIE_SECURE=true` when serving through HTTPS. When running the binary directly, export these settings into its environment. The server refuses to start before migrations have been applied. Run `task db:generate` after changing sqlc queries/schema. Useful commands: `task build`, `task check`, and `task dev:down`.

Login and first-user setup share a limit of five attempts per connection IP per minute and 30 attempts per minute across the server, with at most two requests being processed concurrently. Throttled requests return HTTP 429 with `Retry-After`. Limits reset on server restart. Forwarding headers are ignored; clients behind a reverse proxy share its connection IP budget.

Go integration tests run when `VIGIRA_TEST_DATABASE_URL` points to a test PostgreSQL database whose user can create schemas. Each run creates and removes an isolated schema. CI supplies this database automatically.

## Layout

- `apps/api`: Go HTTP API and migrations
- `apps/agent`: Go host agent starter
- `apps/web`: React, TypeScript, Vite UI
- `internal/protocol`: versioned server/agent types
- `internal/modules`: module contracts
- `docs`: operator and architecture notes

The initial deployment uses one Go process, embedded web assets, PostgreSQL, and local account authentication. Enrollment, telemetry ingestion, and backup execution remain future implementation work.
