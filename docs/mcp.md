# Claude and ChatGPT connections

Vigara includes an optional remote MCP server. Claude, ChatGPT, and other MCP
clients use the same endpoint and sign in with an existing local Vigara account.
No Google account, external identity provider, or OpenAI API key is required.

## Enable the server

For a new deployment, `bash deploy/setup.sh` offers MCP as an opt-in step. With
Caddy it uses the HTTPS hostname you selected. The script also asks for a shared
Compose root; its generated files live in `<compose-root>/<project>/`, outside
the source repository.

The installer can also deploy Caddy Docker Proxy with your approval when the
named proxy is missing, or start an existing stopped proxy. It starts the proxy
before Vigara, then verifies HTTPS and OAuth discovery. Configure public DNS and
forward TCP 80/443 to the server first. If verification fails, fix routing or TLS
and run the generated `check.sh`; do not rerun initial setup or replace credentials.

Open **Account settings → Claude and ChatGPT** for setup guidance even when MCP
is disabled. When enabled, the page displays the configured server URL with a
copy button and separate ChatGPT and Claude instructions. An enabled status
confirms configuration, not reachability from either provider.

Set these variables on the **API server**, then restart it:

```dotenv
VIGARA_MCP_URL=https://vigara.example.com
VIGARA_COOKIE_SECURE=true
```

`VIGARA_MCP_URL` is the externally reachable **origin**, without `/mcp` or another
path. Leaving it unset disables the MCP and OAuth routes. The original
`VIGIRA_MCP_URL` spelling remains an alias. Database migrations run automatically
at startup. Building the API now requires Go 1.25 or newer.

For an existing Caddy deployment, adding a value to `.env` is insufficient unless
the `app` service passes it into the container. The current Caddy example includes:

```yaml
services:
  app:
    environment:
      VIGARA_MCP_URL: ${VIGARA_MCP_URL:-}
```

Keep your existing database and credentials. Edit the existing deployment files,
then use its original Compose invocation or helper to recreate the API service:

```sh
bash ~/dockers/compose-stacks/vigara/compose.sh up -d --wait app
```

Substitute your actual deployment directory. The staging template already passes
application settings through its `env_file`. Reload Account settings after the
server restarts. To disable MCP, clear `VIGARA_MCP_URL` and recreate the API service.

Use a trusted HTTPS reverse proxy and preserve the original `Host` header.
Forward `/mcp`, `/oauth/*`, and `/.well-known/oauth-*` to the API without an
additional browser-only login challenge. Vigara supplies its own authentication.
The configured origin determines OAuth discovery and token audience; forwarded
headers cannot change it. HTTP is accepted only for loopback development origins
such as `http://localhost:8080`.

Claude's remote connectors connect from Anthropic's cloud. ChatGPT also needs a
network path from its infrastructure to the endpoint. A LAN-only hostname or VPN
connection on your laptop does not make it reachable by either service. Expose
the HTTPS endpoint through an appropriate reverse proxy or tunnel before connecting.

The same OAuth endpoint supports web and desktop MCP clients using public-client
authorization code flow with S256 PKCE. Registered callbacks can use HTTPS or
HTTP on localhost/127.0.0.1/::1; other HTTP callbacks and custom URI schemes are
not supported. Callback matching is exact, including the port. Desktop clients
must register their actual callback and support DCR or a pre-registered public
client ID. This is remote Streamable HTTP MCP, not a local stdio server.
Cloud-backed web and desktop apps both need a cloud-reachable server. Direct
cross-origin browser JavaScript requests are not supported; web integrations
should connect through their MCP backend.

## Connect ChatGPT

1. Open ChatGPT's custom MCP app/plugin setup (developer mode or workspace access
   may be required by your account).
2. Enter `https://vigara.example.com/mcp` as the server URL.
3. Choose OAuth. Select **dynamic client registration / DCR** when a client
   registration choice is offered. Leave client ID and secret blank for automatic
   registration. This server uses public clients with PKCE, not client secrets.
4. On Vigara's consent page, check the application name and return address, enter
   your local Vigara credentials, and approve the requested access.
5. Enable the resulting connection in your conversation.

If you need to reuse a pre-registered public client, register it once at
`POST /oauth/register` with its exact HTTPS callback address and use the returned
`client_id`. Do not configure client-secret authentication.

## Connect Claude

1. Open Claude's custom connector settings and add a remote connector.
2. Enter `https://vigara.example.com/mcp`.
3. Choose OAuth/sign-in and **Register automatically**, rather than the published
   client identity option. This server advertises DCR; it does not implement CIMD.
4. Sign in on the Vigara consent page and approve the connection.

Client interface names can change. The discovery documents are the source of truth:

- `/.well-known/oauth-protected-resource`
- `/.well-known/oauth-protected-resource/mcp`
- `/.well-known/oauth-authorization-server`

## Available tools

| Tool | Scope | Result |
| --- | --- | --- |
| `get_profile` | Any authenticated connection | Stable account ID and display name |
| `list_devices` | `devices.read` | Devices, latest agent, heartbeat, and monitoring status |
| `list_containers` | `containers.read` | Latest collected names, images, and states; no environment/configuration secrets |
| `list_backup_status` | `backups.read` | Latest report per device and policy, including failures |
| `list_backup_policies` | `backups.read` | Backup schedules, retention, source paths and destinations; no credentials |
| `list_alerts` | `alerts.read` | Recent firing and resolved incidents |
| `inspect_container`, `list_docker_resources`, `inspect_docker_resource` | `containers.read` | Live container and Docker resource details; container environment secrets remain masked |
| `container_action` | `containers.manage` | Start, stop, restart, pause, unpause, kill or remove a container |
| `recreate_container`, `pull_docker_image` | `containers.manage` | Recreate a container or pull an image; returns progress when complete |
| `remove_docker_resource`, `prune_docker_resources` | `containers.manage` | Manage images, networks and volumes; prune supports dry-run previews |
| `list_stacks`, `get_stack`, `get_stack_history`, `get_stack_setup` | `stacks.deploy` | Compose source, your draft, revisions and deployment targets |
| `create_stack`, `update_stack_draft`, `discard_stack_draft`, `save_stack` | `stacks.deploy` | Create and edit stacks with draft version checks and Git validation |
| `preview_stack_deployment`, `deploy_stack`, `list_stack_deployments`, `reconcile_stack_deployment` | `stacks.deploy` | Preview exact revisions, queue deployment and check outcomes |
| `edit_stack_environment` | `stacks.deploy` | Encrypt an environment input for use in a draft |
| `get_stack_settings`, `update_stack_settings` | `stacks.deploy` | Read or update Git repository configuration with version checks |
| `get_stack_placements`, `save_stack_placements`, `bind_stack_profile` | `stacks.deploy` | Manage profiles and assignments with existing fencing checks |

The monitoring and backup list tools accept `limit` (1–100; default 50) and `offset` (0–1000000), and return
`items`, `limit`, `offset`, and `hasMore`. Device and container observations can be
stale: use the returned timestamps when interpreting results. Backup status covers
reports received by Vigara, not an independent verification of repository contents.

Account roles are reloaded on every MCP request, and a
tool requires both its connection scope and the corresponding current account
permission. Viewers retain read access. Operators can manage Docker containers
and resources; administrators can also manage Compose stacks. Requests without
an explicit scope ask for all six supported scopes, and consent lists the write
capabilities explicitly. Clients can request only read scopes for monitoring.
Existing grants do not gain write scopes automatically: disconnect and reconnect
to approve `containers.manage` and `stacks.deploy`. Refresh cannot expand scopes.

Stack tools accept `stackId` when targeting an existing stack, and write payloads
under `body` as documented in each tool's schema. The normal workflow is
`get_stack` → `update_stack_draft` with the exact draft version → `save_stack` →
`preview_stack_deployment` with the saved commit → `deploy_stack` with the exact
preview token and immutable image map. Poll `list_stack_deployments` afterward;
queueing a job does not prove deployment succeeded. Git settings require the
current settings version; placements require the current placement commit.
Encrypted environment edits return ciphertext and manifest contents to include
in a subsequent draft update. Plaintext dotenv files remain prohibited.

Docker tools require `agentId`; container tools also require `containerId`.
Resource tools select `kind` from `images`, `networks`, or `volumes`. For example,
stopping a container uses `container_action` with
`{"agentId":"<uuid>","containerId":"web","body":{"action":"stop"}}`.
Agent-side Docker tier and deployment capability checks still apply. Image pulls
and recreation can take several minutes; cancellation or an incomplete response
requires checking current state before retrying.

There are no backup write scopes or backup mutation tools: MCP cannot change
backup policies, schedules or retention, or request backup deletion. Access to
backup settings is read-only. The management bridge uses a fixed operation
allowlist, never a client-selected URL or HTTP method. User/account management,
host enrollment/removal, alert configuration, interactive terminals and arbitrary
shell execution are not exposed by this tool set. Container and Compose control
can affect workloads and their mounted data; read-only backup APIs do not provide
filesystem isolation from those workload permissions.

## Disconnect and expiration

Open **Account settings → Connected applications**, or visit
`https://vigara.example.com/oauth/connections` after signing into the web UI.
Disconnecting immediately invalidates that connection's access and refresh tokens.
Only the owning account can disconnect a connection through this page.

Connections last up to 30 days. Access tokens expire after at most 15 minutes;
refreshing rotates both access and refresh credentials. Reuse of an old refresh
token revokes the whole connection. A password change or role demotion also
revokes connections through their dedicated account sessions. Reconnect after
expiration, revocation, or a password change. Each consent requires local
credentials, even if the browser is already signed into the web UI.

Tokens and authorization codes are stored only as SHA-256 hashes in PostgreSQL.
Authorization codes expire after five minutes and require S256 PKCE, the exact
registered callback, and the same client. Consent forms expire after ten minutes
and are signed, bound to their target, and protected against cross-site requests.
OAuth connections never accept web-session cookies or agent credentials as MCP
bearer tokens. Consent and user-initiated disconnect events appear in the audit log.

OAuth authorize, register, token, and revoke requests share the login attempt
limiter: five POSTs per connection IP per minute, 30 globally per minute, and two
concurrent requests. Forwarding headers do not affect the peer identity. Clients
behind the same proxy share its allowance; HTTP 429 includes `Retry-After`.
Unauthenticated registration is capped at 512 clients. Client IDs persist across
restarts and do not expire automatically. If the quota is reached, the operator
can remove unused registrations from `mcp_oauth_clients`; deleting a registration
also revokes its codes and grants. Keep active registrations for client reuse.

## Validation

```sh
go test ./apps/api/internal/mcpserver ./apps/api/internal/auth ./apps/api/migrations
```

Set `VIGIRA_TEST_DATABASE_URL` to a disposable PostgreSQL database to include the
OAuth integration tests. Each test uses and removes a separate schema. These
tests cover PKCE, client binding, code replay, refresh rotation/replay, scoped
tools, restart persistence, revocation, password changes, and concurrent exchange.

Protocol references: [MCP authorization](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization),
[OpenAI authentication](https://developers.openai.com/plugins/build/auth), and
[Claude remote connectors](https://support.claude.com/en/articles/11175166-get-started-with-custom-connectors-using-remote-mcp).
