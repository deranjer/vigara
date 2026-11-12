# Notification channels

Vigira can deliver alert transitions to ntfy, Gotify, Discord and phones running the Vigara Android app. The ntfy, Gotify and Discord destinations are optional; each is enabled only when its environment settings are present. The Vigara app channel needs no settings. A partial Gotify configuration prevents startup so delivery is not silently disabled.

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

## Vigara app (Android)

Phones running the Vigara app receive alerts as notifications through [UnifiedPush](https://unifiedpush.org/), so the phone needs a UnifiedPush app such as ntfy (from F-Droid or Google Play). In the app, More > Notifications turns notifications on: the app registers with the push app and sends its push endpoint and Web Push keys to the server. Anyone who can sign in can register their own phones (up to ten); Settings > Notifications on the web lists them under **Phones**, where each person can send a test or remove theirs, and administrators see everyone's.

Each alert is encrypted on the Vigara server for the phone (RFC 8291, `aes128gcm`) before it is posted to the push endpoint (RFC 8030), so the push app and its server, for example the public ntfy.sh, only relay ciphertext. Critical alerts are sent with high urgency. On the phone, alerts land in three Android notification categories (Critical alerts, Warnings, Cleared alerts) whose sound and importance can be changed in Android settings; a cleared alert replaces its firing notification, and tapping one opens Alerts.

The channel sends each alert to every registered phone and counts as delivered when at least one phone took it. A phone whose push app no longer knows the endpoint (HTTP 404 or 410) is removed. Signing out of the app, or turning notifications off, removes the phone. Push apps that require a VAPID server key are not supported yet.

The server posts to whatever HTTP or HTTPS endpoint the push app hands out, which can be a LAN address for a self-hosted ntfy. Endpoints are only accepted from signed-in users.

## Routing and quiet hours

Settings › Notifications lists the configured channels, sends a test message, and sets which alerts each channel receives (critical, warning, cleared) and the daily quiet hours. During quiet hours, warnings and cleared alerts wait until quiet hours end; critical alerts always notify. A held alert that has since cleared or refired is dropped instead of being sent late, and so is the "cleared" for a firing quiet hours held back. Alerts muted from the inbox are never sent. Only administrators can change these settings.

## Delivery behavior

The adapters use a ten-second HTTP timeout, do not follow redirects, and bound request and response sizes. A successful send requires HTTP 200 and a valid provider message ID in the JSON acknowledgement. HTTP 429 responses, server and transport failures, and malformed or oversized acknowledgements are retryable; other client errors are permanent. A bounded `Retry-After` delay of up to 24 hours is exposed to the delivery worker when available. If a provider accepts a message but its acknowledgement is lost or malformed, retrying can deliver a duplicate; delivery is at-least-once. Errors contain only the channel and a stable category, never credentials, URLs, or response bodies. The durable worker owns retry scheduling.

Provider references:

- [Gotify push messages](https://gotify.net/docs/pushmsg)
- [Gotify REST API](https://gotify.net/api-docs)
- [Discord Execute Webhook](https://discord.com/developers/docs/resources/webhook#execute-webhook)
- [Discord message allowed mentions](https://discord.com/developers/docs/resources/message#allowed-mentions-object)
