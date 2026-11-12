import type { Device } from './device-types'

export type TelemetryDelivery = {
 lastAttemptAt?: string | null; lastStatusCode?: number | null; retryAt?: string | null; lastAcceptedAt?: string | null
 queueReportedAt?: string | null; queuedSnapshots?: number | null; oldestQueuedAt?: string | null; newestQueuedAt?: string | null; limitPerMinute: number
}
export function recent(value: string | null | undefined, now = Date.now()) {
 const at = value ? Date.parse(value) : NaN
 return Number.isFinite(at) && at <= now && now - at <= 300_000
}
export function staleReason(device: Device, now = Date.now()) {
 if (!recent(device.lastSeenAt, now)) return 'Check-in overdue'
 const observed = device.telemetryObservedAt || device.latest?.observedAt
 if (!observed) return 'Waiting for monitoring data'
 if (Date.parse(observed) > now) return 'Monitoring timestamp is in the future'
 return 'Monitoring data delayed'
}
export function deliverySummary(device: Device, now = Date.now()) {
 const delivery = device.telemetryDelivery
 const queueFresh = recent(delivery?.queueReportedAt, now)
 const queued = queueFresh ? delivery?.queuedSnapshots : null
 const attemptFresh = recent(delivery?.lastAttemptAt, now)
 const code = delivery?.lastStatusCode
 if (!recent(device.lastSeenAt, now)) return { title: 'Delivery status out of date', color: 'gray', queued, message: 'The device has not checked in recently. Delivery details below are the last known values.' }
 if (attemptFresh && code === 429) return { title: 'Uploads rate limited', color: 'yellow', queued, message: 'The device is checking in, but monitoring uploads are being throttled. Queued snapshots retry automatically.' }
 if (attemptFresh && code && code >= 400) return { title: code >= 500 ? 'Monitoring uploads failing' : 'Monitoring uploads rejected', color: 'red', queued, message: 'The device is checking in, but the server is not accepting its monitoring uploads. Check the agent logs for details.' }
 if (queued != null && queued > 0) return { title: 'Catching up', color: 'blue', queued, message: 'Older monitoring snapshots are uploading first. The displayed measurements can remain delayed until fresh snapshots arrive.' }
 if (device.status === 'stale') return { title: staleReason(device, now), color: 'yellow', queued, message: 'A recent check-in does not guarantee recent monitoring measurements. Check the observation and upload times below.' }
 if (queued === 0 && attemptFresh && code && code < 300) return { title: 'Monitoring uploads up to date', color: 'green', queued, message: 'The server accepted the latest upload and the device reports no queued snapshots.' }
 return { title: 'Delivery details unavailable', color: 'gray', queued, message: 'No recent delivery report is available. Older agents do not report their local backlog.' }
}
