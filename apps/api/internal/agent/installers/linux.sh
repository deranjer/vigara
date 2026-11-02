#!/bin/sh
set -eu
umask 077
BASE=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
CONFIG="$BASE/bootstrap.json"
RESET_ENROLLMENT=0
NON_INTERACTIVE=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --reset-enrollment) RESET_ENROLLMENT=1;;
    --non-interactive) NON_INTERACTIVE=1;;
    *) echo 'Usage: sh install.sh [--reset-enrollment] [--non-interactive]' >&2; exit 1;;
  esac
  shift
done
fail() { echo "$*" >&2; exit 1; }
REINSTALL=0
if [ -f "$CONFIG" ]; then
  REINSTALL=$(python3 -c 'import json,sys; print(int(bool(json.load(open(sys.argv[1])).get("reinstall"))))' "$CONFIG")
fi
SAVED_STATE=''
if sudo test -f /var/lib/vigara-agent/agent.json; then SAVED_STATE=/var/lib/vigara-agent/agent.json
elif sudo test -f /var/lib/vigira-agent/agent.json; then SAVED_STATE=/var/lib/vigira-agent/agent.json; fi
confirm_reset() {
  [ "$NON_INTERACTIVE" -eq 0 ] || fail 'Enrollment reset requires interactive confirmation; omit --non-interactive.'
  [ "$REINSTALL" -eq 0 ] || fail 'Enrollment reset requires a fresh Add device bundle, not a Reinstall agent bundle.'
  [ -f "$CONFIG" ] || fail 'Enrollment reset requires a fresh Add device bundle with bootstrap.json.'
  python3 - "$CONFIG" <<'PY' || fail 'The reset bundle is missing enrollment settings.'
import json,sys
b=json.load(open(sys.argv[1]))
if not all(b.get(key) for key in ('token','serverUrl','name')): raise SystemExit(1)
PY
  echo 'WARNING: RESET replaces the saved identity, local settings and queued reports.'
  echo 'History and policies stay on the old server. Backup files are not deleted.'
  printf 'Type RESET to confirm enrollment reset, or press Enter to keep the saved enrollment: '
  IFS= read -r confirmation || fail 'Input ended; saved enrollment has not been changed.'
  [ "$confirmation" = RESET ]
}
if [ "$RESET_ENROLLMENT" -eq 1 ]; then
  confirm_reset || fail 'Enrollment reset cancelled; saved enrollment has not been changed.'
elif [ -n "$SAVED_STATE" ] && [ -f "$CONFIG" ] && [ "$REINSTALL" -eq 0 ]; then
  printf 'Saved enrollment server: '
  sudo python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["serverUrl"])' "$SAVED_STATE"
  printf 'Bundle enrollment server: '
  python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("serverUrl", "not supplied"))' "$CONFIG"
  if [ "$NON_INTERACTIVE" -eq 0 ] && [ -t 0 ]; then
    if confirm_reset; then RESET_ENROLLMENT=1; fi
  else
    echo 'Keeping saved enrollment. To switch servers, use a fresh Add device bundle and --reset-enrollment.'
  fi
fi
LEGACY_RUNNING=0
CURRENT_RUNNING=0
RESET_BACKUP=''
ENROLLMENT_SAVED=0
recover() {
  status=$?
  if [ "$status" -ne 0 ] && [ "$ENROLLMENT_SAVED" -eq 0 ]; then
    if [ -n "$RESET_BACKUP" ]; then
      sudo cp -p -- "$RESET_BACKUP" /var/lib/vigara-agent/agent.json || {
        echo "Restore previous enrollment manually from $RESET_BACKUP" >&2; exit "$status";
      }
      echo 'Enrollment failed; the previous enrollment has been restored.' >&2
    fi
    if [ "$CURRENT_RUNNING" -eq 1 ]; then sudo systemctl start vigara-agent.service || true
    elif [ "$LEGACY_RUNNING" -eq 1 ]; then sudo systemctl start vigira-agent.service || true; fi
  fi
  exit "$status"
}
trap recover EXIT
AGENT_USER=vigara-agent
# Keep a migrated account's access to operator-managed backup/configuration
# files. Fresh installations use the canonical account and all services use
# the canonical binary, state path, and unit name.
if id -u vigira-agent >/dev/null 2>&1; then AGENT_USER=vigira-agent; fi
if [ -f "$CONFIG" ]; then python3 "$BASE/configure-docker.py" validate "$CONFIG" "$AGENT_USER"; fi
getent group vigara-agent >/dev/null || sudo groupadd --system vigara-agent
id -u vigara-agent >/dev/null 2>&1 || sudo useradd --system --gid vigara-agent --home-dir /var/lib/vigara-agent --shell /usr/sbin/nologin vigara-agent
sudo install -d -o "$AGENT_USER" -g "$AGENT_USER" -m 0700 /var/lib/vigara-agent
if [ -f "$CONFIG" ]; then python3 "$BASE/configure-docker.py" probe "$CONFIG" "$AGENT_USER"; fi
if sudo systemctl is-active --quiet vigara-agent.service; then
  CURRENT_RUNNING=1
  sudo systemctl stop vigara-agent.service
fi
sudo install -m 0755 "$BASE/vigara-agent" /usr/local/bin/vigara-agent
if [ -f "$BASE/vigara-restic" ]; then
  sudo install -m 0755 "$BASE/vigara-restic" /usr/local/bin/vigara-restic
  sudo install -d -m 0755 /usr/local/share/licenses/vigara
  sudo install -m 0644 "$BASE/restic.LICENSE" /usr/local/share/licenses/vigara/restic.LICENSE
fi
if sudo systemctl is-active --quiet vigira-agent.service; then
  LEGACY_RUNNING=1
  sudo systemctl stop vigira-agent.service
fi
if [ "$RESET_ENROLLMENT" -eq 0 ] && sudo test ! -f /var/lib/vigara-agent/agent.json && sudo test -f /var/lib/vigira-agent/agent.json; then
  # Copy all agent-owned state, including journals and local backup profiles.
  sudo cp -a /var/lib/vigira-agent/. /var/lib/vigara-agent/
  sudo chown -R "$AGENT_USER:$AGENT_USER" /var/lib/vigara-agent
fi
if [ "$RESET_ENROLLMENT" -eq 1 ] && [ -n "$SAVED_STATE" ]; then
  RESET_BACKUP=$(sudo mktemp /var/lib/vigara-agent/agent.before-reset-XXXXXXXX.json)
  sudo cp -p -- "$SAVED_STATE" "$RESET_BACKUP"
  sudo chmod 0600 "$RESET_BACKUP"
  sudo rm -f -- /var/lib/vigara-agent/agent.json
  printf 'Previous enrollment backed up to: %s\n' "$RESET_BACKUP"
fi
if sudo test ! -d /etc/systemd/system/vigara-agent.service.d && sudo test -d /etc/systemd/system/vigira-agent.service.d; then
  sudo cp -a /etc/systemd/system/vigira-agent.service.d /etc/systemd/system/vigara-agent.service.d
fi
MANAGED=0
REINSTALL=0
if [ -f "$CONFIG" ]; then
  MANAGED=$(python3 -c 'import json,sys; print(int(bool(json.load(open(sys.argv[1])).get("managedBackups"))))' "$CONFIG")
  SERVER=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["serverUrl"])' "$CONFIG")
  NAME=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["name"])' "$CONFIG")
  TOKEN=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["token"])' "$CONFIG")
  REINSTALL=$(python3 -c 'import json,sys; print(int(bool(json.load(open(sys.argv[1])).get("reinstall"))))' "$CONFIG")
  if [ "$REINSTALL" -eq 1 ] && sudo test -f /var/lib/vigara-agent/agent.json; then
    sudo python3 - "$CONFIG" <<'PY'
import json,sys
from pathlib import Path
b=json.loads(Path(sys.argv[1]).read_text())
s=json.loads(Path('/var/lib/vigara-agent/agent.json').read_text())
if b['agentId'] != s['agentId']: raise SystemExit('Reinstall bundle belongs to a different agent; credentials unchanged.')
PY
  fi
fi
if [ -f "$CONFIG" ]; then python3 "$BASE/configure-docker.py" apply "$CONFIG" "$AGENT_USER"; fi
if [ "$REINSTALL" -eq 1 ] || sudo test ! -f /var/lib/vigara-agent/agent.json; then
  test -f "$CONFIG" || { echo 'No saved enrollment. Download a device bundle.' >&2; exit 1; }
  INSECURE=$(python3 -c 'import json,sys; print("1" if json.load(open(sys.argv[1])).get("insecureHttp") else "")' "$CONFIG")
  INSECURE_FLAG=""
  if [ -n "$INSECURE" ]; then
    INSECURE_FLAG="--allow-insecure-private-http"
    echo 'WARNING: plain HTTP; use only on a trusted private network or VPN.' >&2
  fi
  if [ "$MANAGED" -eq 1 ]; then sudo -u "$AGENT_USER" /usr/local/bin/vigara-agent --config /var/lib/vigara-agent/agent.json enable-managed-backups; fi
  printf '%s\n' "$TOKEN" | sudo -u "$AGENT_USER" /usr/local/bin/vigara-agent --config /var/lib/vigara-agent/agent.json enroll --server "$SERVER" --name "$NAME" $INSECURE_FLAG
  sudo test -f /var/lib/vigara-agent/agent.json || fail 'Enrollment did not save agent.json.'
  ENROLLMENT_SAVED=1
  rm -f -- "$CONFIG"
fi
if [ "$MANAGED" -eq 1 ]; then sudo -u "$AGENT_USER" /usr/local/bin/vigara-agent --config /var/lib/vigara-agent/agent.json enable-managed-backups; fi
sudo install -m 0644 "$BASE/vigara-agent.service" /etc/systemd/system/vigara-agent.service
if [ "$AGENT_USER" = vigira-agent ]; then
  sudo mkdir -p /etc/systemd/system/vigara-agent.service.d
  printf '[Service]\nUser=vigira-agent\nGroup=vigira-agent\n' | sudo tee /etc/systemd/system/vigara-agent.service.d/00-legacy-account.conf >/dev/null
fi
# Managed file backups need host read access and repository write access.
# Only explicitly opted-in backup installations use the root service.
if [ "$MANAGED" -eq 1 ] || sudo python3 -c 'import json; exit(not json.load(open("/var/lib/vigara-agent/agent.json")).get("managedBackups", False))'; then
  sudo mkdir -p /etc/systemd/system/vigara-agent.service.d
  printf '[Service]\nUser=root\nGroup=root\nProtectHome=false\nProtectSystem=false\n' | sudo tee /etc/systemd/system/vigara-agent.service.d/90-managed-backups.conf >/dev/null
fi
sudo systemctl daemon-reload
sudo systemctl enable --now vigara-agent.service
sudo systemctl is-active --quiet vigara-agent.service
if sudo systemctl cat vigira-agent.service >/dev/null 2>&1; then sudo systemctl disable vigira-agent.service; fi
rm -f -- "$CONFIG"
echo 'Vigara agent is running. Existing device identity and legacy files were preserved.'
