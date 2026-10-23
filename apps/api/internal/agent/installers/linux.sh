#!/bin/sh
set -eu
umask 077
BASE=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
CONFIG="$BASE/bootstrap.json"
AGENT_USER=vigara-agent
# Keep a migrated account's access to operator-managed backup/configuration
# files. Fresh installations use the canonical account and all services use
# the canonical binary, state path, and unit name.
if id -u vigira-agent >/dev/null 2>&1; then AGENT_USER=vigira-agent; fi
getent group vigara-agent >/dev/null || sudo groupadd --system vigara-agent
id -u vigara-agent >/dev/null 2>&1 || sudo useradd --system --gid vigara-agent --home-dir /var/lib/vigara-agent --shell /usr/sbin/nologin vigara-agent
sudo install -d -o "$AGENT_USER" -g "$AGENT_USER" -m 0700 /var/lib/vigara-agent
sudo systemctl stop vigara-agent.service 2>/dev/null || true
sudo install -m 0755 "$BASE/vigara-agent" /usr/local/bin/vigara-agent
LEGACY_RUNNING=0
if sudo systemctl is-active --quiet vigira-agent.service; then
  LEGACY_RUNNING=1
  sudo systemctl stop vigira-agent.service
fi
recover() {
  status=$?
  if [ "$status" -ne 0 ] && [ "$LEGACY_RUNNING" -eq 1 ]; then sudo systemctl start vigira-agent.service || true; fi
  exit "$status"
}
trap recover EXIT
if sudo test ! -f /var/lib/vigara-agent/agent.json && sudo test -f /var/lib/vigira-agent/agent.json; then
  # Copy all agent-owned state, including journals and local backup profiles.
  sudo cp -a /var/lib/vigira-agent/. /var/lib/vigara-agent/
  sudo chown -R "$AGENT_USER:$AGENT_USER" /var/lib/vigara-agent
fi
if sudo test ! -d /etc/systemd/system/vigara-agent.service.d && sudo test -d /etc/systemd/system/vigira-agent.service.d; then
  sudo cp -a /etc/systemd/system/vigira-agent.service.d /etc/systemd/system/vigara-agent.service.d
fi
REINSTALL=0
if [ -f "$CONFIG" ]; then
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
if [ "$REINSTALL" -eq 1 ] || sudo test ! -f /var/lib/vigara-agent/agent.json; then
  test -f "$CONFIG" || { echo 'No saved enrollment. Download a device bundle.' >&2; exit 1; }
  INSECURE=$(python3 -c 'import json,sys; print("1" if json.load(open(sys.argv[1])).get("insecureHttp") else "")' "$CONFIG")
  INSECURE_FLAG=""
  if [ -n "$INSECURE" ]; then
    INSECURE_FLAG="--allow-insecure-private-http"
    echo 'WARNING: plain HTTP; use only on a trusted private network or VPN.' >&2
  fi
  printf '%s\n' "$TOKEN" | sudo -u "$AGENT_USER" /usr/local/bin/vigara-agent --config /var/lib/vigara-agent/agent.json enroll --server "$SERVER" --name "$NAME" $INSECURE_FLAG
  rm -f -- "$CONFIG"
fi
sudo install -m 0644 "$BASE/vigara-agent.service" /etc/systemd/system/vigara-agent.service
if [ "$AGENT_USER" = vigira-agent ]; then
  sudo mkdir -p /etc/systemd/system/vigara-agent.service.d
  printf '[Service]\nUser=vigira-agent\nGroup=vigira-agent\n' | sudo tee /etc/systemd/system/vigara-agent.service.d/00-legacy-account.conf >/dev/null
fi
sudo systemctl daemon-reload
sudo systemctl enable --now vigara-agent.service
sudo systemctl is-active --quiet vigara-agent.service
if sudo systemctl cat vigira-agent.service >/dev/null 2>&1; then sudo systemctl disable vigira-agent.service; fi
echo 'Vigara agent is running. Existing device identity and legacy files were preserved.'
