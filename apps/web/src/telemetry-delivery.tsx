import { Alert, Badge, Group, SimpleGrid, Stack, Text } from '@mantine/core'
import type { Device } from './device-types'
import { deliverySummary, recent } from './telemetry-delivery-utils'

const age = (value: string) => {
 const minutes = Math.max(0, Math.floor((Date.now() - Date.parse(value)) / 60_000))
 return minutes < 1 ? 'less than a minute old' : minutes < 60 ? minutes + ' minutes old' : Math.floor(minutes / 60) + ' hours old'
}
const time = (value?: string | null) => value ? new Date(value).toLocaleString() : 'Not yet reported'
export function TelemetryDeliveryChip({ device }: { device: Device }) {
 const state = deliverySummary(device)
 const d = device.telemetryDelivery
 if ((state.queued == null || state.queued === 0) && !(recent(d?.lastAttemptAt) && (d?.lastStatusCode ?? 0) >= 400)) return null
 return <Badge color={state.color} variant="light">{state.title}{state.queued != null && state.queued > 0 ? ' · ' + state.queued + ' queued' : ''}</Badge>
}
export function TelemetryDeliveryPanel({ device }: { device: Device }) {
 const state = deliverySummary(device)
 const d = device.telemetryDelivery
 const details = [
  ['Last check-in', time(device.lastSeenAt)],
  ['Latest monitoring observation', time(device.telemetryObservedAt || device.latest?.observedAt)],
  ['Waiting on device', state.queued != null ? state.queued + ' snapshots' : d?.queuedSnapshots != null ? d.queuedSnapshots + ' snapshots (last known)' : 'Backlog not reported — update the agent for an exact count'],
  ['Queue last reported', time(d?.queueReportedAt)],
  ['Oldest queued observation', state.queued === 0 ? 'None — queue empty' : d?.oldestQueuedAt ? age(d.oldestQueuedAt) + ' · ' + time(d.oldestQueuedAt) : 'Not reported'],
  ['Last upload accepted', time(d?.lastAcceptedAt)],
 ]
 return <Alert color={state.color} title={state.title} mb="lg"><Stack gap="sm"><Text size="sm">{state.message}</Text>
 <SimpleGrid cols={{ base: 1, sm: 2, lg: 3 }} spacing="sm">{details.map(([label, value]) => <div key={label}><Text size="xs" c="dimmed">{label}</Text><Text size="sm">{value}</Text></div>)}</SimpleGrid>
 <Group gap="xs">{d?.lastStatusCode && <Text size="xs">Last upload response: HTTP {d.lastStatusCode} · {time(d.lastAttemptAt)}</Text>}{d && <Text size="xs">Upload allowance: {d.limitPerMinute} snapshots per minute per device.</Text>}</Group>
 {recent(d?.lastAttemptAt) && d?.lastStatusCode === 429 && <Text size="xs">{d.retryAt && Date.parse(d.retryAt) > Date.now() ? 'Retry after ' + time(d.retryAt) + '.' : 'Waiting for the next upload attempt.'} Successful batches can upload between rate-limit warnings.</Text>}
 <Text size="xs" c="dimmed">Delivery details refresh every 15 seconds. Queue counts reflect the last agent check-in.</Text>
 </Stack></Alert>
}
