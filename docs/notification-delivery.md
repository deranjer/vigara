# Notification delivery

`apps/api/internal/notifications` delivers alert lifecycle events through durable
per-destination acknowledgments. Call `Initialize(ctx, pool, destinations)`
during startup with a short deadline before serving requests. It persists a
first activation watermark for each stable destination ID and returns an error
if the delivery migration is missing. With no enabled destinations it does no
database work. Start `Run` after successful initialization and cancel its
context during shutdown.

```go
destinations := []notifications.Destination{
    {ID: "gotify:primary", Send: gotify.Send},
    {ID: "discord:alerts", Send: discord.Send},
}
if err := notifications.Initialize(startupCtx, pool, destinations); err != nil {
    return err
}
go notifications.Run(ctx, pool, destinations, logger)
```

Destination IDs are durable identities. Removing one from the configured list
pauses delivery and retains its queue. Re-enabling the same ID resumes pending
rows and prior acknowledgments. A new ID receives a new activation watermark,
so its first run includes events created from that activation time onward and
does not backfill historical alert rows. Each destination has independent
leases, retries, and acknowledgments. A dead-lettered event is diagnostic and
does not block later transitions for that incident.

The worker claims one event at a time per destination under a short
`FOR UPDATE SKIP LOCKED` transaction. It releases the transaction before
calling the callback, uses a lease and bounded send timeout, and acknowledges
only while it still owns the lease token. An expired lease can be reclaimed by
another process after a crash. This is at-least-once delivery: a process may
successfully send and then crash before recording the acknowledgment, so a
destination should tolerate duplicate event IDs. Firing and resolved events
for an incident are ordered per destination; a later transition waits while an
earlier one remains pending or leased.

Temporary failures and errors without a temporary classification are retried
with exponential backoff and deterministic jitter. `RetryAfter()` is honored up
to 24 hours. `Temporary() == false` marks an event dead immediately; retries
also stop after 12 attempts. Only a sanitized `ErrorCode()` or a generic
`delivery_failed` marker is stored. The logger receives no raw transport or
payload error text.

The existing authenticated `GET /api/alerts/events` endpoint remains an
independent view of alert history and does not claim or mark events globally
delivered. A success for one destination therefore does not hide an event from
other destinations or alter the legacy `alert_events.delivered_at` field.
