# Device inventory

Inventory devices are separate records from agent identities, discovery sources,
and IP addresses. An agent identity owns a revocable bearer credential. An
observation source records which agent or external scanner saw a device.
Addresses are time-stamped observations and never identify or merge devices.

Enrollment may send stable `identifiers` with `kind` and `value`. The server
currently accepts `machine-id` and `system-uuid`, normalizes their hexadecimal
forms, including 64-character SHA-256 digests for `machine-id`, and rejects placeholders. `system-uuid` must be a 32-hex UUID. Hostnames, display names, IP addresses, BIOS
serials, and unscoped cloud or container IDs are not reconciliation keys.
Unsupported identifier kinds are rejected. If keys in one observation already
belong to different devices, reconciliation returns a conflict and changes
nothing. Concurrent observations for the same stable key serialize and resolve
to one device.

On Linux and Windows, the agent reads `/etc/machine-id` or the Windows
`MachineGuid` registry value during enrollment, validates it, and sends only a
SHA-256 digest as a `machine-id` identifier. Other platforms or systems without
a valid local identifier omit the field. The identifier is sent only to the
server URL explicitly configured for enrollment. Discovery sources may provide
the raw 32-hex machine ID or the agent's 64-hex digest; both normalize to the
same key.

The authenticated `POST /api/devices/discovery` endpoint accepts an external
source ID, display name, optional stable identifiers, and optional address
observations. Repeated reports from the same external source resolve to its
record even when no stable identifier is available. An external source ID
cannot be reassigned to another device. Identifier provenance is retained for
each independent source that reports the same key. External address provenance
is assigned by the server from the authenticated source identity.
