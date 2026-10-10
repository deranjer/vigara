-- Keep stable inventory keys, discovery sources, and network locations apart.
CREATE TABLE device_identifiers (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    device_id UUID NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    kind TEXT NOT NULL,
    normalized_value TEXT NOT NULL,
    reported_value TEXT NOT NULL,
    provenance TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(kind, normalized_value)
);

CREATE TABLE device_sources (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    device_id UUID NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    source_type TEXT NOT NULL,
    source_id TEXT NOT NULL,
    provenance TEXT NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(source_type, source_id)
);

CREATE TABLE device_identifier_sources (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    identifier_id UUID NOT NULL REFERENCES device_identifiers(id) ON DELETE CASCADE,
    provenance TEXT NOT NULL,
    reported_value TEXT NOT NULL,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(identifier_id, provenance)
);

CREATE TABLE device_addresses (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    device_id UUID NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    address TEXT NOT NULL,
    address_type TEXT NOT NULL,
    provenance TEXT NOT NULL,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(device_id, address, address_type, provenance)
);
CREATE INDEX device_identifiers_device_idx ON device_identifiers(device_id);
CREATE INDEX device_identifier_sources_identifier_idx ON device_identifier_sources(identifier_id);
CREATE INDEX device_sources_device_idx ON device_sources(device_id);
CREATE INDEX device_addresses_device_idx ON device_addresses(device_id);
