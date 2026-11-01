#!/usr/bin/env bash
# Interactive initial deployment. Run from any directory: bash /path/to/vigara/deploy/setup.sh
set -Eeuo pipefail
umask 077

fail() { printf 'Error: %s\n' "$*" >&2; exit 1; }
ask() {
  local answer
  read -r -p "$2 [$3]: " answer || fail 'Input ended; setup cancelled.'
  printf -v "$1" '%s' "${answer:-$3}"
}
yesno() {
  local answer
  while true; do
    read -r -p "$1 [y/N]: " answer || fail 'Input ended; setup cancelled.'
    case "$answer" in [yY]|[yY][eE][sS]) return 0;; ''|[nN]|[nN][oO]) return 1;; esac
    printf 'Please enter yes or no.\n'
  done
}

[[ $# == 0 ]] || fail 'Usage: bash deploy/setup.sh (interactive initial setup only)'
[[ $(uname -s) == Linux ]] || fail 'This setup script supports Linux servers.'
for tool in docker openssl sed grep mktemp; do
  command -v "$tool" >/dev/null || fail "Install $tool before continuing."
done
docker info >/dev/null 2>&1 || fail 'Docker is unavailable. Check the daemon and your Docker permissions.'
docker compose version
docker compose up --help | grep -q -- '--wait-timeout' || fail 'Update the Docker Compose plugin; up --wait-timeout is required.'

# Prevent exported configuration from silently overriding the generated env file.
while IFS= read -r variable; do
  case "$variable" in VIGARA_*|VIGIRA_*|POSTGRES_*|COMPOSE_*) unset "$variable";; esac
done < <(compgen -e)

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
repo_dir=$(cd -- "$script_dir/.." && pwd -P)
printf '\nVigara initial server setup\n'
printf 'This creates a new deployment. Existing deployments should keep their original project and database storage.\n'
ask project 'New Compose project name' vigara
[[ $project =~ ^[a-z0-9][a-z0-9_-]*$ ]] || fail 'Project names must use lowercase letters, numbers, underscores or hyphens.'
ask stacks_root 'Shared directory for all Compose stacks (including Vigara)' "$HOME/dockers/compose-stacks"
case "$stacks_root" in '~') stacks_root=$HOME;; '~/'*) stacks_root="$HOME/${stacks_root:2}";; esac
[[ $stacks_root == /* ]] || fail 'Choose an absolute path or a path starting with ~/.'
[[ $stacks_root != *$'\n'* && $stacks_root != *$'\r'* ]] || fail 'The path must not contain line breaks.'
mkdir -p -- "$stacks_root" || fail 'Cannot create the Compose root. Check its ownership and permissions.'
stacks_root=$(cd -- "$stacks_root" && pwd -P)
[[ $stacks_root != "$repo_dir" && $stacks_root != "$repo_dir/"* ]] || fail 'Choose a Compose root outside the Vigara source checkout.'
destination="$stacks_root/$project"
[[ ! -e "$destination" && ! -L "$destination" ]] || fail "Setup already exists at $destination. Use its compose.sh helper; setup never overwrites credentials."
[[ -z $(docker ps -aq --filter "label=com.docker.compose.project=$project") ]] || fail 'That Compose project already has containers. Use its existing deployment configuration.'
[[ -z $(docker volume ls -q --filter "label=com.docker.compose.project=$project") ]] || fail 'That project already has persistent volumes. Preserve its existing deployment configuration.'
if docker volume inspect "${project}_postgres-data" >/dev/null 2>&1; then
  fail 'A database volume already exists for that project. Use its existing deployment configuration.'
fi

printf '\nUse a release tag or sha tag to pin production. The default staging tag moves with main.\n'
ask image 'Container image' ghcr.io/deranjer/vigara:staging
[[ $image =~ ^[a-zA-Z0-9][a-zA-Z0-9._/:@-]*$ ]] || fail 'Invalid image reference.'

mode=direct
if yesno 'Use HTTPS labels for an existing caddy-docker-proxy?'; then
  mode=caddy
  ask domain 'Vigara hostname (without https:// or a path)' ''
  [[ ${#domain} -le 253 && $domain == *.* && $domain != *..* && $domain =~ ^[a-zA-Z0-9]([a-zA-Z0-9.-]*[a-zA-Z0-9])?$ ]] || fail 'Enter a valid DNS hostname.'
  ask network 'Existing Caddy Docker network' web
  [[ $network =~ ^[a-zA-Z0-9][a-zA-Z0-9_.-]*$ ]] || fail 'Invalid network name.'
  docker network inspect "$network" >/dev/null 2>&1 || fail 'Proxy network does not exist. Start your Caddy project first.'
  ask proxy 'Running Caddy proxy container name' caddy-proxy
  [[ $proxy =~ ^[a-zA-Z0-9][a-zA-Z0-9_.-]*$ ]] || fail 'Invalid container name.'
  [[ $(docker inspect --format '{{.State.Running}}' "$proxy" 2>/dev/null) == true ]] || fail 'Start the Caddy proxy before continuing.'
  docker network inspect --format '{{range .Containers}}{{println .Name}}{{end}}' "$network" |
    grep -Fxq "$proxy" || fail 'The Caddy container is not attached to the selected network.'
  printf 'Point %s at your proxy and route ports 80/443 to it. Caddy will discover the labels.\n' "$domain"
  url="https://$domain"
else
  printf '\nDirect access publishes a host port. Loopback supports a local reverse proxy or SSH tunnel.\n'
  ask bind 'Bind IPv4 address (127.0.0.1 local only; 0.0.0.0 all interfaces)' 127.0.0.1
  [[ $bind =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]] || fail 'Enter an IPv4 address.'
  IFS=. read -r -a octets <<< "$bind"
  for octet in "${octets[@]}"; do (( 10#$octet <= 255 )) || fail 'Invalid IPv4 address.'; done
  ask port 'Host port' 8080
  [[ $port =~ ^[0-9]{1,5}$ ]] && (( 10#$port >= 1 && 10#$port <= 65535 )) || fail 'Port must be between 1 and 65535.'
  port=$((10#$port))
  if command -v ss >/dev/null; then
    [[ -z $(ss -H -ltn "sport = :$port") ]] || fail "Port $port is already listening."
  else
    printf 'ss is unavailable; Docker will check port availability on startup.\n'
  fi
  secure=false
  if yesno 'Will a separate HTTPS reverse proxy serve this port?'; then secure=true; fi
  host=$bind
  [[ $host != 0.0.0.0 ]] || host=127.0.0.1
  url="http://$host:$port"
fi

mcp_origin=''
if yesno 'Enable read-only MCP connections for ChatGPT and Claude?'; then
  printf 'Use a public HTTPS origin reachable by both services; Vigara supplies OAuth sign-in.\n'
  default_origin=''
  [[ $mode != caddy ]] || default_origin="https://$domain"
  ask mcp_origin 'Public Vigara origin (https://hostname, without /mcp)' "$default_origin"
  [[ $mcp_origin =~ ^https://[a-zA-Z0-9]([a-zA-Z0-9.-]*[a-zA-Z0-9])?(:[0-9]{1,5})?$ ]] || fail 'MCP requires an HTTPS origin without a path, query or credentials.'
  if [[ $mode == caddy ]]; then
    [[ $mcp_origin == "https://$domain" ]] || fail 'Use the same origin as the Caddy hostname.'
  else
    [[ $secure == true ]] || fail 'Configure an HTTPS reverse proxy before enabling MCP.'
  fi
fi

temporary=$(mktemp -d "$stacks_root/.vigara-setup.XXXXXXXX")
trap 'rm -rf -- "$temporary"' EXIT
password=$(openssl rand -hex 32)
backup_key=$(openssl rand -base64 32)
{
  printf 'POSTGRES_PASSWORD=%s\nVIGARA_IMAGE=%s\n' "$password" "$image"
  printf 'VIGARA_BACKUP_MASTER_KEY=%s\n' "$backup_key"
  printf 'VIGARA_MCP_URL=%s\n' "$mcp_origin"
  if [[ $mode == caddy ]]; then
    printf 'VIGARA_DOMAIN=%s\nVIGARA_PROXY_NETWORK=%s\n' "$domain" "$network"
  else
    printf 'POSTGRES_USER=vigira\nPOSTGRES_DB=vigira\n'
    printf 'VIGARA_BIND_ADDRESS=%s\nVIGARA_PORT=%s\nVIGARA_COOKIE_SECURE=%s\n' "$bind" "$port" "$secure"
  fi
} > "$temporary/.env"
unset password backup_key
if [[ $mode == caddy ]]; then
  # Forward generated application settings without changing the shared template.
  sed '/      VIGARA_ADDR:/i\      VIGARA_BACKUP_MASTER_KEY: ${VIGARA_BACKUP_MASTER_KEY:?Set VIGARA_BACKUP_MASTER_KEY}' \
    "$script_dir/compose.caddy.example.yml" > "$temporary/compose.yml"
else
  sed -e 's/env_file: .env.staging/env_file: .env/g' \
    -e 's/"${VIGARA_PORT:-8080}:8080"/"${VIGARA_BIND_ADDRESS:-127.0.0.1}:${VIGARA_PORT:-8080}:8080"/' \
    "$script_dir/compose.staging.yml" > "$temporary/compose.yml"
fi
{
  printf '#!/usr/bin/env bash\nset -Eeuo pipefail\n'
  printf 'cd -- "$(dirname -- "${BASH_SOURCE[0]}")"\n'
  printf 'while IFS= read -r variable; do\n'
  printf '  case "$variable" in VIGARA_*|VIGIRA_*|POSTGRES_*|COMPOSE_*) unset "$variable";; esac\n'
  printf 'done < <(compgen -e)\n'
  printf 'exec docker compose -p %q --env-file .env -f compose.yml "$@"\n' "$project"
} > "$temporary/compose.sh"
chmod 600 "$temporary/.env"
chmod 700 "$temporary/compose.sh"
bash "$temporary/compose.sh" config --quiet
mv -T -- "$temporary" "$destination"
trap - EXIT

printf '\nConfiguration validated and saved to %s\nProject: %s\nImage: %s\nAccess: %s\n' "$destination" "$project" "$image" "$url"
printf 'Back up .env securely: it contains the database password and backup master key.\n'
printf 'Shared Compose root: %s\n' "$stacks_root"
printf 'When enrolling the host agent, set VIGARA_COMPOSE_MANAGED_ROOT=%q for new managed stacks.\n' "$stacks_root"
if [[ -n $mcp_origin ]]; then
  printf 'MCP endpoint: %s/mcp\nConnection instructions: Account settings > Claude and ChatGPT.\n' "$mcp_origin"
fi
printf 'Manage this deployment with: bash %q/compose.sh ps\n' "$destination"
if ! yesno 'Pull images and start Vigara now?'; then
  printf 'Start later:\n  bash %q/compose.sh pull\n  bash %q/compose.sh up -d --wait --wait-timeout 120\n' "$destination" "$destination"
  exit 0
fi
bash "$destination/compose.sh" pull || fail "Image pull failed. For private GHCR images, run docker login ghcr.io, then retry with $destination/compose.sh pull."
if ! bash "$destination/compose.sh" up -d --wait --wait-timeout 120; then
  bash "$destination/compose.sh" ps
  fail "Startup failed. Inspect with: bash $destination/compose.sh logs --tail 100"
fi
bash "$destination/compose.sh" ps
if command -v curl >/dev/null; then
  if curl --fail --silent --show-error --max-time 10 --retry 5 --retry-delay 2 --retry-connrefused "$url/healthz"; then
    printf '\nHealth check passed.\n'
  else
    printf '\nHealth endpoint could not be reached. Check routing, DNS/TLS and app logs.\n' >&2
  fi
fi
if [[ $mode == direct && $secure == true ]]; then
  printf 'Open your external HTTPS URL to sign in; secure cookies are enabled.\n'
else
  printf 'Open %s and complete first administrator setup.\n' "$url"
fi
printf 'Next: enroll the host agent before configuring Compose stack management.\n'
