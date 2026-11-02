#!/usr/bin/env bash
# Copied into a deployment with server_url and managed_root set by setup.
set -Eeuo pipefail
umask 077
cd -- "$(dirname -- "${BASH_SOURCE[0]}")"
bash ./check.sh || { echo 'Server readiness must pass before adding this host.' >&2; exit 1; }
printf '\nAdd this server as a Docker host for deploying Compose stacks?\n'
printf 'This installs a host agent with Docker manage, exec and deploy access. Its service runs as root and can control this Docker host.\n'
printf 'Compose directory: %s\nServer connection: %s\n' "$managed_root" "$server_url"
[[ $server_url != http://* ]] || printf 'Plain HTTP is permitted only for loopback or a trusted private network.\n'
read -r -p 'Register this Docker host automatically? [y/N]: ' answer || answer=''
case "$answer" in
  y|Y|yes|YES|Yes) ;;
  *) printf 'Host registration skipped. Add it later with: bash %q/register-host.sh\n' "$PWD"; exit 0;;
esac
fail() { printf '%s\nRetry with: bash %q/register-host.sh\n' "$*" "$PWD" >&2; exit 1; }
for tool in python3 sudo systemctl docker hostname; do
  command -v "$tool" >/dev/null || fail "Install $tool before registering this host."
done
if [[ -n ${DOCKER_CONTEXT:-} || -z ${DOCKER_HOST:-} ]]; then
  endpoint=$(docker context inspect --format '{{.Endpoints.docker.Host}}')
else
  endpoint=$DOCKER_HOST
fi
[[ $endpoint == unix://* && -S ${endpoint#unix://} ]] || fail 'Host registration requires a local Docker Unix socket.'
systemctl --version >/dev/null || fail 'Host registration requires systemd.'
sudo -v || fail 'Administrator access is required to install the host agent.'
if sudo test -f /var/lib/vigara-agent/agent.json || sudo test -f /var/lib/vigira-agent/agent.json; then
  fail 'This host already has an enrolled agent. Use Device info > Reinstall agent to enable Docker deploy access and preserve its identity.'
fi
case "$(uname -m)" in
  x86_64|amd64) target=linux-amd64;;
  aarch64|arm64) target=linux-arm64;;
  *) fail 'Automatic host registration supports Linux amd64 and arm64.';;
esac
default_name=$(hostname)
read -r -p "Device name [$default_name]: " name || fail 'Input ended before host registration.'
name=${name:-$default_name}
bundle_dir=$(mktemp -d "$PWD/.host-agent.XXXXXXXX")
keep_bundle=false
trap '[[ $keep_bundle == true ]] || rm -rf -- "$bundle_dir"' EXIT
printf 'Preparing the verified host-agent bundle…\n'
bash ./compose.sh exec -T app /usr/local/bin/vigara host-agent-bundle \
  --name "$name" --target "$target" --server "$server_url" \
  --docker-host "$endpoint" --compose-root "$managed_root" > "$bundle_dir/bundle.zip" ||
  fail 'Could not prepare the host-agent bundle. Check agent release availability in Admin > Scheduled tasks and that the server image supports host registration.'
python3 - "$bundle_dir" <<'PY'
from pathlib import Path
import sys
import zipfile
directory = Path(sys.argv[1])
expected = {'vigara-agent', 'bootstrap.json', 'install.sh', 'configure-docker.py', 'vigara-agent.service'}
with zipfile.ZipFile(directory / 'bundle.zip') as archive:
    if set(archive.namelist()) != expected or len(archive.infolist()) != len(expected):
        raise SystemExit('Unexpected host-agent bundle contents.')
    archive.extractall(directory)
PY
keep_bundle=true
if ! sh "$bundle_dir/install.sh" --non-interactive; then
  printf 'Host-agent installation did not complete. The server is still running.\nThe private bundle is retained. Retry installation with:\n  sh %q/install.sh --non-interactive\n' "$bundle_dir" >&2
  exit 1
fi
keep_bundle=false
printf 'This server is now registered as a Docker host with Compose deployment enabled.\n'
