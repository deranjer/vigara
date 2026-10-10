# Device inventory

Inventory devices are separate records from agent identities, discovery sources,
and IP addresses. An agent identity owns a revocable bearer credential. An
observation source records which agent or external scanner saw a device.
Addresses are time-stamped observations and never identify or merge devices.

Enrollment may send stable `identifiers` with `kind` and `value`. The server
currently accepts `machine-id` and `system-uuid`, normalizes their hexadecimal
forms, including 64-character SHA-256 digests, and rejects placeholders. Hostnames, display names, IP addresses, BIOS
serials, and unscoped cloud or container IDs are not reconciliation keys.
Unsupported identifier kinds are rejected. If keys in one observation already
belong to different devices, reconciliation returns a conflict and changes
nothing. Concurrent observations for the same stable key serialize and resolve
to one device.

The authenticated `POST /api/devices/discovery` endpoint accepts an external
source ID, display name, optional stable identifiers, and optional address
observations. Repeated reports from the same external source resolve to its
record even when no stable identifier is available. An external source ID
cannot be reassigned to another device. Identifier provenance is retained for
each independent source that reports the same key.
