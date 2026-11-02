#!/usr/bin/env bash
# Install only the two reviewed TCP checks on the existing .97 test agent.
set -euo pipefail

service='vigira-agent.service'
config='/etc/vigira-agent/tplink-network-checks.json'
dropin='/etc/systemd/system/vigira-agent.service.d/90-vigara-tplink-network-checks.conf'
mode="${1:-install}"
if [[ $# -gt 1 || ( "$mode" != install && "$mode" != --check ) ]]; then
  printf 'Usage: sudo bash %s [--check]\n' "$0" >&2
  exit 2
fi
if [[ "$EUID" -ne 0 ]]; then
  printf 'Run this script with sudo; it must inspect protected service configuration.\n' >&2
  exit 1
fi
for dependency in python3 systemctl mktemp install; do
  if ! command -v "$dependency" >/dev/null 2>&1; then
    printf 'Required command is unavailable: %s\n' "$dependency" >&2
    exit 1
  fi
done

# These exact bytes are also the ownership identity for an idempotent rerun.
config_content='{
  "checks": [
    {"name":"archer-be550-admin-port","type":"tcp","address":"192.168.1.1:80"},
    {"name":"re653be-admin-port","type":"tcp","address":"192.168.1.254:80"}
  ]
}
'
dropin_content='# Managed by deploy/install-tplink-checks.sh for the existing test agent.
[Service]
Environment="VIGIRA_NETWORK_CHECKS_CONFIG=/etc/vigira-agent/tplink-network-checks.json"
Environment="VIGIRA_NETWORK_CHECKS_CONFIG=/etc/vigira-agent/tplink-network-checks.json"
'

# Capture all potentially sensitive systemd/environment data inside Python.
# Only fixed diagnostic messages leave this process, never environment values.
python3 - "$service" "$config" "$dropin" "$config_content" "$dropin_content" <<'PY'
import os
from pathlib import Path
import re
import shlex
import socket
import stat
import subprocess
import sys

service, config, dropin, expected_config, expected_dropin = sys.argv[1:]
keys = {"VIGIRA_NETWORK_CHECKS_CONFIG"}

def fail(message):
    print("Preflight refused: " + message, file=sys.stderr)
    raise SystemExit(1)

def property_value(name):
    try:
        result = subprocess.run(
            ["systemctl", "show", "--value", "--property=" + name, service],
            check=True, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
            text=True, timeout=10,
        )
        return result.stdout.strip()
    except (OSError, subprocess.SubprocessError, UnicodeError):
        fail("could not inspect the existing agent service.")

def words(value):
    try:
        return shlex.split(value, comments=False, posix=True)
    except ValueError:
        fail("service environment syntax needs manual review.")

def check_assignment(assignment):
    name, separator, value = assignment.partition("=")
    if separator and name in keys and value not in ("", config):
        fail("another network-check path is already configured; preserve it and merge manually.")

def safe_existing_file(path, expected):
    candidate = Path(path)
    if not candidate.exists() and not candidate.is_symlink():
        return
    info = candidate.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o022:
        fail("a destination is not a protected root-owned regular file.")
    if info.st_size > 16384 or candidate.read_bytes() != expected.encode():
        fail("an existing destination differs from this script's exact contents; nothing was overwritten.")

try:
    if property_value("LoadState") != "loaded" or not property_value("FragmentPath"):
        fail("the existing vigira-agent.service unit was not found.")
    if property_value("ActiveState") != "active":
        fail("the existing test agent must already be active.")
    if property_value("NeedDaemonReload") == "yes":
        fail("the agent has other pending unit changes; review and reload them before running this installer.")

    # Refuse unsafe destination directories; do not change existing ownership,
    # permissions, unit files, other drop-ins, or any other check configuration.
    directories = ["/etc", "/etc/vigira-agent", "/etc/systemd", "/etc/systemd/system", str(Path(dropin).parent)]
    for directory in directories:
        candidate = Path(directory)
        if candidate.exists() or candidate.is_symlink():
            info = candidate.lstat()
            if not stat.S_ISDIR(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o022:
                fail("a destination directory is not a protected root-owned directory.")
    safe_existing_file(config, expected_config)
    safe_existing_file(dropin, expected_dropin)

    for assignment in words(property_value("Environment")):
        check_assignment(assignment)
    for assignment in words(property_value("UnsetEnvironment")):
        if assignment.split("=", 1)[0] in keys:
            fail("UnsetEnvironment would remove the reviewed check setting.")

    # EnvironmentFile values override Environment=. Inspect them without
    # displaying their contents; refuse unsupported paths/ambiguous syntax.
    sources = words(property_value("EnvironmentFiles"))
    if len(sources) % 2:
        fail("environment-file configuration needs manual review.")
    for index in range(0, len(sources), 2):
        path, options = sources[index:index + 2]
        if options not in ("(ignore_errors=yes)", "(ignore_errors=no)") or not path.startswith("/") or any(character in path for character in "%*?[]"):
            fail("environment-file configuration needs manual review.")
        source = Path(path)
        if not source.exists() and options == "(ignore_errors=yes)":
            continue
        if not source.is_file() or source.stat().st_size > 1048576:
            fail("an environment file could not be safely inspected.")
        contents = source.read_text().replace("\\\n", "")
        for line in contents.splitlines():
            match = re.match(r"^\s*(VIGIRA_NETWORK_CHECKS_CONFIG)\s*=(.*)$", line)
            if match:
                values = words(match.group(2))
                if len(values) > 1:
                    fail("a network-check environment-file value needs manual review.")
                if not values or not values[0]:
                    fail("an environment-file setting would disable the reviewed check configuration.")
                check_assignment(match.group(1) + "=" + (values[0] if values else ""))

    # PassEnvironment can import manager settings that Environment= does not
    # report. Also check the active process, covering already-loaded overrides.
    if keys.intersection(words(property_value("PassEnvironment"))):
        result = subprocess.run(["systemctl", "show-environment"], check=True,
                                stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                                text=True, timeout=10)
        for assignment in words(result.stdout):
            check_assignment(assignment)
    pid = property_value("MainPID")
    if not pid.isdecimal() or int(pid) <= 1:
        fail("the active agent process could not be inspected.")
    process_environment = Path("/proc") / pid / "environ"
    for assignment in process_environment.read_bytes().split(b"\0"):
        check_assignment(assignment.decode("utf-8", errors="strict"))

    # No file, directory, or service has been changed above. Attempt both fixed
    # literal-IP ports with a total sequential timeout of at most three seconds.
    failed = []
    for name, address in (("archer-be550-admin-port", "192.168.1.1"), ("re653be-admin-port", "192.168.1.254")):
        try:
            with socket.create_connection((address, 80), timeout=1.5):
                pass
        except OSError:
            failed.append(name)
    if failed:
        fail("bounded TCP precheck failed for " + ", ".join(failed) + "; nothing was changed.")
except (OSError, subprocess.SubprocessError, UnicodeError):
    fail("protected service configuration could not be safely inspected.")

print("Preflight passed: existing agent, compatible configuration, and both bounded TCP checks.")
PY

if [[ "$mode" == --check ]]; then
  printf 'Check only: no files or services were changed.\n'
  exit 0
fi

work="$(mktemp -d /tmp/vigara-tplink-checks.XXXXXXXX)"
cleanup() {
  rm -f -- "$work/network.json" "$work/drop-in.conf"
  rmdir -- "$work"
}
trap cleanup EXIT
printf '%s' "$config_content" > "$work/network.json"
printf '%s' "$dropin_content" > "$work/drop-in.conf"

if [[ ! -d /etc/vigira-agent ]]; then
  install -d -m 0755 /etc/vigira-agent
fi
if [[ ! -d /etc/systemd/system/vigira-agent.service.d ]]; then
  install -d -m 0755 /etc/systemd/system/vigira-agent.service.d
fi
install -o root -g root -m 0644 "$work/network.json" "$config"
install -o root -g root -m 0644 "$work/drop-in.conf" "$dropin"
if ! systemctl daemon-reload >/dev/null 2>&1; then
  printf 'Files installed, but systemd reload failed. Inspect the test agent locally.\n' >&2
  exit 1
fi
if ! systemctl restart "$service" >/dev/null 2>&1 || ! systemctl is-active --quiet "$service"; then
  printf 'Files installed, but the test agent did not restart successfully. Inspect it locally.\n' >&2
  exit 1
fi
printf 'Installed both reviewed TCP checks and restarted only vigira-agent.service.\n'
