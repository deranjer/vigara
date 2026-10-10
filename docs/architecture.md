# Initial architecture

The central Go server owns inventory, configuration, policies, schedules, job state, telemetry ingestion, alerts, and the web API. Native Go agents initiate outbound HTTPS requests and begin with polling for configuration and authorized jobs. Monitoring and backup providers use separate interfaces while sharing inventory, scheduling, credentials, and events.

The first deployment is one server process and PostgreSQL. React assets are embedded into the server binary at build time. Server and agent share versioned protocol types under `internal/protocol`. Start with compiled modules rather than dynamic plugins. Restic remains independently usable for recovery.

This records the planning baseline, not completed product capabilities. See Loopira issues for decisions and milestone acceptance criteria.
