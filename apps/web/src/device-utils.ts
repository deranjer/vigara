import type { ContainerInfo } from './container-utils'
import type { Device } from './device-types'
import { relativeTime } from './overview-utils'
import type { StatusTone } from './status'
import { deliverySummary, recent } from './telemetry-delivery-utils'
import type { TrendResponse } from './trend-utils'

export const deviceTabs = ['overview', 'metrics', 'containers', 'backups', 'settings'] as const
export type DeviceTab = (typeof deviceTabs)[number]

/** The tab named in the address (#/devices/<id>/<tab>), or Overview. */
export function deviceTab(value: string | null): DeviceTab {
  return deviceTabs.find((tab) => tab === value) ?? 'overview'
}

export type CompareMetric = 'cpu' | 'memory' | 'disk' | 'network'
type TrendSeries = TrendResponse['series'][number]

const hostSeries = (trend: TrendResponse, name: string) => trend.series.filter((s) => s.name === name && !s.labels?.container_id)

/** Adds aligned series bin by bin; a bin is missing only when every series is missing there. */
function sum(series: TrendSeries[], length: number): (number | null)[] {
  return Array.from({ length }, (_, i) => {
    const known = series.map((s) => s.values[i]).filter((v): v is number => v !== null && v !== undefined)
    return known.length ? known.reduce((a, b) => a + b, 0) : null
  })
}

/**
 * One device's line for the Compare chart: CPU or memory use, the fullest filesystem's
 * use (all in %), or network receive plus send (bytes per second).
 */
export function compareValues(trend: TrendResponse, metric: CompareMetric): (number | null)[] {
  const length = trend.bins || Math.max(0, ...trend.series.map((s) => s.values.length))
  if (metric === 'cpu') return hostSeries(trend, 'cpu_used_percent')[0]?.values ?? []
  if (metric === 'memory') return hostSeries(trend, 'memory_used_percent')[0]?.values ?? []
  if (metric === 'network') return sum([...hostSeries(trend, 'network_receive_bytes_per_second'), ...hostSeries(trend, 'network_send_bytes_per_second')], length)
  const totals = hostSeries(trend, 'filesystem_total_bytes')
  const frees = hostSeries(trend, 'filesystem_free_bytes')
  if (!totals.length) return []
  return Array.from({ length }, (_, i) => {
    let worst: number | null = null
    for (const total of totals) {
      const free = frees.find((f) => f.labels?.mount_path === total.labels?.mount_path)?.values[i]
      const size = total.values[i]
      if (free === null || free === undefined || !size) continue
      const used = Math.max(0, Math.min(100, (1 - free / size) * 100))
      if (worst === null || used > worst) worst = used
    }
    return worst
  })
}

/** Rows for a multi-line chart: one per time bin, one key per device. */
export function compareRows(trends: { id: string; trend: TrendResponse }[], metric: CompareMetric): Record<string, number | null>[] {
  if (!trends.length) return []
  const first = trends[0].trend
  const start = Date.parse(first.windowStart)
  const lines = trends.map(({ id, trend }) => ({ id, values: compareValues(trend, metric) }))
  const length = Math.max(0, ...lines.map((l) => l.values.length))
  return Array.from({ length }, (_, i) => {
    const row: Record<string, number | null> = { timestamp: start + i * first.stepSeconds * 1000 }
    for (const line of lines) row[line.id] = line.values[i] ?? null
    return row
  })
}

export type AgentHealth = { tone: StatusTone; label: string; detail: string; problem: boolean }

/**
 * The agent chip in a device's header. It reports the agent and its uploads in a few words;
 * `problem` is true when uploads are delayed, throttled or failing, so the details open by themselves.
 */
export function agentHealth(device: Device, now = Date.now()): AgentHealth {
  if (!device.agentId) return { tone: 'idle', label: 'No agent', detail: 'Install the agent to collect information', problem: false }
  if (!device.lastSeenAt) return { tone: 'warn', label: 'Agent never checked in', detail: 'Waiting for the first check-in', problem: true }
  const delivery = deliverySummary(device, now)
  const seen = relativeTime(device.lastSeenAt, now)
  if (!recent(device.lastSeenAt, now)) return { tone: device.status === 'failed' ? 'crit' : 'warn', label: 'Agent not checking in', detail: `last check-in ${seen}`, problem: true }
  const tone: StatusTone = delivery.color === 'red' ? 'crit' : delivery.color === 'yellow' ? 'warn' : delivery.color === 'blue' ? 'info' : 'ok'
  if (tone === 'crit' || tone === 'warn') return { tone, label: delivery.title, detail: `checked in ${seen}`, problem: true }
  if (tone === 'info') return { tone, label: `Agent catching up${delivery.queued ? ` · ${delivery.queued} queued` : ''}`, detail: `checked in ${seen}`, problem: false }
  return { tone: 'ok', label: 'Agent healthy', detail: `checked in ${seen}`, problem: false }
}

/** "3 d 4 h", "5 h 12 min", "8 min". */
export function shortUptime(seconds: number | undefined): string | null {
  if (seconds === undefined || !Number.isFinite(seconds) || seconds < 0) return null
  const days = Math.floor(seconds / 86400)
  const hours = Math.floor((seconds % 86400) / 3600)
  const minutes = Math.floor((seconds % 3600) / 60)
  return days ? `${days} d ${hours} h` : hours ? `${hours} h ${minutes} min` : `${minutes} min`
}

export const platformName = (platform: string) => ({ linux: 'Linux', windows: 'Windows', darwin: 'macOS' } as Record<string, string>)[platform] ?? platform

/** Containers grouped by Compose project; each row opens the container's own page. */
export function containersByStack(containers: ContainerInfo[]): { project: string; rows: ContainerInfo[] }[] {
  const groups = new Map<string, ContainerInfo[]>()
  for (const c of [...containers].sort((a, b) => a.name.localeCompare(b.name))) {
    const key = c.project ?? ''
    groups.set(key, [...(groups.get(key) ?? []), c])
  }
  return [...groups.entries()].sort(([a], [b]) => (a === '' ? 1 : b === '' ? -1 : a.localeCompare(b))).map(([project, rows]) => ({ project, rows }))
}

/** "Debian GNU/Linux 12" when the agent reports a distribution, otherwise "Linux", "Windows"… */
export function osName(host: { os?: string; osVersion?: string } | undefined, platform: string): string {
  const version = host?.osVersion?.trim()
  const os = platformName(host?.os || platform)
  if (!version) return os
  return /[a-z]{3}/i.test(version) ? version : `${os} ${version}`
}

/** Memory in whole binary gigabytes, as the header and list show it. */
export const gigabytes = (bytes: number) => `${Math.round(bytes / 2 ** 30)} GB`
