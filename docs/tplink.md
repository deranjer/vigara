# TP-Link Archer BE550 and RE653BE

## Identified setup

This support note is scoped to the user's reported network: Archer BE550
hardware v1.0 (sold under the BE9300 product branding) at 192.168.1.1,
running firmware 1.3.1 Build 20260731 rel.12040(5553); and RE653BE hardware
v1.0 at 192.168.1.254, running firmware 1.0.8 Build 20250624 rel.18867(8583).
These versions were observed in the logged-in web interfaces on 2026-10-25.
The router shows EasyMesh enabled in Main Router mode and the extender present;
the extender status shows a wired connection. No settings were changed.

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
file is configured for the reported LAN addresses, `192.168.1.1` and
`192.168.1.254`, using the observed HTTP management port 80. Change the
port to the one shown in the browser address when opening each device's web
interface. Enable the file with
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

## Read-only reachability check

On 2026-10-25, bounded unauthenticated HTTP GETs from test host 192.168.1.97
to both root URLs returned HTTP 200, without following redirects or retaining
response bodies. Earlier attempts from the restricted local execution sandbox
could not establish connections; those failures are not evidence of device
unavailability. Run the configured checks from an agent with LAN reachability.

Read-only browser inspection found no SNMP control in the router's System /
Administration page or the extender's System Tools menu. Router System Log
offers email/local export rather than a visible remote syslog destination.
Authorize Third-Party Services controls Fing client identification and is not
a telemetry API; it was left disabled. This inspection does not prove hidden
protocols are absent. No stable authenticated API was established, so WAN,
client and mesh values shown by the UI are observations, not an implemented
automatic collector. Password, remote-management and firmware settings were
left unchanged.

## Install the reviewed checks on the test host

`deploy/install-tplink-checks.sh` prepares only the existing
`vigira-agent.service` on test host `192.168.1.97`. After copying that script to
`/home/deranjer/install-tplink-checks.sh`, run these commands in an SSH session
on that host:

```sh
sudo bash /home/deranjer/install-tplink-checks.sh --check
sudo bash /home/deranjer/install-tplink-checks.sh
```

The first command performs preflight without writing files or restarting a
service. Both commands require root so protected service configuration can be
checked without displaying environment values or credentials. The script
requires an already active agent and successful TCP connections to both
`192.168.1.1:80` and `192.168.1.254:80`, with a 1.5-second timeout per attempt,
before making changes. Existing conflicting check paths, different destination
contents, unsafe destination files/directories, or environment overrides stop
the installation for manual review.

Installation writes only the exact two-check configuration to
`/etc/vigira-agent/tplink-network-checks.json` and the systemd drop-in
`/etc/systemd/system/vigira-agent.service.d/90-vigara-tplink-network-checks.conf`,
both root-owned with mode `0644`. The drop-in sets
`VIGIRA_NETWORK_CHECKS_CONFIG` and its `VIGARA_NETWORK_CHECKS_CONFIG` alias to
that file. It preserves other service configuration and checks, reloads
systemd, and restarts only `vigira-agent.service`. Exact matching files are
accepted on subsequent runs. The script embeds the reviewed configuration;
it does not load an arbitrary configuration supplied beside it. The agent's
default check timeout is 1.5 seconds and its minimum network-check collection
interval is 15 seconds. These checks establish admin-port reachability only;
they do not log in to either device or change router settings.
