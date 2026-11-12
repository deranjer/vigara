# Notification channels

Vigira can deliver alert transitions to ntfy, Gotify and Discord. Each destination is optional; it is enabled only when its environment settings are present. A partial Gotify configuration prevents startup so delivery is not silently disabled.

## ntfy

Set the ntfy server's base URL and a topic. A token is optional and is sent as a Bearer token for servers that require access control.

```dotenv
VIGIRA_NTFY_URL=https://ntfy.example.com
VIGIRA_NTFY_TOPIC=vigara-alerts
VIGIRA_NTFY_TOKEN=<optional-access-token>
```

Critical alerts are sent at priority 5, warnings at 4, cleared alerts at 2. When `VIGIRA_PUBLIC_URL` is set, tapping a notification opens Alerts, and firing alerts carry **Acknowledge** and **Mute 24 h** buttons. Each button is a signed link for that one alert and action; it works without signing in and expires after seven days.

## Gotify

Set the base URL of a self-hosted Gotify server and an application token. The adapter appends `/message` to the base URL and sends the token in the `X-Gotify-Key` request header.

```dotenv
VIGIRA_GOTIFY_URL=http://gotify:80
VIGIRA_GOTIFY_TOKEN=<gotify-application-token>
```

Gotify supports HTTP for self-hosted installations. Use HTTPS when traffic crosses an untrusted network. The URL cannot contain credentials, a query, or a fragment.

## Discord

Set the complete incoming webhook URL. Only HTTPS URLs hosted on `discord.com` are accepted. The webhook URL contains a secret token; store it as a secret and never publish it in logs or support output.

```dotenv
VIGIRA_DISCORD_WEBHOOK_URL=https://discord.com/api/webhooks/<webhook-id>/<webhook-token>
```

Discord delivery waits for server confirmation, disables allowed mentions, and bounds message content to Discord's documented 2,000-character limit. Both adapters sanitize and Markdown-escape user-provided fields.

## Routing and quiet hours

Settings › Notifications lists the configured channels, sends a test message, and sets which alerts each channel receives (critical, warning, cleared) and the daily quiet hours. During quiet hours, warnings and cleared alerts wait until quiet hours end; critical alerts always notify. A held alert that has since cleared or refired is dropped instead of being sent late, and so is the "cleared" for a firing quiet hours held back. Alerts muted from the inbox are never sent. Only administrators can change these settings.

## Delivery behavior

The adapters use a ten-second HTTP timeout, do not follow redirects, and bound request and response sizes. A successful send requires HTTP 200 and a valid provider message ID in the JSON acknowledgement. HTTP 429 responses, server and transport failures, and malformed or oversized acknowledgements are retryable; other client errors are permanent. A bounded `Retry-After` delay of up to 24 hours is exposed to the delivery worker when available. If a provider accepts a message but its acknowledgement is lost or malformed, retrying can deliver a duplicate; delivery is at-least-once. Errors contain only the channel and a stable category, never credentials, URLs, or response bodies. The durable worker owns retry scheduling.

Provider references:

- [Gotify push messages](https://gotify.net/docs/pushmsg)
- [Gotify REST API](https://gotify.net/api-docs)
- [Discord Execute Webhook](https://discord.com/developers/docs/resources/webhook#execute-webhook)
- [Discord message allowed mentions](https://discord.com/developers/docs/resources/message#allowed-mentions-object)
