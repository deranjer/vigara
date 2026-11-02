#!/usr/bin/env bash
# Verify the deployed service from this host; never bypass TLS verification.
set -Eeuo pipefail
[[ $# == 2 ]] || { echo 'Usage: check-ready.sh HEALTH_ORIGIN MCP_ORIGIN_OR_EMPTY' >&2; exit 1; }
command -v curl >/dev/null || { echo 'Install curl to verify HTTPS and MCP readiness.' >&2; exit 1; }
health_origin=$1
mcp_origin=$2
deadline=$((SECONDS + 120))

retry() {
  until "$@"; do
    (( SECONDS < deadline )) || return 1
    sleep 2
  done
}

fetch() {
  curl --fail --silent --connect-timeout 3 --max-time 10 "$1"
}

metadata() {
  local body actual
  body=$(fetch "$1") || return 1
  actual=$(printf '%s' "$body" | sed -n 's/.*"'"$2"'"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
  [[ $actual == "$3" ]]
}

challenge() {
  local status
  status=$(curl --silent --connect-timeout 3 --max-time 10 --output /dev/null --write-out '%{http_code}' "$mcp_origin/mcp") || return 1
  [[ $status == 401 ]]
}

printf 'Waiting for %s/healthz (including certificate validation for HTTPS)…\n' "$health_origin"
if ! retry fetch "$health_origin/healthz" >/dev/null; then
  printf 'Health/HTTPS check failed: %s/healthz\nCheck public DNS (including AAAA), inbound ports 80/443, certificates and app/proxy logs.\n' "$health_origin" >&2
  exit 1
fi
printf 'Health endpoint verified from this server.\n'
if [[ -n $mcp_origin ]]; then
  if ! retry metadata "$mcp_origin/.well-known/oauth-protected-resource" resource "$mcp_origin/mcp" ||
     ! retry metadata "$mcp_origin/.well-known/oauth-authorization-server" issuer "$mcp_origin" ||
     ! retry challenge; then
    printf 'MCP readiness failed. Check VIGIRA_MCP_URL, forwarded Host, OAuth discovery routes and proxy login rules.\n' >&2
    exit 1
  fi
  printf 'MCP discovery and OAuth authentication boundary verified from this server: %s/mcp\n' "$mcp_origin"
  printf 'Cloud reachability still depends on your public DNS/firewall. Connect ChatGPT or Claude to complete OAuth sign-in.\n'
fi
