ALTER TABLE users DROP CONSTRAINT users_role_check;
ALTER TABLE users ADD CONSTRAINT users_role_check CHECK (role IN ('viewer','operator','admin','restricted'));

CREATE TABLE user_resource_grants (
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    agent_id uuid NOT NULL REFERENCES agent_identities(id) ON DELETE CASCADE,
    kind text NOT NULL CHECK (kind IN ('stack','service','container')),
    resource text NOT NULL CHECK (length(resource) BETWEEN 1 AND 128),
    service text NOT NULL DEFAULT '' CHECK (length(service) <= 128),
    logs boolean NOT NULL DEFAULT false,
    restart boolean NOT NULL DEFAULT false,
    recreate boolean NOT NULL DEFAULT false,
    pull_recreate boolean NOT NULL DEFAULT false,
    PRIMARY KEY (user_id,agent_id,kind,resource,service),
    CHECK ((kind = 'service' AND service <> '') OR (kind <> 'service' AND service = ''))
);

CREATE TABLE container_log_values (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id uuid NOT NULL REFERENCES agent_identities(id) ON DELETE CASCADE,
    kind text NOT NULL CHECK (kind IN ('service','container')),
    resource text NOT NULL,
    service text NOT NULL DEFAULT '',
    label text NOT NULL CHECK (length(label) BETWEEN 1 AND 80),
    match_text text NOT NULL CHECK (length(match_text) BETWEEN 1 AND 256),
    prefix text NOT NULL DEFAULT '',
    suffix text NOT NULL DEFAULT '',
    pattern text NOT NULL DEFAULT '',
    shared boolean NOT NULL DEFAULT false,
    value text NOT NULL DEFAULT '',
    run_id text NOT NULL DEFAULT '',
    cursor_at text NOT NULL DEFAULT '',
    captured_at text NOT NULL DEFAULT '',
    checked_at timestamptz,
    state text NOT NULL DEFAULT 'waiting',
    version bigint NOT NULL DEFAULT 1,
    UNIQUE (agent_id,kind,resource,service,label)
);
