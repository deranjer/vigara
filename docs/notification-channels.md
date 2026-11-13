# Notification channels

Vigira can deliver alert transitions to ntfy, Gotify, Discord and phones running the Vigara Android app. The Vigara app channel is always present and needs no settings. Administrators add ntfy, Gotify and Discord channels under Settings > Notifications, any number of each (up to 20 in all); **Send test** tries the settings before they are saved.

## Master key

Channel tokens and webhook URLs are stored encrypted (AES-256-GCM) with a key the server reads from its environment. Without it, channels cannot be added or edited, and the page says so.

```dotenv
VIGIRA_NOTIFICATION_MASTER_KEY=<output of: openssl rand -base64 32>
```

Keep the key with your other server secrets and back it up: a database restored without the same key keeps its channels, but they cannot send until each one is edited and its token entered again.

## Channels from environment variables

Earlier versions configured channels with the environment variables below. On the first start with a master key, each configured channel is copied into Settings > Notifications, keeping its delivery history and routing, and from then on its variables are ignored (the log says so; remove them). A channel that was imported and later removed in Settings is not imported again. Without a master key these channels keep working as before and are shown read-only. A partial configuration still prevents startup so delivery is not silently disabled.

| Channel | Variables |
| --- | --- |
| ntfy | `VIGIRA_NTFY_URL`, `VIGIRA_NTFY_TOPIC`, optional `VIGIRA_NTFY_TOKEN` |
| Gotify | `VIGIRA_GOTIFY_URL`, `VIGIRA_GOTIFY_TOKEN` |
| Discord | `VIGIRA_DISCORD_WEBHOOK_URL` |

## ntfy

Enter the ntfy server's base URL and a topic. A token is optional and is sent as a Bearer token for servers that require access control. On the public ntfy.sh anyone who knows a topic can read it, so pick a name that is hard to guess.

Critical alerts are sent at priority 5, warnings at 4, cleared alerts at 2. When `VIGIRA_PUBLIC_URL` is set, tapping a notification opens Alerts, and firing alerts carry **Acknowledge** and **Mute 24 h** buttons. Each button is a signed link for that one alert and action; it works without signing in and expires after seven days.

## Gotify

Enter the base URL of a self-hosted Gotify server, such as `http://gotify:80`, and an application token. The adapter appends `/message` to the base URL and sends the token in the `X-Gotify-Key` request header.

Gotify supports HTTP for self-hosted installations. Use HTTPS when traffic crosses an untrusted network. The URL cannot contain credentials, a query, or a fragment.

## Discord

Paste the complete incoming webhook URL (`https://discord.com/api/webhooks/<webhook-id>/<webhook-token>`). Only HTTPS URLs hosted on `discord.com` are accepted. The webhook URL contains a secret token, so it is stored encrypted like other tokens and never shown again after saving.

Discord delivery waits for server confirmation, disables allowed mentions, and bounds message content to Discord's documented 2,000-character limit. Both adapters sanitize and Markdown-escape user-provided fields.

## Vigara app (Android)

Phones running the Vigara app receive alerts as notifications through [UnifiedPush](https://unifiedpush.org/), so the phone needs a UnifiedPush app such as ntfy (from F-Droid or Google Play). In the app, More > Notifications turns notifications on: the app registers with the push app and sends its push endpoint and Web Push keys to the server. Anyone who can sign in can register their own phones (up to ten); Settings > Notifications on the web lists them under **Phones**, where each person can send a test or remove theirs, and administrators see everyone's.

Each alert is encrypted on the Vigara server for the phone (RFC 8291, `aes128gcm`) before it is posted to the push endpoint (RFC 8030), so the push app and its server, for example the public ntfy.sh, only relay ciphertext. Critical alerts are sent with high urgency. On the phone, alerts land in three Android notification categories (Critical alerts, Warnings, Cleared alerts) whose sound and importance can be changed in Android settings; a cleared alert replaces its firing notification, and tapping one opens Alerts.

The channel sends each alert to every registered phone and counts as delivered when at least one phone took it. A phone whose push app no longer knows the endpoint (HTTP 404 or 410) is removed. Signing out of the app, or turning notifications off, removes the phone. Pushes are signed with the server's VAPID key (RFC 8292), which Vigara creates on first start and keeps in the database; the app registers with its public key, so push apps that require one work and push servers that bind endpoints to it accept only this server's pushes. The contact sent with the key is `VIGIRA_VAPID_SUBJECT` (a `mailto:` address or `https:` URL) when set, else `VIGIRA_PUBLIC_URL` when it is `https:`, else `mailto:vigara@localhost`.

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
