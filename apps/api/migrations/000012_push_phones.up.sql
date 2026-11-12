-- Phones running the Vigara app that receive alerts through UnifiedPush. The
-- endpoint is where the user's push distributor accepts messages; p256dh and
-- auth are the phone's Web Push keys (RFC 8291), so the distributor only ever
-- sees ciphertext.
CREATE TABLE push_phones (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 name text NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
 endpoint text NOT NULL UNIQUE CHECK (length(endpoint) BETWEEN 1 AND 2048),
 p256dh text NOT NULL CHECK (length(p256dh) BETWEEN 80 AND 100),
 auth text NOT NULL CHECK (length(auth) BETWEEN 16 AND 32),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 last_success_at timestamptz,
 last_error text
);
CREATE INDEX push_phones_user_idx ON push_phones(user_id);
