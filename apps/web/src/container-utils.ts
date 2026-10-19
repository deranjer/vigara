export type DockerPort = { ip?: string; privatePort: number; publicPort?: number; type: string }
export type ContainerInfo = { id: string; name: string; image: string; state: string; status: string; created: number; ports?: DockerPort[]; project?: string; service?: string }
export type HostState = 'ok' | 'offline' | 'unavailable' | 'error'
export type HostEntry = { agentId: string; name: string; state: HostState; message?: string; dockerTier?: string; containers: ContainerInfo[]; truncated?: boolean }
export type FleetRow = ContainerInfo & { hostId: string; hostName: string; hostTier?: string }

export type LogLine = { s: 'o' | 'e'; t?: string; m: string; x?: boolean }
export type StatsSample = { t: string; cpu: number; cpuCores: number; memUsed: number; memLimit: number; memPct: number; netRx: number; netTx: number; netRxBps?: number; netTxBps?: number; blkRead: number; blkWrite: number; pids: number }
export type StreamEnd = { end: true; code?: string; message?: string }

export const stateColors: Record<string, string> = { running: 'green', exited: 'gray', paused: 'yellow', restarting: 'orange', dead: 'red', created: 'blue', removing: 'grape' }

export function formatBytes(value: number, digits = 1): string {
  if (!Number.isFinite(value) || value < 0) return '–'
  if (value < 1024) return `${Math.round(value)} B`
  const units = ['KiB', 'MiB', 'GiB', 'TiB']
  let scaled = value / 1024
  let unit = 0
  while (scaled >= 1024 && unit < units.length - 1) { scaled /= 1024; unit += 1 }
  return `${scaled.toFixed(scaled >= 100 ? 0 : digits)} ${units[unit]}`
}

export function formatRate(bytesPerSecond: number | undefined): string {
  return bytesPerSecond === undefined ? '–' : `${formatBytes(bytesPerSecond)}/s`
}

/** Flattens every healthy host's containers into rows that remember where they run. */
export function fleetRows(hosts: HostEntry[]): FleetRow[] {
  return hosts.flatMap((host) => host.containers.map((container) => ({ ...container, hostId: host.agentId, hostName: host.name, hostTier: host.dockerTier })))
}

export type FleetFilter = { search: string; hostId: string | null; state: string | null }

export function filterRows(rows: FleetRow[], filter: FleetFilter): FleetRow[] {
  const needle = filter.search.trim().toLowerCase()
  return rows.filter((row) => {
    if (filter.hostId && row.hostId !== filter.hostId) return false
    if (filter.state && row.state !== filter.state) return false
    if (!needle) return true
    return [row.name, row.image, row.project ?? '', row.service ?? '', row.id, row.hostName].some((value) => value.toLowerCase().includes(needle))
  })
}

export type RowGroup = { key: string; title: string; subtitle: string; rows: FleetRow[] }

/** Groups rows by host and Compose project; containers outside Compose share a "Standalone" group per host. */
export function groupRows(rows: FleetRow[]): RowGroup[] {
  const groups = new Map<string, RowGroup>()
  for (const row of rows) {
    const key = `${row.hostId}\u0000${row.project ?? ''}`
    let group = groups.get(key)
    if (!group) {
      group = { key, title: row.project || 'Standalone containers', subtitle: row.hostName, rows: [] }
      groups.set(key, group)
    }
    group.rows.push(row)
  }
  return [...groups.values()].sort((a, b) => a.subtitle.localeCompare(b.subtitle) || (a.title === 'Standalone containers' ? 1 : 0) - (b.title === 'Standalone containers' ? 1 : 0) || a.title.localeCompare(b.title))
}

export function paginate<T>(items: T[], page: number, size: number): { items: T[]; pages: number; page: number } {
  const pages = Math.max(1, Math.ceil(items.length / size))
  const current = Math.min(Math.max(1, page), pages)
  return { items: items.slice((current - 1) * size, current * size), pages, page: current }
}

export function stateCounts(rows: FleetRow[]): Record<string, number> {
  const counts: Record<string, number> = {}
  for (const row of rows) counts[row.state] = (counts[row.state] ?? 0) + 1
  return counts
}

export function portSummary(ports: DockerPort[] | undefined, limit = 3): string {
  if (!ports || ports.length === 0) return ''
  const seen = new Set<string>()
  const labels: string[] = []
  for (const port of ports) {
    const label = port.publicPort ? `${port.publicPort}→${port.privatePort}/${port.type}` : `${port.privatePort}/${port.type}`
    if (!seen.has(label)) { seen.add(label); labels.push(label) }
  }
  return labels.length > limit ? `${labels.slice(0, limit).join(', ')} +${labels.length - limit}` : labels.join(', ')
}

/** The WebSocket address for a same-origin path. */
export function streamUrl(path: string, location: { protocol: string; host: string }): string {
  return `${location.protocol === 'https:' ? 'wss:' : 'ws:'}//${location.host}${path}`
}

/**
 * Splits a stream message into its newline-delimited JSON records. The server
 * guarantees each message holds whole lines, so a bad line is skipped rather
 * than breaking the stream.
 */
export function parseRecords<T>(message: string): (T | StreamEnd)[] {
  const records: (T | StreamEnd)[] = []
  for (const line of message.split('\n')) {
    if (!line.trim()) continue
    try { records.push(JSON.parse(line) as T | StreamEnd) } catch { /* Skip a malformed line. */ }
  }
  return records
}

export function isStreamEnd(record: unknown): record is StreamEnd {
  return typeof record === 'object' && record !== null && (record as { end?: unknown }).end === true
}

/** Appends lines to a bounded buffer, dropping the oldest when it grows past the limit. */
export function appendBounded<T>(buffer: T[], incoming: T[], limit: number): T[] {
  const merged = buffer.concat(incoming)
  return merged.length > limit ? merged.slice(merged.length - limit) : merged
}

export function filterLogLines(lines: LogLine[], search: string): LogLine[] {
  const needle = search.trim().toLowerCase()
  return needle ? lines.filter((line) => line.m.toLowerCase().includes(needle)) : lines
}

export function logsToText(lines: LogLine[]): string {
  return lines.map((line) => `${line.t ? `${line.t} ` : ''}${line.s === 'e' ? '[stderr] ' : ''}${line.m}`).join('\n')
}

export function containerName(rows: FleetRow[], hostId: string, id: string): string | null {
  const match = rows.find((row) => row.hostId === hostId && (row.id === id || row.name === id))
  return match ? match.name : null
}

export type ActionName = 'start' | 'stop' | 'restart' | 'pause' | 'unpause' | 'kill' | 'remove'

export const actionLabels: Record<ActionName | 'recreate', string> = {
  start: 'Start', stop: 'Stop', restart: 'Restart', pause: 'Pause', unpause: 'Resume', kill: 'Kill', remove: 'Remove', recreate: 'Recreate',
}
export const actionPast: Record<ActionName, string> = { start: 'started', stop: 'stopped', restart: 'restarted', pause: 'paused', unpause: 'resumed', kill: 'killed', remove: 'removed' }

/** The lifecycle actions that make sense for a container in `state`, most useful first. */
export function actionsFor(state: string): ActionName[] {
  switch (state) {
    case 'running': return ['stop', 'restart', 'pause', 'kill', 'remove']
    case 'paused': return ['unpause', 'stop', 'kill', 'remove']
    case 'restarting': return ['stop', 'kill', 'remove']
    default: return ['start', 'remove']
  }
}

const tierRank: Record<string, number> = { read: 0, manage: 1, exec: 2, deploy: 3 }

/** Changing containers needs an operator or admin role and a host that allows management. */
export function canManage(role: string, hostTier: string | undefined): boolean {
  return (role === 'operator' || role === 'admin') && (tierRank[hostTier ?? 'read'] ?? 0) >= tierRank.manage
}

/** Why management is unavailable, or null when it is available. */
export function manageBlocker(role: string, hostTier: string | undefined): string | null {
  if (role !== 'operator' && role !== 'admin') return 'Your account can view containers but not change them.'
  if ((tierRank[hostTier ?? 'read'] ?? 0) < tierRank.manage) return 'Container management is not enabled for this host. Set VIGIRA_DOCKER_MODE=manage on its agent and allow it when the device is enrolled.'
  return null
}

/** Actions that destroy or interrupt work and so ask for confirmation. */
export function needsConfirmation(action: ActionName | 'recreate'): boolean {
  return action === 'kill' || action === 'remove' || action === 'recreate'
}

export type RecreateStep = { step: string; message?: string; layer?: string; current?: number; total?: number; result?: RecreateResult }
export type RecreateResult = { id: string; name: string; previousImageId?: string; imageId?: string; imageChanged: boolean; started: boolean; rolledBack?: boolean }

/** Splits a streamed NDJSON response into complete records, keeping a trailing partial line for the next read. */
export function drainRecords<T>(buffer: string): { records: T[]; rest: string } {
  const lines = buffer.split('\n')
  const rest = lines.pop() ?? ''
  const records: T[] = []
  for (const line of lines) {
    if (!line.trim()) continue
    try { records.push(JSON.parse(line) as T) } catch { /* Skip a malformed line. */ }
  }
  return { records, rest }
}

export type BulkOutcome = { row: FleetRow; ok: boolean; message: string }

/** Runs `task` over `items` with at most `limit` running at once, preserving input order in the results. */
export async function runLimited<T, R>(items: T[], limit: number, task: (item: T) => Promise<R>): Promise<R[]> {
  const results = new Array<R>(items.length)
  let next = 0
  const worker = async () => {
    while (next < items.length) {
      const index = next
      next += 1
      results[index] = await task(items[index])
    }
  }
  await Promise.all(Array.from({ length: Math.min(Math.max(1, limit), items.length) }, worker))
  return results
}

export function shortImageId(id: string | undefined): string {
  return id ? id.replace(/^sha256:/, '').slice(0, 12) : '–'
}
