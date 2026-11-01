# Docker deployment

The container runs the Vigira API and serves the web UI embedded in the same process. PostgreSQL is a separate service. On startup, the server applies pending migrations embedded in its binary before accepting requests. No migration container or mounted SQL files are required.

The server preserves the `schema_migrations` history used by previous CLI/container deployments and serializes simultaneous migration attempts with a database lock. If a migration fails or the database is marked dirty, startup stops without resetting that state. Inspect and repair the failure before restarting; do not force the version forward without checking the database. The configured database user needs permission to apply the schema changes, including creating the `pgcrypto` extension on a fresh database.

## Interactive Linux setup

For a new server deployment, run from your cloned repository:

```sh
bash deploy/setup.sh
```

The script checks Docker access and Compose support, asks for an image,
project name and shared Compose root, and offers Caddy Docker Proxy labels or a published IPv4
port. For Caddy it asks for the hostname, shared network and proxy container name.
A running proxy is reused after checking its network attachment. A stopped proxy
can be started with your approval. If the named container does not exist, the
script asks permission to deploy a new Caddy Docker Proxy stack alongside Vigara.
DNS and inbound TCP ports 80/443 must route to this server; the script cannot
change your DNS provider, router or firewall.
For direct access, it defaults to loopback port 8080 and asks whether a separate
HTTPS proxy will serve it, enabling secure cookies when appropriate.

It generates a database password and backup master key, writes private files
under `<compose-root>/<project>/`, and validates Compose before asking
whether to pull images and start. No credentials are printed. The script stops
if that deployment directory or the selected project's containers or volumes already
exist; it is an initial installer, not an adoption or migration tool.

### Optional Caddy bootstrap

When you approve a new proxy, the installer checks that TCP ports 80 and 443 are
free, including Docker-published ports, and that the selected proxy project has
no existing containers, volumes or deployment directory. It requires `ss`
(normally supplied by `iproute2`), `curl`, and a local Unix Docker socket. Remote
Docker contexts are rejected for automatic proxy deployment.

With the default project name, the proxy files are written under
`<compose-root>/vigara-caddy/` and Vigara under `<compose-root>/vigara/`. The proxy
uses the separate `vigara-caddy` Compose project, the chosen container name, and
the published `lucaslorentz/caddy-docker-proxy:alpine` release image by default;
you can choose a pinned image in the prompt. Persistent volumes retain `/data`
(certificates and ACME state) and `/config`. Preserve those volumes on upgrades.

The proxy gets access to the Docker socket to discover labels across the host.
This is privileged Docker access even though the socket mount is marked
read-only. Review the deployment prompt before approving. The installer does
not replace an existing proxy container or change its Caddyfile.

After configuration validation and the final startup approval, setup pulls the
proxy image, creates the shared network if missing, starts Caddy, then pulls and
starts Vigara. If you defer startup, it prints the commands in that order.
Failures retain both generated configurations for inspection and retry.

### Verify HTTPS and MCP

The generated `check.sh` verifies the health endpoint with normal TLS certificate
validation and retries for up to approximately two minutes. With MCP enabled,
it also checks public OAuth resource/issuer metadata and an unauthenticated
`/mcp` request returning HTTP 401. A failed check returns a nonzero status and
does not report setup as ready. The checks run from this server; cloud access
and a live ChatGPT/Claude OAuth connection must still be verified separately.

If HTTPS is not ready, check A and AAAA records, router forwarding/firewall rules
for TCP 80/443, and the proxy/app logs. An incorrect AAAA record can prevent
certificate issuance or connections even when the A record is correct. Retry
verification without rerunning the installer or regenerating credentials:

```sh
bash ~/dockers/compose-stacks/vigara-caddy/compose.sh logs --tail 100
bash ~/dockers/compose-stacks/vigara/compose.sh logs --tail 100
bash ~/dockers/compose-stacks/vigara/check.sh
```

Substitute your selected root and project. For an existing externally managed
proxy, use its own log commands. Direct access without MCP can run without curl,
but the installer explicitly reports that readiness was not verified.

### Deployment files and later management

The shared root defaults to `~/dockers/compose-stacks`, so the default Vigara
deployment lives in `~/dockers/compose-stacks/vigara/`, outside the cloned source
repository. You can choose another absolute path, including paths with spaces.
Other stacks can live alongside Vigara in their own subdirectories. Existing
stack directories are left in place. The installer refuses a root within the
Vigara source checkout.

When configuring the host agent for new managed stacks, set its
`VIGARA_COMPOSE_MANAGED_ROOT` to this same absolute root and grant the agent
write access through both filesystem permissions and its service sandbox. This
is an **agent** setting, not an API server setting. The installer prints the
selected value; agent installation/enrollment remains a separate step. Existing
stacks still require explicit target configuration; choosing a root does not
automatically adopt or relocate them.

After setup, use the generated helper from any working directory (substitute
your chosen root and project):

```sh
bash ~/dockers/compose-stacks/vigara/compose.sh ps
bash ~/dockers/compose-stacks/vigara/compose.sh logs --tail 100
```

The helper keeps the project name, environment file and Compose file together.
It clears exported `VIGARA_*`, `VIGIRA_*`, `POSTGRES_*` and `COMPOSE_*` variables
so the generated `.env` remains authoritative. Edit that file for later changes.
Back it up securely; losing or replacing the backup master key can make managed
backup credentials unrecoverable. Keep the generated Compose file too.

Installations previously created under `deploy/production/` can keep using their
existing helper. This installer does not migrate them or change their storage.

The installer also offers read-only MCP access for ChatGPT and Claude. For Caddy,
it uses the selected HTTPS hostname. Direct-port deployments need an existing
HTTPS proxy and its public origin. MCP remains disabled unless you opt in.
After startup, open **Account settings → Claude and ChatGPT** for the connection
URL, OAuth instructions for each client, and application revocation.

If a pull fails for a private image, authenticate with `docker login ghcr.io`,
then retry `compose.sh pull` and `compose.sh up -d --wait --wait-timeout 120`.
If startup fails, configuration is retained for inspection and retry. HTTPS and
MCP checks follow startup as described above. Complete the first administrator
setup after opening the site.
Host-agent enrollment and management of existing Compose stacks are separate
steps. This script does not configure agent downloads or optional Android storage.

## Environment variable names

Use `VIGARA_*` in deployment environment files. Before upgrading an existing
Compose deployment, rename these host-side inputs if present:

| Previous name | Current name |
| --- | --- |
| `VIGIRA_IMAGE` | `VIGARA_IMAGE` |
| `VIGIRA_PORT` | `VIGARA_PORT` |
| `VIGIRA_DOMAIN` | `VIGARA_DOMAIN` |
| `VIGIRA_PROXY_NETWORK` | `VIGARA_PROXY_NETWORK` |
| `VIGIRA_PUBLIC_URL` | `VIGARA_PUBLIC_URL` |
| `VIGIRA_AGENT_RELEASE_HOST_DIR` | `VIGARA_AGENT_RELEASE_HOST_DIR` |

Compose reads these inputs before the application starts, so the application's
legacy aliases cannot translate them. Old image, port and network inputs would
otherwise silently use defaults; old domain and download inputs fail validation.
Run `docker compose --env-file YOUR_ENV_FILE -f YOUR_COMPOSE_FILE config --quiet`
after updating the names, and review your configured image, port and network.

The application separately supports legacy `VIGIRA_*` settings; explicitly set
`VIGARA_*` application settings take precedence, including empty values.
Keep existing database names, volume names and filesystem paths when upgrading;
the environment prefix change does not require renaming stored data.

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

By default the setup pulls `ghcr.io/deranjer/vigara:staging`, persists database contents in the `postgres-data` volume, and publishes the app on port 8080. Set `VIGARA_PORT` in the environment file to change the host port. Set `VIGARA_IMAGE` to a commit tag or version tag to test a fixed image instead of the moving staging tag. For upgrades, pull the image and run the same `up` command; the server applies new migrations automatically. `--remove-orphans` removes the obsolete migration container from earlier deployments. Use an image containing automatic startup migrations with this simplified Compose file.

## Caddy reverse-proxy example

The [Caddy Compose example](https://github.com/deranjer/vigara/blob/main/deploy/compose.caddy.example.yml) connects the app to an existing external network shared with `caddy-docker-proxy`. It uses Caddy labels for HTTPS routing and keeps PostgreSQL on a private backend network with a named data volume. No app or database host port is published. Container and volume names are scoped to the Compose project.

Create an environment file with your hostname and a URL-safe database password. Set `VIGARA_PROXY_NETWORK` to the existing proxy network name; it defaults to `web`. You can also set `VIGARA_IMAGE` to pin a version or commit tag.

```dotenv
POSTGRES_PASSWORD=replace-with-a-long-random-url-safe-password
VIGARA_DOMAIN=vigara.example.com
VIGARA_PROXY_NETWORK=web
```

```sh
docker compose --env-file .env -f deploy/compose.caddy.example.yml up -d
```

Point the hostname at your proxy and access the app through HTTPS; secure cookies are enabled. This example requires `caddy-docker-proxy`, which reads Docker labels, rather than a plain Caddy installation. For a different proxy, replace the routing labels with that proxy's configuration. Use the staging example above if you want direct access through a published host port.

When replacing an existing deployment, preserve its actual database volume or bind mount rather than switching it to a new empty named volume. Back up the database before changing storage settings.

## Optional Android file storage

Android uploads are disabled when `VIGARA_ANDROID_BACKUP_ROOT` is empty. To enable them, set it in `deploy/.env.staging` to `/var/lib/vigira/android-backups`. The Compose setup mounts that path from the persistent `android-backups` volume, and the container user can write there. Protect and back up this volume alongside PostgreSQL; the uploaded files are stored unencrypted. See [Android storage details](../android.md#enable-server-storage).

## Optional agent downloads

Agent bundle downloads require release files and a public HTTPS URL. Build the bundles on an operator machine and copy the output directory to the staging host. From the repository root, the bundle command is:

```sh
python scripts/package_agent_releases.py 0.1.0 deploy/agent-releases
```

Set `VIGARA_AGENT_RELEASE_HOST_DIR=./agent-releases` and `VIGARA_PUBLIC_URL=https://vigira.example` in `deploy/.env.staging`. Ensure the directory and files are readable by the container's UID 10001, usually with directory mode 755 and file mode 644. For a home network without HTTPS, you can instead set `VIGARA_PUBLIC_URL=http://192.168.1.50:8080` and `VIGARA_ALLOW_INSECURE_AGENT_HTTP=true`; add other addresses such as a Tailscale one with `VIGARA_PUBLIC_URLS`. This is limited to private-network addresses and leaves agent traffic unencrypted; read [Server addresses and plain HTTP](../agent.md#server-addresses-and-plain-http) first. Start or update the app with the optional override file:

```sh
docker compose --env-file deploy/.env.staging -f deploy/compose.staging.yml -f deploy/compose.staging.downloads.example.yml up -d app
```

The bundle directory is mounted read-only at `/opt/vigira/releases`. Omit the override file and leave the URL and host directory unset to keep downloads disabled. See [agent bundle setup](../agent.md#downloading-configured-client-bundles).

Check service state and the health endpoint with:

```sh
docker compose --env-file deploy/.env.staging -f deploy/compose.staging.yml ps
curl http://localhost:8080/healthz
```

The host should put the app behind its HTTPS reverse proxy and set `VIGARA_COOKIE_SECURE=true` in `deploy/.env.staging` when serving HTTPS. Use a URL-safe password in `POSTGRES_PASSWORD` because the database URL is assembled in Compose. Back up the `postgres-data` volume before testing upgrades or removing the stack. `docker compose down -v` deletes that database volume.
