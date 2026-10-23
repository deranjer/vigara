# Claude and ChatGPT connections

Vigara includes an optional remote MCP server. Claude, ChatGPT, and other MCP
clients use the same endpoint and sign in with an existing local Vigara account.
No Google account, external identity provider, or OpenAI API key is required.

## Enable the server

Set these variables on the **API server**, then restart it:

```dotenv
VIGARA_MCP_URL=https://vigara.example.com
VIGARA_COOKIE_SECURE=true
```

`VIGARA_MCP_URL` is the externally reachable **origin**, without `/mcp` or another
path. Leaving it unset disables the MCP and OAuth routes. The original
`VIGIRA_MCP_URL` spelling remains an alias. Database migrations run automatically
at startup. Building the API now requires Go 1.25 or newer.

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
| `list_alerts` | `alerts.read` | Recent firing and resolved incidents |

List tools accept `limit` (1–100; default 50) and `offset` (0–1000000), and return
`items`, `limit`, `offset`, and `hasMore`. Device and container observations can be
stale: use the returned timestamps when interpreting results. Backup status covers
reports received by Vigara, not an independent verification of repository contents.

All tools are read-only. Account roles are reloaded on every MCP request, and a
tool requires both its connection scope and the corresponding current account
permission. Viewer, operator, and administrator roles currently have these read
permissions. Requests without an explicit scope ask for all four read scopes.
The consent page lists the requested information. Tools do not accept a user ID
or arbitrary SQL, paths, URLs, shell commands, or container actions.

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
