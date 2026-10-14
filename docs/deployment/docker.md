# Docker deployment

The container runs the Vigira API and serves the web UI embedded in the same process. PostgreSQL is a separate service. The API does not migrate the database automatically, so the staging Compose setup applies checked-in migrations before starting it.

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
docker compose --env-file deploy/.env.staging -f deploy/compose.staging.yml up -d db
docker compose --env-file deploy/.env.staging -f deploy/compose.staging.yml run --rm migrate
docker compose --env-file deploy/.env.staging -f deploy/compose.staging.yml up -d app
```

By default the setup pulls `ghcr.io/deranjer/vigara:staging`, persists database contents in the `postgres-data` volume, and publishes the app on port 8080. Set `VIGIRA_PORT` in the environment file to change the host port. Set `VIGIRA_IMAGE` to a commit tag or version tag to test a fixed image instead of the moving staging tag. Before each deployment, pull the image, run the migration service, and then recreate the app so the database schema is ready for that image.

Check service state and the health endpoint with:

```sh
docker compose --env-file deploy/.env.staging -f deploy/compose.staging.yml ps
curl http://localhost:8080/healthz
```

The host should put the app behind its HTTPS reverse proxy and set `VIGIRA_COOKIE_SECURE=true` in `deploy/.env.staging` when serving HTTPS. Use a URL-safe password in `POSTGRES_PASSWORD` because the database URL is assembled in Compose. Back up the `postgres-data` volume before testing upgrades or removing the stack. `docker compose down -v` deletes that database volume.
