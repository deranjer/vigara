# TP-Link Archer BE550 and RE653BE

## Identified setup

This support note is scoped to the user's reported network: Archer BE550
hardware v1.0 (sold under the BE9300 product branding), currently reported on
firmware 1.2.4 Build 20260402 rel.18154(5553), with an offered 1.3.1 Build
20260731 rel.12040 update that has not been confirmed installed; and RE653BE
hardware v1.0, reported on firmware 1.0.8 Build 20250624 rel.18867. The user
reports EasyMesh is enabled. These device and firmware versions are user
reported; Vigira has not connected to either device to validate them.

TP-Link's regional support pages map BE550 v1.0 to its v1.6 support track and
RE653BE v1.0 to its v1.6 support track. The pages publish product guides,
firmware and Tether app information. TP-Link's EasyMesh guidance describes
management of mesh devices and clients through the main router's web interface.
That is a documented human management path, not a documented machine telemetry
API. The reviewed public material does not specify an authenticated endpoint,
stable response schema, or versioned API for collecting router status, WAN
health, client inventory, or mesh topology on this hardware and firmware pair.

Sources: [Archer BE550 v1.60 support and downloads](https://www.tp-link.com/us/support/download/archer-be550/v1.60/),
[RE653BE support and downloads](https://www.tp-link.com/us/support/download/re653be/),
[Archer BE550 user guide](https://static.tp-link.com/upload/manual/2023/202307/20230721/1910013423_Archer%20BE550_UG_REV1.0.0.pdf),
[TP-Link EasyMesh overview](https://www.tp-link.com/us/support/faq/3529/).

## Available check

Vigara's existing opt-in network check supports bounded TCP connection checks
to literal private or local IP addresses. The
[`docs/examples/tplink-easymesh-network-checks.json`](examples/tplink-easymesh-network-checks.json)
file is a starting point for checking whether the two devices' chosen admin
ports accept TCP connections. Replace the sample addresses with the devices'
reserved LAN addresses, and replace port 443 with the port shown in the browser
address when opening each device's web interface. Enable the file with
`VIGIRA_NETWORK_CHECKS_CONFIG` as described in the agent configuration guide.

A successful result means only that a TCP connection to that IP and port
succeeded. It does not authenticate or inspect the web interface and does not
prove the router has internet access, that Wi-Fi is serving clients, that the
extender is joined to EasyMesh, or that clients have healthy connectivity. An
unreachable admin port also does not prove the device itself is down; its
management port or address may differ or be filtered.

HTTP checks are also available for a known unauthenticated URL that returns a
2xx response, but the TP-Link web login page is not such a health endpoint.
Checks do not follow redirects or send credentials. Do not point them at a
login URL and interpret its result as device health.

## Unsupported collection

This support slice does not implement or claim SNMP, SSH, ICMP, Prometheus
export, TP-Link cloud/Tether API access, web-session scraping, or a private
authenticated router API. It does not collect WAN state, traffic counters,
connected clients, radio status, firmware version, EasyMesh membership, node
backhaul, or topology. The web interface may display some of these values for
an operator, but that does not make them available through this check.

No live device request or configuration change was performed. Firmware update
availability and the installed version should be checked in the local web UI;
the offered router update is not assumed to have been applied.
