# Retained log search and saved values

Both features require the updated API server and Docker host agent. Older agents continue to support recent logs, but search and capture display an agent-update message. They use the existing authenticated agent channel and Docker API; no host shell is involved.

## Search retained logs

Open **Containers → container → Logs → Search retained logs**. Enter literal match text and optional From/Until times in the browser's timezone. Matching occurs on the host before results are limited, so a startup message remains searchable even after thousands of newer lines have pushed it out of the recent-log viewer.

Each request scans up to 64 MiB or 15 seconds and returns up to 200 matching lines. Lines longer than 512 bytes are shortened and marked. The response distinguishes an incomplete scan from a completed scan with more matching lines than can be returned. Continue from the last scanned timestamp or narrow the time range. Timestamp pagination is inclusive, so boundary lines can repeat. If a single timestamp itself exceeds the scan budget, use a narrower source or host-level log tooling.

Search can only recover logs retained by the current container's logging driver. Deleted containers and rotated-away history are not recoverable through this feature. An empty result means no matches in the scanned history, not proof that a line never existed.

Selected-resources accounts need Read raw logs on the target. View alone permits only shared extracted values.

## Capture a value

On a container's **Overview**, choose **Saved log values → Add log value**. Administrators configure:

- A label, such as Join code.
- Literal text identifying the relevant log line, such as `JoinCode`.
- Text delimiters, such as `value[` and `]`, or an advanced Go regular expression with exactly one capture group.
- Whether to share the extracted value with users who can view that resource. New cards are administrator-only.

Preview displays the matching line and extracted value before saving. A rule can be saved without an existing match, so it can capture a future startup message. Shortened lines and values longer than 512 bytes are not accepted as captures.

The API's background worker polls independently of open browsers, performs searches on the host, and persists the extracted value and scan cursor in PostgreSQL. It stores no raw log lines. A value already captured survives log rotation and API restarts. An existing value cannot be recovered if its log history was already deleted before a rule was created.

Capture follows a Compose service across replacements, using agent, project, and service identity. A standalone rule is tied to the current container ID. If multiple replicas of a service exist, capture reports an ambiguous source instead of choosing one arbitrarily. Up to 100 rules are supported, with at most four concurrent background readers.

## Freshness and restarts

A capture belongs to a specific container ID and Docker start timestamp. Every card read checks the current run. Restarting or recreating the container hides the previous value until a complete match is found for the new run. A stale worker check, unavailable host, incomplete scan, or ambiguous source disables copying. Copy also revalidates the run, rather than trusting a card left open in an old browser tab.

The latest complete match by timestamp wins. A noisy service can require several background passes to scan retained history. Labels and sharing can be changed without discarding an already captured value; changing the extraction rule starts a new scan. Concurrent edits require the current rule version.

These checks identify Docker runs. If an application restarts its game process inside a container without restarting the container itself, the old value remains until a newer matching log entry is captured. Configure the container to restart as a unit when run-level invalidation is required.

Saved values can themselves grant access to a game or service. Share only the intended field. Granting View does not reveal the extraction pattern or raw source line to restricted accounts. Save/delete operations and resource-access changes are audited without captured values or log content.
