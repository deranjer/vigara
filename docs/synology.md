# Synology DS923+ monitoring and backup

This integration targets a Synology DS923+ running DSM 7.4.1-90080. DSM 7.4 documents SNMP v1, v2c, and v3 support. The Vigira agent currently uses read-only SNMP v2c and Synology's documented System and Disk MIB objects; it does not use private DSM endpoints or shell access. Synology's MIB guide documents system partition status and NAS temperature, and per-disk deployment status and temperature. Disk status is the DSM deployment state, not a SMART, RAID, storage-pool, or volume health verdict. The collector accepts system status 1 (normal) and 2 (failed), and disk statuses 1–5; it does not treat unknown values as healthy.

Enable SNMP in DSM only after restricting its access to the agent host or a trusted management network. SNMP v2c sends the read community without encryption. Use a dedicated read-only community and firewall rules; do not reuse a password. The community is supplied to the agent as a private environment variable, is not included in telemetry, and is never copied into collector errors. DSM supports SNMPv3, but this initial collector does not yet implement its credentials or privacy settings.

Set these variables in the Vigira agent's service environment to opt in:

```text
VIGIRA_SYNOLOGY_SNMP_TARGET=192.0.2.23:161
VIGIRA_SYNOLOGY_SNMP_COMMUNITY=<dedicated-read-only-community>
```

The example address is reserved documentation space. Replace it with the NAS address on the management network. Collection uses a fixed OID set, at most 32 disks per table, no retries, and a two-second overall deadline. Every NAS measurement has the `nas_target` label; disk measurements also carry the numeric `disk_index`. Metrics use fixed names and units: `synology_system_status` (1), `synology_temperature_celsius` (Cel), `synology_disk_status` (1), and `synology_disk_temperature_celsius` (Cel). A failed remote poll yields a collection error and no synthetic NAS measurements; local host telemetry continues. The agent does not identify its own host metrics as NAS metrics. A collection timeout or a truncated disk table is reported explicitly. Tests use a loopback SNMP fixture; no physical DS923+ or DSM instance was used for validation.

## Backup path

The DS923+ is suitable as a network-share endpoint for the existing Restic worker. The worker runs on a Vigira-supported Linux, Windows, or macOS agent host, where an operator can mount an SMB or NFS shared folder exposed by DSM. That mounted path can be a source or a local/network-drive Restic repository in the agent's private Restic profile. Configure the share, mount, credentials, filesystem permissions, and reconnect behavior on the agent host. Keep credentials out of job configuration and logs. A NAS-hosted repository provides convenient storage but is not an independent copy if the NAS itself is the only backup destination; preserve another copy on separate storage.

This release does not install or schedule an agent on DSM, configure DSM shares or permissions, mount shares automatically, or claim application-consistent backups for live NAS files, Docker volumes, databases, or virtual machines. No backup is started by enabling monitoring. A local share mount and an existing Restic profile are required before the agent can use a DSM share; server-managed scheduling remains subject to the deployed agent's available job delivery path.

Official references: [DSM 7.4 System Operations & Monitoring](https://www.synology.com/en-global/dsm/7.4/software_spec/system_operations_monitoring), [DSM 7.4 File Services & Protocols](https://www.synology.com/en-global/dsm/7.4/software_spec/file_services_protocols), [Synology DiskStation MIB Guide](https://global.download.synology.com/download/Document/Software/DeveloperGuide/Firmware/DSM/All/enu/Synology_DiskStation_MIB_Guide.pdf), [DS923+ product specifications](https://www.synology.com/en-us/products/DS923%2B#specs).
