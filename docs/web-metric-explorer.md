# Web metric explorer

The authenticated web app uses a Mantine AppShell with collapsible navigation, separate top-level Devices, Monitoring, and Backup history views, an avatar/name/logout footer above server release information, and a persisted light/dark preference.

Device history uses Mantine LineChart. Select up to five metric names; label variants remain separate lines (64 total series and 20,000 returned points maximum). Hover shows original values, units, labels and local timestamps. Mixed units automatically use independently normalized lines; this compares timing/shape, not absolute magnitude. Constant normalized series appear at 50. Single-unit series can use their real scale. Missing buckets remain gaps.

Presets retain the page position. Start/end fields are drafts until Apply interval. They use the displayed browser timezone and accept seconds/milliseconds. Drag the plot to zoom, then Reset zoom restores the pre-zoom range. Exact bounds filter raw observations. Retained hourly summaries are included only when the complete summary hour fits; sub-hour values cannot be reconstructed. Point-level resolution preserves recent minute data when mixed with older hourly summaries.

History API retains the legacy `range`/`metric` query. It additionally accepts paired RFC3339 `start`/`end` values and up to five repeated `metrics` parameters. Intervals must be positive, at most 30 days, and end no more than one minute in the future. Authentication and origin checks still apply.

Network values are stored in bytes per second (`By/s`) but displayed as B/s, kB/s, MB/s, GB/s, kb/s, Mb/s or Gb/s. Units use decimal prefixes; bit units multiply bytes by eight. CPU cores in use is explicitly an estimate from total CPU-capacity percentage times reported logical CPU count. Disk totals are shown only when reported.

`GET /api/version` reports the running server version/revision. Builds default to `dev`; release builds can set `-ldflags "-X main.version=1.2.3 -X main.revision=COMMIT"`. The sidebar checks the fixed public deranjer/vigara GitHub release endpoint with a timeout and five-minute cache, supports a manual recheck, and compares semantic versions. No release, development/uncomparable version and unavailable checks are distinct states.

Devices lists inventory and basic CPU, memory, and disk stats. Clicking a row, its View device button, or pressing Enter/Space on a focused row opens the device-centric page. It defaults to Device info; Monitoring, Backups, and Device settings are separate tabs. Existing Docker inventory appears in a Containers tab only when containers are reported. Compose stack grouping awaits API integration (ENG-176/ENG-181); it is not inferred from container names.

Device info shows reported hardware/system data and observation time. Deeper GPU, memory module, motherboard and firmware collection is tracked separately in ENG-192. Device settings reads and saves the existing expected-awake schedule (weekdays, start/end and IANA timezone), and lists current/upcoming maintenance windows. Disabling the schedule means always expected awake. Backup policies are paginated and filtered by device ID before job histories are requested.

The global Monitoring dashboard refreshes every 30 seconds, showing totals across all inventory pages, device health issues and firing rule-based incidents from the latest 500 incident records. Incident failures are shown explicitly. Alerts link to the appropriate device's Monitoring tab. Status/collection issues remain distinct from rule-based alerts; schedules can explain stale check-ins. The table below offers per-device CPU/memory/network drilldown. Device enrollment and additional settings remain future work.
