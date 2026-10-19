# Agent enrollment and services

The API creates one-use enrollment tokens that are limited to a set of module
capabilities. Sign in as an administrator, then create a token with the
authenticated `POST /api/agents/enrollment-tokens` endpoint:

```json
{"capabilities":[],"expiresInHours":24}
```

The response includes the token once and its expiry. The current agent build
advertises no module capabilities, so use an empty list. Keep the token private
and use it once. Enrollment creates a distinct device record and agent
identity; the agent identity receives a unique random credential, stored by the
server only as a hash. Revoke an identity with the authenticated
`POST /api/agents/{agentID}/revoke` endpoint. Revocation takes effect on the
next request.

Enrollment is interactive so the one-time token does not appear in process
arguments. The agent asks for it on standard input and saves its returned
credential in its private state file:

```sh
vigira-agent enroll --server https://vigira.example --name "Kitchen PC"
```

The agent accepts HTTPS server URLs. HTTP is accepted only for localhost and
loopback development. Redirects are never followed, which prevents a server
redirect from forwarding the bearer credential to another endpoint. The agent
polls outbound, reconnects with bounded backoff and jitter, saves complete
configuration snapshots atomically, and retains up to 500 job reports while
offline. It rejects requests to exceed the capabilities allowed by its
enrollment token.

## Linux systemd

Create the service account and state directory, enroll while running as that
account, install the supplied unit, and enable it:

```sh
sudo useradd --system --home /var/lib/vigira-agent --shell /usr/sbin/nologin vigira-agent
sudo install -d -o vigira-agent -g vigira-agent -m 0700 /var/lib/vigira-agent
sudo -u vigira-agent /usr/local/bin/vigira-agent --config /var/lib/vigira-agent/agent.json enroll --server https://vigira.example --name "Kitchen PC"
sudo install -m 0644 deploy/systemd/vigira-agent.service /etc/systemd/system/vigira-agent.service
sudo systemctl daemon-reload
sudo systemctl enable --now vigira-agent
```

The unit runs with a dedicated account, a private state directory, a read-only
system view, and no new privileges. Review the permissions needed by the
modules you choose to deploy before granting them on a host.

## Windows service

Run enrollment from an elevated terminal, enter the one-time token at the
prompt, then register the service:

```powershell
New-Item -ItemType Directory -Force 'C:\Program Files\Vigira'
& 'C:\Program Files\Vigira\vigira-agent.exe' --config 'C:\ProgramData\Vigira\agent.json' enroll --server https://vigira.example --name 'Kitchen PC'
sc.exe create VigiraAgent binPath= '"C:\Program Files\Vigira\vigira-agent.exe" --config "C:\ProgramData\Vigira\agent.json" run' start= auto
sc.exe start VigiraAgent
```

The state directory and file use a protected Windows ACL for the enrolling
account, local administrators, and LocalSystem, so the service can read the
credential while ordinary users cannot. The service handles stop and shutdown
signals and reconnects after transient network failures.

The current agent build advertises compiled module capabilities that are
enabled by valid local operator configuration; a capability report does not
grant execution by itself. A job still needs enabled desired configuration and
an explicit scoped permission grant. The agent refuses jobs it cannot support.
When choosing a custom `--config` path, place it in a dedicated private
directory that the service account can access; the agent secures newly created
state directories and always protects the credential file itself.

## Downloading configured client bundles

The Devices page offers Windows x64, Linux x64, and Linux ARM64 bundles when
the server has verified release artifacts. Build them from the repository root
and copy the output directory to a private, operator-managed location readable
by the API process:

```sh
python scripts/package_agent_releases.py 0.1.0 ./agent-releases
```

Set `VIGIRA_AGENT_RELEASE_DIR` to that directory and `VIGIRA_PUBLIC_URL` to the
reachable HTTPS base URL of this Vigira instance (for example
`https://vigira.example`). The directory must contain the generated
`manifest.json` and fixed artifact filenames. The API verifies each SHA-256
digest on availability checks and downloads. Missing or invalid artifacts are
shown as unavailable; the server does not build binaries during downloads.

### Server addresses and plain HTTP

Bundles point agents at a server URL chosen from addresses the operator configures:

- `VIGIRA_PUBLIC_URL` is the default address.
- `VIGIRA_PUBLIC_URLS` is an optional comma-separated list of alternates, for example a LAN address and a Tailscale address. When more than one address is configured, **Add device** lets the administrator pick which one the new device should use. A request can only select a configured address; it cannot supply its own.

HTTPS addresses are always accepted. Plain HTTP is accepted only when you set `VIGIRA_ALLOW_INSECURE_AGENT_HTTP=true` **and** the host is a private-network address: loopback, RFC 1918 (`10/8`, `172.16/12`, `192.168/16`), IPv6 unique-local or link-local, the Tailscale/CGNAT range `100.64.0.0/10`, a `.local` or `.ts.net` name, or a single-label name such as `nas`. A public IP or dotted public hostname must use HTTPS even with the flag on, and entries that break the rule are ignored with a startup warning.

Plain HTTP means the agent's credential, telemetry, and inventory cross the network unencrypted, so anyone who can observe or alter that traffic can impersonate the agent or read its data. Use it only on a network you trust or over a VPN such as Tailscale (whose traffic is already encrypted), and never across the internet. The Add device dialog shows a warning whenever the selected address is not encrypted, and the installers repeat it. Prefer HTTPS where practical: for example put Caddy in front with `tls internal`, or use `tailscale serve` for a real certificate on a `*.ts.net` name.

A bundle for an HTTP address passes `--allow-insecure-private-http` to `enroll`, which stores that choice in the agent's private state file so later polls keep working. To enroll by hand, add the same flag:

```sh
vigira-agent --config /var/lib/vigira-agent/agent.json enroll --server http://192.168.1.97:8080 --name "Kitchen PC" --allow-insecure-private-http
```

Without the flag the agent still accepts HTTP only for `localhost` and loopback addresses.

Authenticated bundle creation issues a single-use enrollment token that expires
after 30 minutes. The archive contains a bootstrap file, so handle it like a
secret. Run its installer on the target device; after successful enrollment the
installer removes the extracted bootstrap file and the agent stores its
per-device credential in protected state. Canceling a waiting enrollment
revokes its token. Feature selection limits server-side capabilities; it does
not install or enable local modules.
