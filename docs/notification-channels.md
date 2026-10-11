# Notification channels

Vigira can deliver alert transitions to Gotify and Discord. Each destination is optional; it is enabled only when its environment settings are present. A partial Gotify configuration prevents startup so delivery is not silently disabled.

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

## Delivery behavior

The adapters use a ten-second HTTP timeout, do not follow redirects, and bound request and response sizes. A successful send requires HTTP 200 and a valid provider message ID in the JSON acknowledgement. HTTP 429 responses, server and transport failures, and malformed or oversized acknowledgements are retryable; other client errors are permanent. A bounded `Retry-After` delay of up to 24 hours is exposed to the delivery worker when available. If a provider accepts a message but its acknowledgement is lost or malformed, retrying can deliver a duplicate; delivery is at-least-once. Errors contain only the channel and a stable category, never credentials, URLs, or response bodies. The durable worker owns retry scheduling.

Provider references:

- [Gotify push messages](https://gotify.net/docs/pushmsg)
- [Gotify REST API](https://gotify.net/api-docs)
- [Discord Execute Webhook](https://discord.com/developers/docs/resources/webhook#execute-webhook)
- [Discord message allowed mentions](https://discord.com/developers/docs/resources/message#allowed-mentions-object)
