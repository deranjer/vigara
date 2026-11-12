import { route, type Route } from './route'
import type { StatusTone } from './status'

export type Severity = 'warning' | 'critical'
export type AlertNote = { id: string; author: string; body: string; createdAt: string }
export type Incident = {
  id: string; ruleId: string; ruleName: string; kind: string; severity: Severity; dimension: string; status: string; summary: string
  startedAt: string; lastObservedAt?: string; resolvedAt?: string; acknowledgedAt?: string; acknowledgedBy?: string; mutedUntil?: string; notes: AlertNote[]
}
export type RuleConfig = {
  deviceId?: string; staleAfterSeconds?: number; metric?: string; operator?: string; threshold?: number; sustainSeconds?: number
  graceSeconds?: number; labels?: Record<string, string>; policyKey?: string; intervalSeconds?: number
}
export type AlertRule = { id: string; name: string; kind: string; enabled: boolean; severity: Severity; config: RuleConfig; createdAt: string }
export type TimelineItem = {
  kind: 'firing' | 'resolved' | 'image'; at: string; summary: string; rule?: string; severity?: Severity; dimension?: string; incidentId?: string
  deviceId?: string; deviceName?: string; agentId?: string; container?: string; image?: string
}
/** Fired after an alert is acknowledged, muted or unmuted, so the sidebar badge refreshes at once. */
export const alertsChangedEvent = 'vigara:alerts-changed'

export type FleetWindow = { id: string; deviceId: string; deviceName: string; startsAt: string; endsAt: string; active: boolean }

export const inboxTabs = ['open', 'acknowledged', 'muted', 'events', 'rules'] as const
export type InboxTab = (typeof inboxTabs)[number]
export type Bucket = 'open' | 'acknowledged' | 'muted' | 'resolved'

/** Which inbox tab an incident sits in. A mute outlives an acknowledgement; resolved alerts leave the inbox. */
export function bucketOf(incident: Pick<Incident, 'status' | 'acknowledgedAt' | 'mutedUntil'>, now = Date.now()): Bucket {
  if (incident.status !== 'firing') return 'resolved'
  if (incident.mutedUntil && new Date(incident.mutedUntil).getTime() > now) return 'muted'
  return incident.acknowledgedAt ? 'acknowledged' : 'open'
}

/** Open alerts: what the sidebar badge counts. */
export function openCount(incidents: Pick<Incident, 'status' | 'acknowledgedAt' | 'mutedUntil'>[], now = Date.now()): number {
  return incidents.filter((i) => bucketOf(i, now) === 'open').length
}

export const severityTone = (severity: string | undefined): StatusTone => severity === 'critical' ? 'crit' : 'warn'

/** Splits a dimension such as "<device>/image/<container>" into its device and the rest. */
function dimensionParts(dimension: string): { device: string; rest: string } {
  const slash = dimension.indexOf('/')
  return slash < 0 ? { device: dimension, rest: '' } : { device: dimension.slice(0, slash), rest: dimension.slice(slash + 1) }
}
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i

export function incidentDevice(incident: Pick<Incident, 'dimension'>): string | null {
  const { device } = dimensionParts(incident.dimension)
  return uuid.test(device) ? device : null
}

/** Where "Open" goes for an alert: the cause on the right page and tab. */
export function incidentTarget(incident: Pick<Incident, 'kind' | 'dimension'>): { label: string; to: Route } | null {
  const device = incidentDevice(incident)
  const { rest } = dimensionParts(incident.dimension)
  switch (incident.kind) {
    case 'device_unavailable': return device ? { label: 'Open device', to: route('devices', device) } : null
    case 'threshold': return device ? { label: 'Open metrics', to: route('devices', device, 'metrics') } : null
    case 'backup_failure': case 'backup_overdue': return { label: 'Open backups', to: route('backups') }
    case 'pinned_image': return { label: 'Open updates', to: route('updates') }
    case 'image_update': {
      const stack = rest.startsWith('image-update-failure/') ? rest.slice('image-update-failure/'.length) : ''
      return stack ? { label: 'Open stack', to: route('stacks', stack) } : { label: 'Open updates', to: route('updates') }
    }
  }
  return device ? { label: 'Open device', to: route('devices', device) } : null
}

/** "45 min", "1 h 31 m", "2 d". */
export function duration(fromIso: string, now = Date.now()): string {
  const minutes = Math.max(0, Math.floor((now - new Date(fromIso).getTime()) / 60000))
  if (minutes < 60) return `${minutes} min`
  if (minutes < 24 * 60) return `${Math.floor(minutes / 60)} h${minutes % 60 ? ` ${minutes % 60} m` : ''}`
  return `${Math.floor(minutes / 1440)} d`
}

function seconds(value: number | undefined): string {
  const s = value ?? 0
  if (s % 86400 === 0 && s >= 86400) return `${s / 86400} d`
  if (s % 3600 === 0 && s >= 3600) return `${s / 3600} h`
  if (s % 60 === 0 && s >= 60) return `${s / 60} min`
  return `${s} s`
}
function bytes(value: number): string {
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let v = value, i = 0
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return `${Number.isInteger(v) ? v : v.toFixed(1)} ${units[i]}`
}

export const metricLabels: Record<string, string> = {
  cpu_used_percent: 'CPU', memory_used_percent: 'Memory', memory_available_bytes: 'Available memory', filesystem_free_bytes: 'Free disk', filesystem_total_bytes: 'Disk size',
}
const operatorWords: Record<string, string> = { gt: 'over', gte: 'at or over', lt: 'under', lte: 'at or under' }

/** The rule's condition in words, for the rules table. */
export function ruleCondition(rule: Pick<AlertRule, 'kind' | 'config'>): string {
  const c = rule.config
  switch (rule.kind) {
    case 'device_unavailable': return `No check-in for ${seconds(c.staleAfterSeconds)}`
    case 'threshold': {
      const value = c.metric?.endsWith('_bytes') ? bytes(c.threshold ?? 0) : `${c.threshold ?? 0}%`
      return `${metricLabels[c.metric ?? ''] ?? c.metric} ${operatorWords[c.operator ?? ''] ?? c.operator} ${value}${c.sustainSeconds ? ` for ${seconds(c.sustainSeconds)}` : ''}`
    }
    case 'backup_failure': return `Backup “${c.policyKey}” fails`
    case 'backup_overdue': return `No successful “${c.policyKey}” backup for ${seconds(c.intervalSeconds)}`
    case 'image_update': return 'An automatic image update fails or its result is unknown'
    case 'pinned_image': return 'The image changes under a fixed version tag such as 1.2.3'
  }
  return rule.kind
}

/** What a rule watches, for the rules table. */
export function ruleScope(rule: Pick<AlertRule, 'kind' | 'config'>, deviceName: (id: string) => string): string {
  if (rule.kind === 'image_update') return 'Stacks with automatic updates'
  if (rule.kind === 'pinned_image') return 'Containers on a version tag'
  return rule.config.deviceId ? deviceName(rule.config.deviceId) : 'All devices'
}

export const builtInRule = (kind: string) => kind === 'image_update' || kind === 'pinned_image'

/** One line in the Events tab. */
export function timelineText(item: TimelineItem): { tone: StatusTone; text: string } {
  if (item.kind === 'image') return { tone: 'info', text: `${item.container || 'A container'} has a newer image${item.image ? ` · ${item.image}` : ''}${item.deviceName ? ` · ${item.deviceName}` : ''}` }
  if (item.kind === 'resolved') return { tone: 'ok', text: `${item.rule || 'Alert'} cleared${item.summary === 'rule disabled' ? ' (rule switched off)' : ''}` }
  return { tone: severityTone(item.severity), text: `${item.rule ? `${item.rule}: ` : ''}${item.summary}` }
}
