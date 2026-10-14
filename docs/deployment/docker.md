# Docker deployment

The container runs the Vigira API and serves the web UI embedded in the same process. PostgreSQL is a separate service. On startup, the server applies pending migrations embedded in its binary before accepting requests. No migration container or mounted SQL files are required.

The server preserves the `schema_migrations` history used by previous CLI/container deployments and serializes simultaneous migration attempts with a database lock. If a migration fails or the database is marked dirty, startup stops without resetting that state. Inspect and repair the failure before restarting; do not force the version forward without checking the database. The configured database user needs permission to apply the schema changes, including creating the `pgcrypto` extension on a fresh database.

## Image releases

The GitHub Actions workflow publishes `ghcr.io/deranjer/vigara` to GitHub Container Registry:

- A push to `main` publishes `:staging`, `:main`, and a 12-character commit tag such as `:sha-0123456789ab`. The staging tag follows the latest successful default-branch image.
- A version tag such as `v1.2.3` publishes `:1.2.3`, `:1.2`, `:1`, `:latest`, and a short commit tag.
- The workflow can be run manually from `main` to refresh the staging image.

GHCR may make the package private by default. On a staging host that cannot pull it anonymously, authenticate once with a GitHub token that has `read:packages`, then keep the login credentials private:

```sh
echo "$GHCR_TOKEN" | docker login ghcr.io -u YOUR_GITHUB_USERNAME --password-stdin
```

## Run on a staging host

Clone the repository on the host, create the environment file, and set a unique database password. The environment file is ignored by Git and contains secrets.

```sh
cp deploy/.env.staging.example deploy/.env.staging
# Edit deploy/.env.staging and set POSTGRES_PASSWORD.
docker compose --env-file deploy/.env.staging -f deploy/compose.staging.yml pull app
docker compose --env-file deploy/.env.staging -f deploy/compose.staging.yml up -d --remove-orphans
```

By default the setup pulls `ghcr.io/deranjer/vigara:staging`, persists database contents in the `postgres-data` volume, and publishes the app on port 8080. Set `VIGIRA_PORT` in the environment file to change the host port. Set `VIGIRA_IMAGE` to a commit tag or version tag to test a fixed image instead of the moving staging tag. For upgrades, pull the image and run the same `up` command; the server applies new migrations automatically. `--remove-orphans` removes the obsolete migration container from earlier deployments. Use an image containing automatic startup migrations with this simplified Compose file.

## Caddy reverse-proxy example

The [Caddy Compose example](https://github.com/deranjer/vigara/blob/main/deploy/compose.caddy.example.yml) connects the app to an existing external network shared with `caddy-docker-proxy`. It uses Caddy labels for HTTPS routing and keeps PostgreSQL on a private backend network with a named data volume. No app or database host port is published. Container and volume names are scoped to the Compose project.

Create an environment file with your hostname and a URL-safe database password. Set `VIGIRA_PROXY_NETWORK` to the existing proxy network name; it defaults to `web`. You can also set `VIGIRA_IMAGE` to pin a version or commit tag.

```dotenv
POSTGRES_PASSWORD=replace-with-a-long-random-url-safe-password
VIGIRA_DOMAIN=vigara.example.com
VIGIRA_PROXY_NETWORK=web
```

```sh
docker compose --env-file .env -f deploy/compose.caddy.example.yml up -d
```

Point the hostname at your proxy and access the app through HTTPS; secure cookies are enabled. This example requires `caddy-docker-proxy`, which reads Docker labels, rather than a plain Caddy installation. For a different proxy, replace the routing labels with that proxy's configuration. Use the staging example above if you want direct access through a published host port.

When replacing an existing deployment, preserve its actual database volume or bind mount rather than switching it to a new empty named volume. Back up the database before changing storage settings.

## Optional Android file storage

Android uploads are disabled when `VIGIRA_ANDROID_BACKUP_ROOT` is empty. To enable them, set it in `deploy/.env.staging` to `/var/lib/vigira/android-backups`. The Compose setup mounts that path from the persistent `android-backups` volume, and the container user can write there. Protect and back up this volume alongside PostgreSQL; the uploaded files are stored unencrypted. See [Android storage details](../android.md#enable-server-storage).

## Optional agent downloads

Agent bundle downloads require release files and a public HTTPS URL. Build the bundles on an operator machine and copy the output directory to the staging host. From the repository root, the bundle command is:

```sh
python scripts/package_agent_releases.py 0.1.0 deploy/agent-releases
```

Set `VIGIRA_AGENT_RELEASE_HOST_DIR=./agent-releases` and `VIGIRA_PUBLIC_URL=https://vigira.example` in `deploy/.env.staging`. Ensure the directory and files are readable by the container's UID 10001, usually with directory mode 755 and file mode 644. Start or update the app with the optional override file:

```sh
docker compose --env-file deploy/.env.staging -f deploy/compose.staging.yml -f deploy/compose.staging.downloads.example.yml up -d app
```

The bundle directory is mounted read-only at `/opt/vigira/releases`. Omit the override file and leave the URL and host directory unset to keep downloads disabled. See [agent bundle setup](../agent.md#downloading-configured-client-bundles).

Check service state and the health endpoint with:

```sh
docker compose --env-file deploy/.env.staging -f deploy/compose.staging.yml ps
curl http://localhost:8080/healthz
```

The host should put the app behind its HTTPS reverse proxy and set `VIGIRA_COOKIE_SECURE=true` in `deploy/.env.staging` when serving HTTPS. Use a URL-safe password in `POSTGRES_PASSWORD` because the database URL is assembled in Compose. Back up the `postgres-data` volume before testing upgrades or removing the stack. `docker compose down -v` deletes that database volume.
