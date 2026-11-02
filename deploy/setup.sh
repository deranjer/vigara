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

offer_fresh_start() {
  local name=$1 directory=$2 containers volumes confirmation backup='' item owner
  containers=$(docker ps -aq --filter "label=com.docker.compose.project=$name") || fail 'Cannot inventory existing containers.'
  volumes=$(docker volume ls -q --filter "label=com.docker.compose.project=$name") || fail 'Cannot inventory existing volumes.'
  # Never delete an unlabelled volume merely because its name looks familiar.
  if docker volume inspect "${name}_postgres-data" >/dev/null 2>&1; then
    owner=$(docker volume inspect --format '{{index .Labels "com.docker.compose.project"}}' "${name}_postgres-data")
    [[ $owner == "$name" ]] || fail "Volume ${name}_postgres-data has no matching project label; resolve it manually."
  fi
  [[ -n $containers || -n $volumes || -e $directory || -L $directory ]] || return 0
  printf '\nWARNING: Existing deployment resources found for Compose project %s.\n' "$name"
  printf 'Containers to DELETE (including running containers):\n%s\n' "${containers:-(none)}"
  printf 'Volumes to PERMANENTLY DELETE:\n%s\n' "${volumes:-(none)}"
  if [[ -e $directory || -L $directory ]]; then
    [[ -d $directory && ! -L $directory && -f $directory/compose.sh && -f $directory/compose.yml && -f $directory/.env ]] ||
      fail "Setup already exists at $directory but is not a recognized deployment directory. Move it manually."
    backup=$(mktemp -d -- "${directory}.before-reset.XXXXXXXX") || fail 'Cannot reserve a backup directory.'
    # Only reserve the name here. Declining must leave the deployment untouched.
    rmdir -- "$backup"
    printf 'Configuration and bind-mounted data at %s will move to:\n%s\n' "$directory" "$backup"
  fi
  printf '\nDANGER: This destroys the selected project\047s database, users, enrollment credentials and other named-volume data.\n'
  printf 'There is NO UNDO for volume deletion. Back up anything you need before continuing.\n'
  printf 'Enrolled agents will need enrolling again after database deletion. A proxy reset also deletes its saved TLS certificates.\n'
  printf 'Archived bind data is retained on disk; external bind paths and shared proxies are not erased.\n'
  yesno "Purge project $name and create a completely fresh deployment?" || fail "Setup already exists: keeping project $name and its data unchanged."
  read -r -p "FINAL WARNING: type PURGE $name exactly to confirm permanent deletion: " confirmation || fail 'Input ended; purge cancelled.'
  [[ $confirmation == "PURGE $name" ]] || fail 'Confirmation did not match; nothing deleted.'
  require_local_docker
  # Abort if any deletion fails. Never proceed to generate replacement secrets
  # over a partially removed deployment. Delete the exact inventoried resources.
  while IFS= read -r item; do
    [[ -z $item ]] || docker rm -f "$item" || fail 'Container deletion failed; reset stopped.'
  done <<< "$containers"
  while IFS= read -r item; do
    [[ -z $item ]] || docker volume rm "$item" || fail 'Volume deletion failed; reset stopped.'
  done <<< "$volumes"
  if [[ -n $backup ]]; then
    mv -T -- "$directory" "$backup" || fail 'Could not archive the deployment; reset stopped.'
    printf 'Previous configuration and bind data retained at %s\n' "$backup"
  fi
  printf 'Project %s reset. Continuing with fresh credentials and storage.\n' "$name"
}

write_helper() {
  local directory=$1 name=$2
  {
    printf '#!/usr/bin/env bash\nset -Eeuo pipefail\n'
    printf 'cd -- "$(dirname -- "${BASH_SOURCE[0]}")"\n'
    printf 'while IFS= read -r variable; do\n'
    printf '  case "$variable" in VIGARA_*|VIGIRA_*|POSTGRES_*|COMPOSE_*) unset "$variable";; esac\n'
    printf 'done < <(compgen -e)\n'
    printf 'exec docker compose -p %q --env-file .env -f compose.yml "$@"\n' "$name"
  } > "$directory/compose.sh"
  chmod 700 "$directory/compose.sh"
}

check_proxy_ports() {
  command -v ss >/dev/null || fail 'Install iproute2 (ss) to check ports before deploying Caddy.'
  local port
  for port in 80 443; do
    [[ -z $(ss -H -ltn "sport = :$port") ]] || fail "Port $port is already listening. Use the existing proxy or free the port first."
    [[ -z $(docker ps -q --filter "publish=$port") ]] || fail "Docker already publishes port $port. Use the existing proxy or free the port first."
  done
}

require_local_docker() {
  local endpoint
  if [[ -n ${DOCKER_CONTEXT:-} || -z ${DOCKER_HOST:-} ]]; then
    endpoint=$(docker context inspect --format '{{.Endpoints.docker.Host}}')
  else
    endpoint=$DOCKER_HOST
  fi
  [[ $endpoint == unix://* ]] || fail 'Bind mounts and automatic proxy deployment require a local Unix Docker socket; remote Docker contexts are unsupported.'
  socket=${endpoint#unix://}
  [[ $socket =~ ^/[a-zA-Z0-9_./-]+$ && -S $socket ]] || fail 'Cannot access the local Docker socket.'
}

choose_proxy_volumes() {
  local volume owner choice confirmation existing=''
  # Inspect the exact names used by the bundled proxy template. Never adopt or
  # delete a volume belonging to another project, even if its name collides.
  for volume in "${proxy_project}_caddy-data" "${proxy_project}_caddy-config"; do
    if docker volume inspect "$volume" >/dev/null 2>&1; then
      owner=$(docker volume inspect --format '{{index .Labels "com.docker.compose.project"}}' "$volume")
      [[ $owner == "$proxy_project" ]] || fail "Volume $volume is not owned by $proxy_project. Use its existing configuration or choose another project."
      proxy_volumes+=("$volume")
    fi
  done
  existing=$(docker volume ls -q --filter "label=com.docker.compose.project=$proxy_project")
  [[ -n $existing || ${#proxy_volumes[@]} != 0 ]] || return 0
  printf '\nWarning: existing volumes found for proxy project %s:\n%s\n' "$proxy_project" "$existing"
  printf 'Other volumes are preserved. Only these Caddy volumes can be purged:\n'
  printf '  %s\n' "${proxy_volumes[@]}"
  printf 'Reusing Caddy volumes preserves certificates and configuration. Purging permanently deletes them.\n'
  while true; do
    ask choice 'Existing proxy volumes: keep, purge, or cancel' keep
    case "$choice" in
      keep)
        if (( ${#proxy_volumes[@]} > 0 )); then
          proxy_storage=volumes
          printf 'Keeping existing Caddy named volumes, including when Vigara uses bind storage.\n'
        fi
        return 0;;
      purge)
        (( ${#proxy_volumes[@]} > 0 )) || fail 'No expected Caddy volumes are available to purge.'
        ask confirmation "Type PURGE $proxy_project to permanently delete the listed Caddy volumes at startup" ''
        [[ $confirmation == "PURGE $proxy_project" ]] || fail 'Purge confirmation did not match; no volumes were deleted.'
        proxy_purge=true
        return 0;;
      cancel) fail 'Setup cancelled; no volumes were deleted.';;
      *) printf 'Please enter keep, purge, or cancel.\n';;
    esac
  done
}

write_proxy_purge_helper() {
  local directory=$1
  {
    printf '#!/usr/bin/env bash\nset -Eeuo pipefail\n'
    printf 'project=%q\nvolumes=(' "$proxy_project"
    printf ' %q' "${proxy_volumes[@]}"
    printf ' )\n'
    cat <<'BASH'
# Recheck every target before deleting anything, including when started later.
for volume in "${volumes[@]}"; do
  docker volume inspect "$volume" >/dev/null 2>&1 || continue
  owner=$(docker volume inspect --format '{{index .Labels "com.docker.compose.project"}}' "$volume")
  [[ $owner == "$project" ]] || { echo "Refusing to purge $volume: project ownership changed." >&2; exit 1; }
  consumers=$(docker ps -aq --filter "volume=$volume")
  [[ -z $consumers ]] || { echo "Refusing to purge $volume: a container still references it." >&2; exit 1; }
done
for volume in "${volumes[@]}"; do
  docker volume inspect "$volume" >/dev/null 2>&1 || continue
  docker volume rm "$volume"
done
BASH
  } > "$directory/purge-volumes.sh"
  chmod 700 "$directory/purge-volumes.sh"
}

use_bind_storage() {
  local directory=$1 name=$2 line source target data_root=./data prepare_api=false
  local -a api_directories=()
  if [[ $bind_root != . ]]; then
    data_root="$bind_root/$name/data"
    [[ ! -L $bind_root/$name && ! -L $data_root ]] || fail 'External bind data must not use a symlink project or data directory.'
    [[ ! -e $data_root || -d $data_root ]] || fail "Expected a directory at $data_root."
  fi
  # Convert only the known persistent mounts in our bundled templates. The
  # top-level volume declarations are the final section in these templates.
  while IFS= read -r line; do
    case "$line" in
      'volumes:') break;;
      '      - postgres-data:/var/lib/postgresql/data') source=postgres; target=/var/lib/postgresql/data;;
      '      - android-backups:/var/lib/vigira/android-backups') source=android-backups; target=/var/lib/vigira/android-backups;;
      '      - agent-releases:/var/lib/vigira/agent-releases') source=agent-releases; target=/var/lib/vigira/agent-releases;;
      '      - caddy-data:/data') source=caddy; target=/data;;
      '      - caddy-config:/config') source=config; target=/config;;
      *) printf '%s\n' "$line"; continue;;
    esac
    case "$source" in android-backups|agent-releases) prepare_api=true; api_directories+=("$source");; esac
    if [[ $bind_root == . ]]; then
      mkdir -p -- "$directory/data/$source"
    else
      [[ ! -L $data_root/$source ]] || fail 'External bind data must not use symlink directories.'
      if [[ -e $data_root/$source ]]; then
        [[ -d $data_root/$source ]] || fail "Expected a directory at $data_root/$source."
        (shopt -s nullglob dotglob; entries=("$data_root/$source"/*); (( ${#entries[@]} == 0 ))) ||
          fail "Bind data already exists at $data_root/$source. Preserve its original deployment configuration or choose another root."
      fi
      mkdir -p -- "$data_root/$source" || fail 'Cannot create bind data directories. Check the selected root permissions.'
    fi
    printf '      - type: bind\n        source: %s/%s\n        target: %s\n        bind:\n          create_host_path: true\n' "$data_root" "$source" "$target"
  done < "$directory/compose.yml" > "$directory/compose.bind.yml"
  mv -- "$directory/compose.bind.yml" "$directory/compose.yml"
  if [[ $prepare_api == true ]]; then
    # The API runs as UID/GID 10001. PostgreSQL and Caddy initialize their own
    # fresh bind directories; the API cannot chown its upload/cache directories.
    {
      printf '#!/usr/bin/env bash\nset -Eeuo pipefail\n'
      printf 'data_root=%q\n' "$data_root"
      printf 'api_directories=('
      printf ' %q' "${api_directories[@]}"
      printf ' )\n'
      cat <<'BASH'
cd -- "$(dirname -- "${BASH_SOURCE[0]}")"
if [[ -n ${DOCKER_CONTEXT:-} || -z ${DOCKER_HOST:-} ]]; then
  endpoint=$(docker context inspect --format '{{.Endpoints.docker.Host}}')
else
  endpoint=$DOCKER_HOST
fi
[[ $endpoint == unix://* && -S ${endpoint#unix://} ]] || { echo 'Bind storage preparation requires a local Docker socket.' >&2; exit 1; }
for name in "${api_directories[@]}"; do
  [[ ! -L $data_root/$name ]] || { echo 'Expected a real API data directory.' >&2; exit 1; }
  mkdir -p -- "$data_root/$name"
  [[ -d $data_root/$name ]] || { echo 'Expected a real API data directory.' >&2; exit 1; }
  data_dir=$(cd -- "$data_root/$name" && pwd -P)
# Mount over the helper image's declared data volume so no anonymous volume is
# created. Change only this directory's owner, without recursive chmod/chown.
docker run --rm --user 0:0 --entrypoint chown \
  --mount "type=bind,source=$data_dir,target=/var/lib/postgresql/data" \
  postgres:17-alpine 10001:10001 /var/lib/postgresql/data
done
BASH
    } > "$directory/prepare-storage.sh"
    chmod 700 "$directory/prepare-storage.sh"
  fi
}

[[ $# == 0 ]] || fail 'Usage: bash deploy/setup.sh (interactive initial setup only)'
[[ $(uname -s) == Linux ]] || fail 'This setup script supports Linux servers.'
for tool in docker openssl sed grep mktemp realpath; do
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
offer_fresh_start "$project" "$destination"
for container in "${project}-app" "${project}-postgres"; do
  if docker container inspect "$container" >/dev/null 2>&1; then
    fail "Container name $container already exists. Choose another project name or use its existing configuration."
  fi
done

printf '\nUse a release tag or sha tag to pin production. The default staging tag moves with main.\n'
ask image 'Container image' ghcr.io/deranjer/vigara:staging
[[ $image =~ ^[a-zA-Z0-9][a-zA-Z0-9._/:@-]*$ ]] || fail 'Invalid image reference.'

storage=volumes
bind_root=.
if yesno 'Use bind-mounted data directories instead of Docker named volumes?'; then
  storage=bind
  require_local_docker
  [[ $stacks_root != *,* ]] || fail 'Choose a Compose root without commas for bind storage.'
  ask bind_root 'Bind data root (. keeps data beside each Compose file; or an absolute path such as /mnt/data)' .
  case "$bind_root" in '~') bind_root=$HOME;; '~/'*) bind_root="$HOME/${bind_root:2}";; esac
  if [[ $bind_root != . ]]; then
    [[ $bind_root == /* && $bind_root =~ ^[a-zA-Z0-9_./\ -]+$ ]] || fail 'Choose an absolute bind root using letters, numbers, spaces, dots, underscores or hyphens, or use . for the default.'
    bind_root=$(realpath -m -- "$bind_root") || fail 'Cannot resolve the bind root.'
    [[ $bind_root =~ ^[a-zA-Z0-9_./\ -]+$ ]] || fail 'The resolved bind root contains unsupported characters.'
    [[ $bind_root != "$repo_dir" && $bind_root != "$repo_dir/"* ]] || fail 'Choose a bind root outside the Vigara source checkout.'
    for deployment_path in "$destination" "$stacks_root/$project-caddy"; do
      [[ $bind_root != "$deployment_path" && $bind_root != "$deployment_path/"* ]] || fail 'Choose a bind root outside the deployment directories.'
    done
    mkdir -p -- "$bind_root" || fail 'Cannot create the bind root. Check its ownership and permissions.'
    # The explicit Compose root resolves to the same layout as the default.
    [[ $bind_root != "$stacks_root" ]] || bind_root=.
  fi
fi

mode=direct
proxy_action=none
proxy_destination=''
proxy_storage=$storage
proxy_purge=false
proxy_volumes=()
if yesno 'Use Caddy Docker Proxy labels for automatic HTTPS?'; then
  mode=caddy
  command -v curl >/dev/null || fail 'Install curl to verify HTTPS readiness.'
  ask domain 'Vigara hostname (without https:// or a path)' ''
  [[ ${#domain} -le 253 && $domain == *.* && $domain != *..* && $domain =~ ^[a-zA-Z0-9]([a-zA-Z0-9.-]*[a-zA-Z0-9])?$ ]] || fail 'Enter a valid DNS hostname.'
  ask network 'Shared Caddy Docker network' web
  [[ $network =~ ^[a-zA-Z0-9][a-zA-Z0-9_.-]*$ ]] || fail 'Invalid network name.'
  ask proxy 'Caddy proxy container name (existing or new)' caddy-proxy
  [[ $proxy =~ ^[a-zA-Z0-9][a-zA-Z0-9_.-]*$ ]] || fail 'Invalid container name.'
  [[ $proxy != "$project-app" && $proxy != "$project-postgres" ]] || fail 'The proxy name conflicts with a Vigara container name.'
  proxy_running=$(docker inspect --format '{{.State.Running}}' "$proxy" 2>/dev/null) || proxy_running=missing
  case "$proxy_running" in
    true)
      docker network inspect "$network" >/dev/null 2>&1 || fail 'The existing proxy network is unavailable.'
      docker network inspect --format '{{range .Containers}}{{println .Name}}{{end}}' "$network" |
        grep -Fxq "$proxy" || fail 'The Caddy container is not attached to the selected network.'
      printf 'Using running proxy %s.\n' "$proxy"
      ;;
    false)
      yesno "Start the existing stopped Caddy container $proxy during startup?" || fail 'Start your Caddy proxy, then rerun setup.'
      docker inspect --format '{{range $name, $_ := .NetworkSettings.Networks}}{{println $name}}{{end}}' "$proxy" |
        grep -Fxq "$network" || fail 'The stopped proxy is not configured for the selected network.'
      check_proxy_ports
      proxy_action=start
      ;;
    missing)
      printf 'No container named %s exists. A new proxy will publish ports 80/443 and access the Docker socket to read routing labels. Docker socket access is privileged.\n' "$proxy"
      yesno 'Deploy a new Caddy Docker Proxy stack during startup?' || fail 'Caddy deployment declined. Start your own proxy before rerunning setup.'
      proxy_action=create
      proxy_project="${project}-caddy"
      proxy_destination="$stacks_root/$proxy_project"
      proxy_containers=$(docker ps -aq --filter "label=com.docker.compose.project=$proxy_project") || fail 'Cannot inventory existing proxy containers.'
      proxy_reset=false
      if [[ -n $proxy_containers || -e $proxy_destination || -L $proxy_destination ]]; then
        offer_fresh_start "$proxy_project" "$proxy_destination"
        proxy_reset=true
      fi
      check_proxy_ports
      require_local_docker
      # A complete reset already handled the proxy's storage. Volumes alone
      # can instead be kept without resetting the project or its certificates.
      if [[ $proxy_reset == false ]]; then choose_proxy_volumes; fi
      ask proxy_image 'Caddy Docker Proxy image' lucaslorentz/caddy-docker-proxy:alpine
      [[ $proxy_image =~ ^[a-zA-Z0-9][a-zA-Z0-9._/:@-]*$ ]] || fail 'Invalid proxy image reference.'
      ;;
    *) fail 'Could not determine the proxy state.';;
  esac
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

public_url=''
if [[ $mode == caddy ]]; then
  public_url="https://$domain"
else
  ask public_url 'HTTPS address agents can reach (leave blank to configure later)' ''
  [[ -z $public_url || $public_url =~ ^https://[a-zA-Z0-9]([a-zA-Z0-9.-]*[a-zA-Z0-9])?(:[0-9]{1,5})?$ ]] || fail 'Enter an HTTPS origin without a path, query or credentials.'
fi

mcp_origin=''
if yesno 'Enable read-only MCP connections for ChatGPT and Claude?'; then
  command -v curl >/dev/null || fail 'Install curl to verify MCP readiness.'
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
  printf 'VIGARA_PUBLIC_URL=%s\n' "$public_url"
  # Quote path values, escaping Compose interpolation separately from systemd.
  agent_root=${stacks_root//\\/\\\\}
  agent_root=${agent_root//\"/\\\"}
  compose_root=${agent_root//\$/\$\$}
  printf 'VIGARA_COMPOSE_MANAGED_ROOT="%s"\n' "$compose_root"
  printf 'VIGARA_BIND_ROOT="%s"\n' "$bind_root"
  if [[ $mode == caddy ]]; then
    printf 'VIGARA_DOMAIN=%s\nVIGARA_PROXY_NETWORK=%s\n' "$domain" "$network"
  else
    printf 'POSTGRES_USER=vigira\nPOSTGRES_DB=vigira\n'
    printf 'VIGARA_BIND_ADDRESS=%s\nVIGARA_PORT=%s\nVIGARA_COOKIE_SECURE=%s\n' "$bind" "$port" "$secure"
  fi
} > "$temporary/.env"
printf 'VIGARA_COMPOSE_MANAGED_ROOT="%s"\n' "$agent_root" > "$temporary/agent.env"
cat > "$temporary/configure-agent.sh" <<'BASH'
#!/usr/bin/env bash
set -Eeuo pipefail
cd -- "$(dirname -- "${BASH_SOURCE[0]}")"
systemctl cat vigara-agent.service >/dev/null 2>&1 || { echo 'Enroll and install the host agent first, then rerun this helper.' >&2; exit 1; }
# Copy only agent settings, never the server's database credentials or backup key.
sudo install -D -m 0600 -- agent.env /etc/vigara-agent/compose.env
sudo install -d -m 0755 /etc/systemd/system/vigara-agent.service.d
printf '[Service]\nEnvironmentFile=/etc/vigara-agent/compose.env\n' |
  sudo tee /etc/systemd/system/vigara-agent.service.d/50-compose-root.conf >/dev/null
sudo systemctl daemon-reload
sudo systemctl try-restart vigara-agent.service
echo 'Host agent Compose root configured from agent.env.'
BASH
chmod 600 "$temporary/agent.env"
chmod 700 "$temporary/configure-agent.sh"
unset password backup_key
if [[ $mode == caddy ]]; then
  # Forward generated application settings without changing the shared template.
  sed -e 's/\r$//' -e '/      VIGARA_ADDR:/i\      VIGARA_BACKUP_MASTER_KEY: ${VIGARA_BACKUP_MASTER_KEY:?Set VIGARA_BACKUP_MASTER_KEY}' \
    "$script_dir/compose.caddy.example.yml" > "$temporary/compose.yml"
else
  sed -e 's/\r$//' -e 's/env_file: .env.staging/env_file: .env/g' \
    -e 's/"${VIGARA_PORT:-8080}:8080"/"${VIGARA_BIND_ADDRESS:-127.0.0.1}:${VIGARA_PORT:-8080}:8080"/' \
    "$script_dir/compose.staging.yml" > "$temporary/compose.yml"
fi
sed -i -e "/^  app:$/a\    container_name: ${project}-app" \
  -e "/^  db:$/a\    container_name: ${project}-postgres" \
  -e "/^  postgres:$/a\    container_name: ${project}-postgres" "$temporary/compose.yml"
if [[ $storage == bind ]]; then use_bind_storage "$temporary" "$project"; fi
write_helper "$temporary" "$project"
readiness_url=${mcp_origin:-$url}
cp -- "$script_dir/check-ready.sh" "$temporary/check-ready.sh"
{
  printf '#!/usr/bin/env bash\nset -Eeuo pipefail\n'
  printf 'exec bash "$(dirname -- "${BASH_SOURCE[0]}")/check-ready.sh" %q %q\n' "$readiness_url" "$mcp_origin"
} > "$temporary/check.sh"
chmod 600 "$temporary/.env"
chmod 700 "$temporary/check.sh" "$temporary/check-ready.sh"
bash "$temporary/compose.sh" config --quiet
if [[ $proxy_action == create ]]; then
  mkdir "$temporary/proxy"
  sed 's/\r$//' "$script_dir/compose.caddy-proxy.example.yml" > "$temporary/proxy/compose.yml"
  printf 'VIGARA_CADDY_IMAGE=%s\nVIGARA_CADDY_CONTAINER=%s\nVIGARA_PROXY_NETWORK=%s\nVIGARA_DOCKER_SOCKET=%s\n' \
    "$proxy_image" "$proxy" "$network" "$socket" > "$temporary/proxy/.env"
  write_helper "$temporary/proxy" "$proxy_project"
  if [[ $proxy_storage == bind ]]; then use_bind_storage "$temporary/proxy" "$proxy_project"; fi
  if [[ $proxy_purge == true ]]; then write_proxy_purge_helper "$temporary/proxy"; fi
  bash "$temporary/proxy/compose.sh" config --quiet
  mv -T -- "$temporary/proxy" "$proxy_destination"
fi
mv -T -- "$temporary" "$destination"
trap - EXIT

printf '\nConfiguration validated and saved to %s\nProject: %s\nImage: %s\nAccess: %s\n' "$destination" "$project" "$image" "$url"
printf 'Back up .env securely: it contains the database password and backup master key.\n'
printf 'Shared Compose root: %s\n' "$stacks_root"
printf 'Persistent storage: %s\nContainer names: %s-app, %s-postgres\n' "$storage" "$project" "$project"
if [[ $storage == bind ]]; then
  if [[ $bind_root == . ]]; then data_base=$stacks_root; else data_base=$bind_root; fi
  printf 'Vigara data: %s/%s/data\n' "$data_base" "$project"
  [[ $proxy_action != create || $proxy_storage != bind ]] || printf 'Caddy data: %s/%s/data\n' "$data_base" "$proxy_project"
fi
printf 'Host agent Compose root saved in .env and agent.env. After enrolling the host agent, apply it with: bash %q/configure-agent.sh\n' "$destination"
if [[ -n $mcp_origin ]]; then
  printf 'MCP endpoint: %s/mcp\nConnection instructions: Account settings > Claude and ChatGPT.\n' "$mcp_origin"
fi
printf 'Manage this deployment with: bash %q/compose.sh ps\n' "$destination"
if ! yesno 'Pull images and start Vigara (and the approved proxy) now?'; then
  if [[ $proxy_action == create ]]; then
    printf 'Start Caddy first:\n  docker network inspect %q >/dev/null 2>&1 || docker network create %q\n  bash %q/compose.sh pull\n' "$network" "$network" "$proxy_destination"
    [[ $proxy_purge != true ]] || printf '  bash %q/purge-volumes.sh # Permanently delete the approved Caddy volumes\n' "$proxy_destination"
    printf '  bash %q/compose.sh up -d --wait --wait-timeout 120\n' "$proxy_destination"
  elif [[ $proxy_action == start ]]; then
    printf 'Start Caddy first: docker start %q\n' "$proxy"
  fi
  printf 'Start later:\n  bash %q/compose.sh pull\n' "$destination"
  [[ ! -f "$destination/prepare-storage.sh" ]] || printf '  bash %q/prepare-storage.sh\n' "$destination"
  printf '  bash %q/compose.sh up -d --wait --wait-timeout 120\n' "$destination"
  printf 'Verify readiness afterward: bash %q/check.sh\n' "$destination"
  exit 0
fi
if [[ $proxy_action == create ]]; then
  bash "$proxy_destination/compose.sh" pull || fail "Proxy image pull failed. Retry with $proxy_destination/compose.sh pull. Configuration is retained."
  if [[ $proxy_purge == true ]]; then
    bash "$proxy_destination/purge-volumes.sh" || fail 'Proxy volume purge failed. Configuration is retained; inspect the volumes before retrying.'
  fi
  docker network inspect "$network" >/dev/null 2>&1 || docker network create "$network" >/dev/null
  bash "$proxy_destination/compose.sh" up -d --wait --wait-timeout 120 || fail "Proxy startup failed. Inspect with: bash $proxy_destination/compose.sh logs --tail 100"
elif [[ $proxy_action == start ]]; then
  docker start "$proxy" >/dev/null || fail 'Could not start the existing proxy. Inspect its logs and port bindings.'
fi
bash "$destination/compose.sh" pull || fail "Image pull failed. For private GHCR images, run docker login ghcr.io, then retry with $destination/compose.sh pull."
if [[ -f "$destination/prepare-storage.sh" ]]; then
  bash "$destination/prepare-storage.sh" || fail "Could not prepare bind storage permissions. Retry with: bash $destination/prepare-storage.sh"
fi
if ! bash "$destination/compose.sh" up -d --wait --wait-timeout 120; then
  bash "$destination/compose.sh" ps
  fail "Startup failed. Inspect with: bash $destination/compose.sh logs --tail 100"
fi
bash "$destination/compose.sh" ps
if command -v curl >/dev/null; then
  bash "$destination/check.sh" || fail "Containers started, but readiness was not confirmed. Fix DNS/routing or inspect logs, then rerun: bash $destination/check.sh"
else
  printf 'curl is unavailable; readiness was not verified. Install curl, then run: bash %q/check.sh\n' "$destination"
fi
if [[ $mode == direct && $secure == true ]]; then
  printf 'Open your external HTTPS URL to sign in; secure cookies are enabled.\n'
else
  printf 'Open %s and complete first administrator setup.\n' "$url"
fi
printf 'Next: enroll the host agent before configuring Compose stack management.\n'
