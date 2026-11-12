import { describe, expect, it } from 'vitest'
import type { Device } from './device-types'
import { agentHealth, compareRows, compareValues, containersByStack, deviceTab, shortUptime } from './device-utils'
import { formatRoute, parseRoute, route } from './route'
import type { TrendResponse } from './trend-utils'

const trend = (series: TrendResponse['series']): TrendResponse => ({ range: '24h', windowStart: '2026-11-11T00:00:00Z', windowEnd: '2026-11-11T00:03:00Z', stepSeconds: 60, bins: 3, series })
const fs = (mount: string, total: (number | null)[], free: (number | null)[]): TrendResponse['series'] => [
  { name: 'filesystem_total_bytes', unit: 'By', labels: { mount_path: mount }, values: total },
  { name: 'filesystem_free_bytes', unit: 'By', labels: { mount_path: mount }, values: free },
]
const now = Date.parse('2026-11-11T12:00:00Z')
const device = (extra: Partial<Device> = {}): Device => ({ id: 'd', name: 'nas-01', status: 'healthy', agentId: 'a', platform: 'linux', architecture: 'amd64', agentVersion: '1.4.0', capabilities: [], lastSeenAt: '2026-11-11T11:59:50Z', ...extra })

describe('device page helpers', () => {
  it('reads the tab from the address and keeps device tabs deep-linkable', () => {
    expect(deviceTab(null)).toBe('overview')
    expect(deviceTab('metrics')).toBe('metrics')
    expect(deviceTab('shell')).toBe('overview')
    expect(parseRoute('#/devices/d1/backups')).toMatchObject({ section: 'devices', id: 'd1', sub: 'backups' })
    expect(formatRoute(route('devices', 'd1', 'settings'))).toBe('#/devices/d1/settings')
  })

  it('builds Compare lines: host CPU only, the fullest disk per bin, and network receive plus send', () => {
    const t = trend([
      { name: 'cpu_used_percent', unit: '%', labels: null, values: [10, null, 30] },
      { name: 'cpu_used_percent', unit: '%', labels: { container_id: 'c1' }, values: [99, 99, 99] },
      ...fs('/', [100, 100, 100], [50, 40, null]), ...fs('/data', [200, 200, 200], [20, 180, null]),
      { name: 'network_receive_bytes_per_second', unit: 'By/s', labels: { interface: 'eth0' }, values: [100, null, 5] },
      { name: 'network_send_bytes_per_second', unit: 'By/s', labels: { interface: 'eth0' }, values: [50, null, null] },
    ])
    expect(compareValues(t, 'cpu')).toEqual([10, null, 30])
    expect(compareValues(t, 'disk')).toEqual([90, 60, null])
    expect(compareValues(t, 'network')).toEqual([150, null, 5])
    expect(compareValues(trend([]), 'memory')).toEqual([])
    const rows = compareRows([{ id: 'a', trend: t }, { id: 'b', trend: trend([{ name: 'cpu_used_percent', unit: '%', labels: {}, values: [1, 2, 3] }]) }], 'cpu')
    expect(rows).toEqual([
      { timestamp: Date.parse('2026-11-11T00:00:00Z'), a: 10, b: 1 },
      { timestamp: Date.parse('2026-11-11T00:01:00Z'), a: null, b: 2 },
      { timestamp: Date.parse('2026-11-11T00:02:00Z'), a: 30, b: 3 },
    ])
  })

  it('summarises the agent in a chip and flags upload problems so the details open', () => {
    expect(agentHealth(device({ telemetryDelivery: { lastAttemptAt: '2026-11-11T11:59:50Z', lastStatusCode: 202, queueReportedAt: '2026-11-11T11:59:50Z', queuedSnapshots: 0, limitPerMinute: 10 } }), now)).toEqual({ tone: 'ok', label: 'Agent healthy', detail: 'checked in just now', problem: false })
    expect(agentHealth(device({ telemetryDelivery: { lastAttemptAt: '2026-11-11T11:59:00Z', lastStatusCode: 429, limitPerMinute: 10 } }), now)).toMatchObject({ tone: 'warn', label: 'Uploads rate limited', problem: true })
    expect(agentHealth(device({ status: 'stale', lastSeenAt: '2026-11-10T06:00:00Z' }), now)).toMatchObject({ tone: 'warn', label: 'Agent not checking in', detail: 'last check-in 1 d ago', problem: true })
    expect(agentHealth(device({ agentId: null }), now)).toMatchObject({ label: 'No agent', problem: false })
  })

  it('groups containers by Compose project with standalone containers last', () => {
    const c = (name: string, project?: string) => ({ id: name, name, image: 'x', state: 'running', status: 'Up', created: 0, project })
    expect(containersByStack([c('web', 'blog'), c('portainer'), c('db', 'blog'), c('jellyfin', 'media')]).map((g) => [g.project, g.rows.map((r) => r.name)]))
      .toEqual([['blog', ['db', 'web']], ['media', ['jellyfin']], ['', ['portainer']]])
  })

  it('formats uptime briefly', () => {
    expect(shortUptime(3 * 86400 + 4 * 3600 + 60)).toBe('3 d 4 h')
    expect(shortUptime(5 * 3600 + 12 * 60)).toBe('5 h 12 min')
    expect(shortUptime(480)).toBe('8 min')
    expect(shortUptime(undefined)).toBeNull()
  })
})
