import type { StatusTone } from './status'

export type Route = { critical: boolean; warning: boolean; resolved: boolean }
export type QuietHours = { enabled: boolean; start: string; end: string; timezone: string }
export type ChannelSource = 'settings' | 'server' | 'app'
/** What the edit form starts from; secrets only say whether they are set. */
export type ChannelConfig = { url?: string; topic?: string; hasToken: boolean }
export type Channel = {
  id: string; kind: string; name: string; target: string; route: Route
  /** settings: added here and editable; server: environment variables; app: the Vigara app channel. */
  source: ChannelSource; config?: ChannelConfig
  lastDeliveredAt?: string; lastError?: string; lastErrorAt?: string; pending: number; held: number
  /** Android channel only: the phones the viewer can see, and how many there are in all. */
  phones?: Phone[]; phoneCount?: number
}
export type Phone = { id: string; name: string; user: string; mine: boolean; createdAt: string; lastSuccessAt?: string; lastError?: string }
export type NotificationSettings = { channels: Channel[]; quietHours: QuietHours; linksEnabled: boolean; canManage: boolean; canStore: boolean }

export type ChannelKind = 'ntfy' | 'gotify' | 'discord'
/** The channel form. secret is the token (ntfy, Gotify) or webhook URL (Discord). */
export type ChannelForm = { kind: ChannelKind; name: string; url: string; topic: string; secret: string; secretTouched: boolean }

export const blankForm = (kind: ChannelKind = 'ntfy'): ChannelForm => ({ kind, name: kindLabels[kind], url: kind === 'ntfy' ? 'https://ntfy.sh' : '', topic: '', secret: '', secretTouched: false })
export const formOf = (c: Channel): ChannelForm => ({ kind: c.kind as ChannelKind, name: c.name, url: c.config?.url ?? '', topic: c.config?.topic ?? '', secret: '', secretTouched: false })

/**
 * The request body for a channel. An untouched secret is left out so the server
 * keeps the saved one; a touched, empty token removes it.
 */
export function channelBody(form: ChannelForm): Record<string, string> {
  const body: Record<string, string> = { kind: form.kind, name: form.name.trim() }
  if (form.kind !== 'discord') body.url = form.url.trim()
  if (form.kind === 'ntfy') body.topic = form.topic.trim()
  if (form.secretTouched) body[form.kind === 'discord' ? 'webhookUrl' : 'token'] = form.secret.trim()
  return body
}

/** Why the form cannot be saved yet, or '' when it can. Mirrors the server's checks loosely. */
export function formProblem(form: ChannelForm, editing: Channel | null): string {
  if (!form.name.trim()) return 'Give the channel a name.'
  if (form.kind !== 'discord' && !/^https?:\/\/[^\s/]+/.test(form.url.trim())) return 'Enter the server URL, starting with http:// or https://.'
  if (form.kind === 'ntfy' && !/^[A-Za-z0-9_-]{1,64}$/.test(form.topic.trim())) return 'Topics are 1–64 letters, digits, - or _.'
  const hasSecret = form.secretTouched ? form.secret.trim() !== '' : !!editing?.config?.hasToken
  if (form.kind === 'gotify' && !hasSecret) return 'Enter the Gotify application token.'
  if (form.kind === 'discord' && !hasSecret) return 'Paste the Discord webhook URL.'
  return ''
}

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
  vapid_failed: 'The push could not be signed',
  no_master_key: 'The server has no key to read this channel',
  secrets_unreadable: 'Saved with a different server key; edit it and enter the token again',
  invalid_destination: 'The saved settings are invalid; edit the channel',
}
export const errorText = (code: string) => errorWords[code] ?? code.replace(/_/g, ' ')

/** The channel's health in a few words, for the channels table. */
export function channelStatus(channel: Channel, relative: (at: string) => string): { tone: StatusTone; text: string } {
  if (channel.kind === 'android' && !channel.phoneCount) return { tone: 'idle', text: 'No phones yet' }
  if (channel.target.startsWith('Cannot send: ')) return { tone: 'crit', text: errorText(channel.target.slice(13).replace(/ /g, '_')) }
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
