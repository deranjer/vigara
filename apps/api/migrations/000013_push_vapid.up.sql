-- The server's VAPID private key (RFC 8292), created on first start. Phones
-- register with its public key; push servers that bind endpoints to it reject
-- pushes signed by any other key.
ALTER TABLE notification_settings ADD COLUMN vapid_key bytea CHECK (vapid_key IS NULL OR length(vapid_key) = 32);
