import { describe, expect, it } from 'vitest'
import { activityItems, attentionItems, deviceRows, relativeTime, topContainers, worstDisk, type OverviewDevice } from './overview-utils'
import { route } from './route'

const fs = (mount: string, total: number, free: number) => [
  { name: 'filesystem_total_bytes', value: total, unit: 'By', labels: { mount_path: mount, filesystem: 'ext4' } },
  { name: 'filesystem_free_bytes', value: free, unit: 'By', labels: { mount_path: mount, filesystem: 'ext4' } },
]
const pi: OverviewDevice = {
  id: 'd1', name: 'edge-pi', status: 'healthy', agentId: 'a1', lastSeenAt: '2026-11-11T10:00:00Z',
  latest: {
    host: { cpuModel: 'Cortex-A76', logicalCpus: 4, totalMemoryBytes: 8 * 2 ** 30 },
    metrics: [
      { name: 'cpu_used_percent', value: 9.5, unit: '%' }, { name: 'memory_used_percent', value: 37, unit: '%' },
      ...fs('/', 100, 7), ...fs('/boot', 100, 88),
      { name: 'docker_memory_usage_bytes', value: 400, unit: 'By', labels: { container_id: 'c1' } },
      { name: 'docker_memory_usage_bytes', value: 900, unit: 'By', labels: { container_id: 'c2' } },
      { name: 'docker_cpu_capacity_used_percent', value: 2.5, unit: '%', labels: { container_id: 'c2' } },
    ],
    dockerContainers: [{ id: 'c1', name: 'pihole', state: 'running' }, { id: 'c2', name: 'kuma', state: 'running' }, { id: 'c3', name: 'old', state: 'exited' }],
  },
}
const laptop: OverviewDevice = { id: 'd2', name: 'old-laptop', status: 'stale', lastSeenAt: '2026-11-10T04:00:00Z' }

describe('overview data', () => {
  it('summarises each device from its latest snapshot', () => {
    const [row, stale] = deviceRows([pi, laptop])
    expect(row).toMatchObject({ name: 'edge-pi', tone: 'ok', cpu: 9.5, memory: 37, diskMount: '/', containers: 2, detail: 'Cortex-A76 · 4 vCPU · 8 GB' })
    expect(row.disk).toBeCloseTo(93)
    expect(stale).toMatchObject({ tone: 'warn', cpu: null, disk: null, containers: null })
    expect(worstDisk(laptop)).toBeNull()
  })

  it('ranks containers by memory and names them from the inventory', () => {
    expect(topContainers([pi, laptop])).toEqual([
      { deviceId: 'd1', device: 'edge-pi', name: 'kuma', memoryBytes: 900, cpu: 2.5 },
      { deviceId: 'd1', device: 'edge-pi', name: 'pihole', memoryBytes: 400, cpu: null },
    ])
  })

  it('lists what needs attention, most serious first, each with a link', () => {
    const items = attentionItems({
      devices: [pi, laptop],
      incidents: [{ id: 'i1', status: 'firing', summary: 'nas-01 is offline', dimension: 'device', startedAt: '2026-11-11T09:00:00Z' }, { id: 'i2', status: 'resolved', summary: 'old', dimension: 'x', startedAt: '2026-11-01T00:00:00Z' }],
      updates: [{ agentId: 'a1', device: 'edge-pi', ignored: false, result: { name: 'pihole', status: 'available' } }, { agentId: 'a1', device: 'edge-pi', ignored: true, result: { name: 'kuma', status: 'available' } }],
      stacks: [{ id: 'media', name: 'Media', draft: true }, { id: 'web', name: 'Web', deploymentStatus: 'failed', deploymentAt: '2026-11-11T08:00:00Z' }],
      hosts: [{ agentId: 'a1', name: 'edge-pi', state: 'ok', containers: [{ name: 'kuma', state: 'running', status: 'Up 2 days (unhealthy)' }, { name: 'pihole', state: 'running', status: 'Up 2 days (healthy)' }] }],
      policies: 0,
    })
    expect(items.map((i) => [i.key, i.tone])).toEqual([
      ['alerts', 'crit'], ['disk:d1', 'crit'], ['deploy:web', 'crit'],
      ['device:d2', 'warn'], ['containers:a1', 'warn'], ['updates', 'warn'], ['draft:media', 'warn'],
      ['backups', 'idle'],
    ])
    expect(items.find((i) => i.key === 'alerts')).toMatchObject({ title: 'nas-01 is offline', to: route('alerts') })
    expect(items.find((i) => i.key === 'containers:a1')).toMatchObject({ title: 'kuma is unhealthy or restarting', to: route('containers', 'a1', 'kuma') })
    expect(items.find((i) => i.key === 'updates')?.title).toBe('pihole has a newer image')
    expect(items.find((i) => i.key === 'draft:media')?.to).toEqual(route('stacks', 'media'))
    expect(attentionItems({ devices: [], incidents: [], updates: [], stacks: [], hosts: [], policies: 2 })).toEqual([])
  })

  it('merges audit entries, incidents and deployments into one newest-first feed', () => {
    const feed = activityItems({
      audit: [
        { id: 2, occurredAt: '2026-11-11T10:00:00Z', actorName: 'deranjer', action: 'stack.deploy', targetId: 'media', outcome: 'success' },
        { id: 1, occurredAt: '2026-11-11T07:00:00Z', actorName: 'system', action: 'scheduled_task.run', outcome: 'success' },
      ],
      incidents: [{ id: 'i1', status: 'resolved', summary: 'nas-01 is offline', dimension: 'd', startedAt: '2026-11-11T08:00:00Z', resolvedAt: '2026-11-11T09:00:00Z' }],
      stacks: [{ id: 'web', name: 'Web', deploymentStatus: 'failed', deploymentAt: '2026-11-11T09:30:00Z' }, { id: 'never', name: 'Never', deploymentAt: '0001-01-01T00:00:00Z' }],
    })
    expect(feed.map((i) => i.text)).toEqual(['Stack deploy: media · deranjer', 'Deploy failed: Web', 'Resolved: nas-01 is offline', 'Alert: nas-01 is offline'])
    expect(feed[0].to).toEqual(route('stacks', 'media'))
  })

  it('formats relative times', () => {
    const now = Date.parse('2026-11-11T12:00:00Z')
    expect(relativeTime('2026-11-11T11:59:30Z', now)).toBe('just now')
    expect(relativeTime('2026-11-11T11:55:00Z', now)).toBe('5 min ago')
    expect(relativeTime('2026-11-11T09:00:00Z', now)).toBe('3 h ago')
    expect(relativeTime('2026-11-09T12:00:00Z', now)).toBe('2 d ago')
    expect(relativeTime(null, now)).toBe('—')
  })
})
