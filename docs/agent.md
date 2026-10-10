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
