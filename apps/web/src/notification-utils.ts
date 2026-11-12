import type { StatusTone } from './status'

export type Route = { critical: boolean; warning: boolean; resolved: boolean }
export type QuietHours = { enabled: boolean; start: string; end: string; timezone: string }
export type Channel = {
  id: string; kind: string; target: string; route: Route
  lastDeliveredAt?: string; lastError?: string; lastErrorAt?: string; pending: number; held: number
  /** Android channel only: the phones the viewer can see, and how many there are in all. */
  phones?: Phone[]; phoneCount?: number
}
export type Phone = { id: string; name: string; user: string; mine: boolean; createdAt: string; lastSuccessAt?: string; lastError?: string }
export type NotificationSettings = { channels: Channel[]; quietHours: QuietHours; linksEnabled: boolean; canManage: boolean }

export const kindLabels: Record<string, string> = { ntfy: 'ntfy', gotify: 'Gotify', discord: 'Discord', android: 'Vigara app' }

const errorWords: Record<string, string> = {
  request_failed: 'Could not reach the server',
  rejected: 'The server refused the message; check the topic and token',
  server_error: 'The server had an error',
  rate_limited: 'Rate limited by the server',
  response_failed: 'Unexpected reply from the server',
  unexpected_status: 'Unexpected reply from the server',
  payload_too_large: 'Message too large',
  attempt_limit: 'Gave up after repeated failures',
  policy_unavailable: 'Settings could not be read',
  gone: 'The phone is no longer registered with its push app',
  no_phones: 'No phones registered yet',
  invalid_registration: 'The phone’s registration is invalid',
  encryption_failed: 'The message could not be encrypted',
}
export const errorText = (code: string) => errorWords[code] ?? code.replace(/_/g, ' ')

/** The channel's health in a few words, for the channels table. */
export function channelStatus(channel: Channel, relative: (at: string) => string): { tone: StatusTone; text: string } {
  if (channel.kind === 'android' && !channel.phoneCount) return { tone: 'idle', text: 'No phones yet' }
  if (channel.lastError) return { tone: 'crit', text: `${errorText(channel.lastError)}${channel.pending ? ` · ${channel.pending} waiting to retry` : ''}` }
  if (channel.pending) return { tone: 'info', text: `Sending ${channel.pending}` }
  const held = channel.held ? ` · ${channel.held} held for quiet hours` : ''
  if (channel.lastDeliveredAt) return { tone: 'ok', text: `Last delivered ${relative(channel.lastDeliveredAt)}${held}` }
  return { tone: 'idle', text: `Nothing sent yet${held}` }
}

/** "23:00 – 07:00, every day" with the zone when it differs from the viewer's. */
export function quietSummary(q: QuietHours, viewerZone: string): string {
  return `${q.start} – ${q.end}, every day${q.timezone !== viewerZone ? ` (${q.timezone})` : ''}`
}

export function sameSettings(a: { quietHours: QuietHours; routes: Record<string, Route> }, b: { quietHours: QuietHours; routes: Record<string, Route> }): boolean {
  return JSON.stringify(a.quietHours) === JSON.stringify(b.quietHours) && JSON.stringify(a.routes) === JSON.stringify(b.routes)
}
