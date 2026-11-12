import { describe, expect, it } from 'vitest'
import { primaryActions } from './container-actions'
import { imageRef, matchesSearch, matchesState, rowTone, stackGroups, stateFilterCounts, stateText, usageByContainer, usageKey } from './container-list-utils'
import type { FleetRow } from './container-utils'

const row = (name: string, extra: Partial<FleetRow> = {}): FleetRow => ({ id: `${name}0000000000000000`, name, image: 'nginx:1.27', state: 'running', status: 'Up 3 days', created: 0, hostId: 'h1', hostName: 'nas-01', ...extra })

describe('container list', () => {
  it('shows images as repo:tag without the digest', () => {
    expect(imageRef('ghcr.io/immich-app/immich-server:v1.120@sha256:abcdef0123')).toBe('ghcr.io/immich-app/immich-server:v1.120')
    expect(imageRef('sha256:0123456789abcdef0123456789')).toBe('0123456789ab')
    expect(imageRef('redis:7-alpine')).toBe('redis:7-alpine')
  })

  it('filters by state and reads health out of the status', () => {
    const rows = [row('a'), row('b', { status: 'Up 2 days (unhealthy)' }), row('c', { state: 'exited', status: 'Exited (0) 1 h ago' }), row('d', { state: 'restarting', status: 'Restarting (1) 5 s ago' })]
    expect(stateFilterCounts(rows)).toEqual({ all: 4, running: 2, stopped: 1, unhealthy: 2 })
    expect(rows.filter((r) => matchesState(r, 'unhealthy')).map((r) => r.name)).toEqual(['b', 'd'])
    expect(rows.map(rowTone)).toEqual(['ok', 'warn', 'idle', 'warn'])
    expect(stateText(row('x', { status: 'Up 3 days (healthy)' }))).toBe('Up 3 days')
    expect(stateText(rows[1])).toBe('Unhealthy')
    expect(stateText(rows[3])).toBe('Restarting')
  })

  it('searches name, image, stack, device and published ports', () => {
    const r = row('web', { project: 'blog', ports: [{ privatePort: 80, publicPort: 8080, type: 'tcp' }] })
    expect(['WEB', 'nginx', 'blog', 'nas', '8080'].every((q) => matchesSearch(r, q))).toBe(true)
    expect(matchesSearch(r, 'postgres')).toBe(false)
  })

  it('groups by device and stack with a status per group, standalone last', () => {
    const groups = stackGroups([row('z'), row('db', { project: 'blog' }), row('web', { project: 'blog', status: 'Up (unhealthy)' }), row('old', { project: 'media', state: 'exited' }), row('x', { hostId: 'h0', hostName: 'edge-pi' })])
    expect(groups.map((g) => [g.hostName, g.project, g.rows.map((r) => r.name), `${g.running}/${g.rows.length}`, g.tone])).toEqual([
      ['edge-pi', '', ['x'], '1/1', 'ok'],
      ['nas-01', 'blog', ['db', 'web'], '2/2', 'warn'],
      ['nas-01', 'media', ['old'], '0/1', 'idle'],
      ['nas-01', '', ['z'], '1/1', 'ok'],
    ])
  })

  it('matches telemetry usage to containers by the short ID', () => {
    const usage = usageByContainer([{ agentId: 'h1', latest: { metrics: [
      { name: 'docker_memory_usage_bytes', value: 1024, labels: { container_id: 'web0000000000000000' } },
      { name: 'docker_cpu_capacity_used_percent', value: 2.5, labels: { container_id: 'web0000000000000000' } },
      { name: 'cpu_used_percent', value: 50 },
    ] } }])
    expect(usage.get(usageKey(row('web')))).toEqual({ cpu: 2.5, memoryBytes: 1024 })
    expect(usage.size).toBe(1)
  })

  it('puts Restart and Stop (or Start) on buttons and the rest in the menu', () => {
    expect(primaryActions('running')).toEqual(['restart', 'stop'])
    expect(primaryActions('paused')).toEqual(['unpause', 'stop'])
    expect(primaryActions('exited')).toEqual(['start'])
  })
})
