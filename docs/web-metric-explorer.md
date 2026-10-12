# Web metric explorer

The authenticated web app uses a Mantine AppShell with collapsible navigation, an expandable Monitoring menu, separate Devices and Backup history views, and a persisted light/dark preference.

Device history uses Mantine LineChart. Select up to five metric names; label variants remain separate lines (64 total series and 20,000 returned points maximum). Hover shows original values, units, labels and local timestamps. Mixed units automatically use independently normalized lines; this compares timing/shape, not absolute magnitude. Constant normalized series appear at 50. Single-unit series can use their real scale. Missing buckets remain gaps.

Presets retain the page position. Start/end fields are drafts until Apply interval. They use the displayed browser timezone and accept seconds/milliseconds. Drag the plot to zoom, then Reset zoom restores the pre-zoom range. Exact bounds filter raw observations. Retained hourly summaries are included only when the complete summary hour fits; sub-hour values cannot be reconstructed. Point-level resolution preserves recent minute data when mixed with older hourly summaries.

History API retains the legacy `range`/`metric` query. It additionally accepts paired RFC3339 `start`/`end` values and up to five repeated `metrics` parameters. Intervals must be positive, at most 30 days, and end no more than one minute in the future. Authentication and origin checks still apply.

Network values are stored in bytes per second (`By/s`) but displayed as B/s, kB/s, MB/s, GB/s, kb/s, Mb/s or Gb/s. Units use decimal prefixes; bit units multiply bytes by eight. CPU cores in use is explicitly an estimate from total CPU-capacity percentage times reported logical CPU count. Disk totals are shown only when reported.

`GET /api/version` reports the running server version/revision. Builds default to `dev`; release builds can set `-ldflags "-X main.version=1.2.3 -X main.revision=COMMIT"`. The sidebar checks the fixed public deranjer/vigara GitHub release endpoint with a timeout and five-minute cache, supports a manual recheck, and compares semantic versions. No release, development/uncomparable version and unavailable checks are distinct states.
