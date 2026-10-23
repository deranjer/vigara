# Vigara

Self-hosted monitoring and backup for household devices. This repository contains the initial development scaffold; architecture and platform support are still being refined.

## Development

Requirements: Go (see `go.mod`), Node.js with pnpm, Docker Compose, Task, and the `sqlc` CLI. The `migrate` CLI is optional for manual database maintenance.

```sh
cp .env.example .env
task dev:db
task dev:api
```

In another terminal, `task dev:web` starts the Vite development server. The API health endpoint is `http://localhost:8080/healthz`; OpenAPI is served at `/docs`.

Task loads settings from `.env`; exported environment variables take precedence. Set `VIGIRA_DATABASE_URL` to override the local PostgreSQL URL. Set `VIGIRA_COOKIE_SECURE=true` when serving through HTTPS. When running the binary directly, export these settings into its environment. The server automatically applies its embedded database migrations before serving requests. A failed or dirty migration prevents startup. Run `task db:generate` after changing sqlc queries/schema. Useful commands: `task build`, `task check`, and `task dev:down`.

Login and first-user setup share a limit of five attempts per connection IP per minute and 30 attempts per minute across the server, with at most two requests being processed concurrently. Throttled requests return HTTP 429 with `Retry-After`. Limits reset on server restart. Forwarding headers are ignored; clients behind a reverse proxy share its connection IP budget.

Go integration tests run when `VIGIRA_TEST_DATABASE_URL` points to a test PostgreSQL database whose user can create schemas. Each run creates and removes an isolated schema. CI supplies this database automatically.

## Documentation and staging

The [documentation site](https://deranjer.github.io/vigara/docs/) is built with Docusaurus from the Markdown files in `docs`. Run `pnpm docs:dev` to preview it locally or `pnpm docs:build` to check the production build. GitHub Actions deploys it to GitHub Pages when changes reach `main`.

Container builds publish to `ghcr.io/deranjer/vigara`: `main` updates the `staging` tag, and version tags publish numbered releases. See the [Docker deployment guide](docs/deployment/docker.md) for the staging Compose setup, database migrations, and upgrades.

## Layout

- `apps/api`: Go HTTP API and migrations
- `apps/agent`: Go host agent starter
- `apps/web`: React, TypeScript, Vite UI
- `internal/protocol`: versioned server/agent types
- `internal/modules`: module contracts
- `docs`: operator and architecture notes

The initial deployment uses one Go process, embedded web assets, PostgreSQL, and local account authentication. Windows and Linux agents enroll through one-use capability-scoped tokens, poll over HTTPS, and can be revoked by an administrator. See [docs/agent.md](docs/agent.md) for Linux systemd and Windows service setup.

For standalone recovery of Vigira's database, server configuration, agent credentials, and Restic file snapshots, see the [recovery runbook](docs/recovery.md).

For local Restic snapshot browsing, repository checks, alternate-destination restore, and retention planning, see the [snapshot recovery guide](docs/snapshot-recovery.md).

For the native Android companion, its selected-file upload storage, and operator download API, see [Android companion setup](docs/android.md).

Compose stack editing and deployment: [Git-backed stacks setup](docs/git-backed-stacks.md).

Claude and ChatGPT monitoring tools: [MCP and OAuth setup](docs/mcp.md).
