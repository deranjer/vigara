import type { FleetRow } from './container-utils'
import type { StatusTone } from './status'

export type StateFilter = 'all' | 'running' | 'stopped' | 'unhealthy'

/** "repo:tag" without the digest; a bare image ID becomes its 12-character short form. */
export function imageRef(image: string): string {
  const withoutDigest = image.split('@')[0]
  if (/^sha256:[0-9a-f]+$/.test(withoutDigest)) return withoutDigest.slice(7, 19)
  return withoutDigest || image.slice(0, 19)
}

export const isUnhealthy = (row: { state: string; status?: string }) => row.state === 'restarting' || !!row.status?.includes('(unhealthy)')
const isStopped = (row: { state: string }) => row.state !== 'running' && row.state !== 'paused' && row.state !== 'restarting'

export function matchesState(row: FleetRow, filter: StateFilter): boolean {
  if (filter === 'running') return row.state === 'running'
  if (filter === 'stopped') return isStopped(row)
  if (filter === 'unhealthy') return isUnhealthy(row)
  return true
}

export function stateFilterCounts(rows: FleetRow[]): Record<StateFilter, number> {
  return { all: rows.length, running: rows.filter((r) => r.state === 'running').length, stopped: rows.filter(isStopped).length, unhealthy: rows.filter(isUnhealthy).length }
}

export function rowTone(row: { state: string; status?: string }): StatusTone {
  if (isUnhealthy(row)) return 'warn'
  if (row.state === 'dead') return 'crit'
  if (row.state === 'running') return 'ok'
  if (row.state === 'paused') return 'info'
  return 'idle'
}

/** Words for a row's state: "Up 3 days" and friends come from Docker; health is pulled out. */
export function stateText(row: { state: string; status?: string }): string {
  const status = row.status?.replace(/\s*\((healthy|unhealthy|health: starting)\)/, '').trim()
  if (row.status?.includes('(unhealthy)')) return 'Unhealthy'
  if (row.state === 'restarting') return 'Restarting'
  return status || row.state
}

/** Case-insensitive match on name, image, stack, service, device and published ports. */
export function matchesSearch(row: FleetRow, search: string): boolean {
  const needle = search.trim().toLowerCase()
  if (!needle) return true
  const ports = (row.ports ?? []).map((p) => `${p.publicPort ?? ''}:${p.privatePort}`).join(' ')
  return [row.name, row.image, row.project ?? '', row.service ?? '', row.hostName, ports].some((value) => value.toLowerCase().includes(needle))
}

export type StackGroup = { key: string; project: string; hostId: string; hostName: string; rows: FleetRow[]; running: number; unhealthy: number; tone: StatusTone }

/** Rows grouped by device and Compose project; standalone containers come last on each device. */
export function stackGroups(rows: FleetRow[]): StackGroup[] {
  const groups = new Map<string, StackGroup>()
  for (const row of [...rows].sort((a, b) => a.name.localeCompare(b.name))) {
    const key = `${row.hostId}/${row.project ?? ''}`
    let group = groups.get(key)
    if (!group) { group = { key, project: row.project ?? '', hostId: row.hostId, hostName: row.hostName, rows: [], running: 0, unhealthy: 0, tone: 'ok' }; groups.set(key, group) }
    group.rows.push(row)
  }
  for (const group of groups.values()) {
    group.running = group.rows.filter((r) => r.state === 'running').length
    group.unhealthy = group.rows.filter(isUnhealthy).length
    group.tone = group.unhealthy ? 'warn' : group.running === group.rows.length ? 'ok' : group.running ? 'warn' : 'idle'
  }
  return [...groups.values()].sort((a, b) => a.hostName.localeCompare(b.hostName) || (a.project === '' ? 1 : b.project === '' ? -1 : a.project.localeCompare(b.project)))
}

export type Usage = { cpu: number | null; memoryBytes: number | null }
type TelemetryDevice = { agentId?: string | null; latest?: { metrics?: { name: string; value: number; labels?: Record<string, string> }[] } }

/**
 * Latest CPU and memory per container from each device's telemetry, keyed by
 * "<agentId>/<first 12 characters of the container ID>".
 */
export function usageByContainer(devices: TelemetryDevice[]): Map<string, Usage> {
  const usage = new Map<string, Usage>()
  for (const device of devices) {
    if (!device.agentId) continue
    for (const metric of device.latest?.metrics ?? []) {
      const id = metric.labels?.container_id
      if (!id || (metric.name !== 'docker_cpu_capacity_used_percent' && metric.name !== 'docker_memory_usage_bytes')) continue
      const key = `${device.agentId}/${id.slice(0, 12)}`
      const entry = usage.get(key) ?? { cpu: null, memoryBytes: null }
      if (metric.name === 'docker_cpu_capacity_used_percent') entry.cpu = metric.value; else entry.memoryBytes = metric.value
      usage.set(key, entry)
    }
  }
  return usage
}

export const usageKey = (row: { hostId: string; id: string }) => `${row.hostId}/${row.id.slice(0, 12)}`
