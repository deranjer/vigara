import { bucketOf } from './alerts-utils'
import { route, type Route } from './route'
import type { StatusTone } from './status'

// Shapes of the API responses the Overview reads. Only the fields used here are listed.
export type OverviewMetric = { name: string; value: number; unit: string; labels?: Record<string, string> }
export type OverviewDevice = {
  id: string; name: string; status: string; agentId?: string | null; lastSeenAt?: string | null
  latest?: { host?: { cpuModel?: string; logicalCpus?: number; totalMemoryBytes?: number }; metrics?: OverviewMetric[]; dockerContainers?: { id: string; name?: string; image?: string; state: string }[] }
}
export type OverviewIncident = { id: string; status: string; summary: string; dimension: string; startedAt: string; resolvedAt?: string; acknowledgedAt?: string; mutedUntil?: string }
export type OverviewUpdate = { agentId: string; device: string; ignored: boolean; result: { name: string; status: string } }
export type OverviewStack = { id: string; name: string; draft?: boolean; deploymentStatus?: string; deploymentAt?: string; deploymentCommit?: string; deviceName?: string }
export type OverviewHost = { agentId: string; name: string; state: string; containers: { name?: string; state: string; status?: string }[] }
export type OverviewAudit = { id: number; occurredAt: string; actorName: string; action: string; targetKind?: string; targetId?: string; outcome: string }

export type DeviceRow = { id: string; name: string; tone: StatusTone; status: string; detail: string; cpu: number | null; memory: number | null; disk: number | null; diskMount: string | null; containers: number | null; lastSeenAt: string | null }
export type ContainerUsage = { deviceId: string; device: string; name: string; memoryBytes: number; cpu: number | null }
export type AttentionItem = { key: string; tone: StatusTone; title: string; detail: string; action: string; to: Route }
export type ActivityItem = { key: string; tone: StatusTone; text: string; at: string; to?: Route }

/** The server sends Go's zero time ("0001-01-01…") for "never"; treat it, and anything unparsable, as missing. */
export function realDate(at: string | null | undefined): string | null {
  if (!at) return null
  const time = Date.parse(at)
  return Number.isNaN(time) || new Date(time).getUTCFullYear() < 2000 ? null : at
}

const metric = (device: OverviewDevice, name: string) => device.latest?.metrics?.find((m) => m.name === name && !m.labels?.container_id)?.value ?? null

export function deviceTone(status: string): StatusTone {
  return status === 'healthy' ? 'ok' : status === 'stale' ? 'warn' : status === 'failed' ? 'crit' : 'idle'
}

/** The fullest filesystem on a device, as a percentage used. */
export function worstDisk(device: OverviewDevice): { percent: number; mount: string } | null {
  const metrics = device.latest?.metrics ?? []
  let worst: { percent: number; mount: string } | null = null
  for (const total of metrics.filter((m) => m.name === 'filesystem_total_bytes' && m.value > 0)) {
    const mount = total.labels?.mount_path ?? ''
    const free = metrics.find((m) => m.name === 'filesystem_free_bytes' && m.labels?.mount_path === mount)
    if (!free) continue
    const percent = Math.max(0, Math.min(100, (1 - free.value / total.value) * 100))
    if (!worst || percent > worst.percent) worst = { percent, mount }
  }
  return worst
}

export function deviceRows(devices: OverviewDevice[]): DeviceRow[] {
  return devices.map((device) => {
    const host = device.latest?.host
    const disk = worstDisk(device)
    const containers = device.latest?.dockerContainers
    const memoryGb = host?.totalMemoryBytes ? `${Math.round(host.totalMemoryBytes / 2 ** 30)} GB` : ''
    return {
      id: device.id, name: device.name, tone: deviceTone(device.status), status: device.status,
      detail: [host?.cpuModel?.replace(/\(R\)|\(TM\)|CPU|with Radeon Graphics|@.*$/g, '').replace(/\s+/g, ' ').trim(), host?.logicalCpus ? `${host.logicalCpus} vCPU` : '', memoryGb].filter(Boolean).join(' · '),
      cpu: metric(device, 'cpu_used_percent'), memory: metric(device, 'memory_used_percent'),
      disk: disk?.percent ?? null, diskMount: disk?.mount ?? null,
      containers: containers ? containers.filter((c) => c.state === 'running').length : null,
      lastSeenAt: realDate(device.lastSeenAt),
    }
  })
}

/** Containers across every device, by memory in the latest telemetry, largest first. */
export function topContainers(devices: OverviewDevice[], limit = 5): ContainerUsage[] {
  const rows: ContainerUsage[] = []
  for (const device of devices) {
    const names = new Map((device.latest?.dockerContainers ?? []).map((c) => [c.id, c.name || c.id.slice(0, 12)]))
    const metrics = device.latest?.metrics ?? []
    for (const m of metrics) {
      const id = m.labels?.container_id
      if (m.name !== 'docker_memory_usage_bytes' || !id) continue
      const cpu = metrics.find((other) => other.name === 'docker_cpu_capacity_used_percent' && other.labels?.container_id === id)?.value ?? null
      rows.push({ deviceId: device.id, device: device.name, name: names.get(id) ?? id.slice(0, 12), memoryBytes: m.value, cpu })
    }
  }
  return rows.sort((a, b) => b.memoryBytes - a.memoryBytes).slice(0, limit)
}

const listNames = (names: string[], max = 3) => names.length <= max ? names.join(', ').replace(/, ([^,]*)$/, ' and $1') : `${names.slice(0, max).join(', ')} and ${names.length - max} more`

/** What needs the user, most serious first. Every item links to the page that resolves it. */
export function attentionItems(input: { devices: OverviewDevice[] | null; incidents: OverviewIncident[] | null; updates: OverviewUpdate[] | null; stacks: OverviewStack[] | null; hosts: OverviewHost[] | null; policies: number | null }): AttentionItem[] {
  const items: AttentionItem[] = []
  // Acknowledged and muted alerts have been seen; only open ones need attention.
  const firing = input.incidents?.filter((i) => bucketOf(i) === 'open') ?? []
  if (firing.length) items.push({ key: 'alerts', tone: 'crit', title: firing.length === 1 ? firing[0].summary : `${firing.length} alerts are open`, detail: firing.length === 1 ? `Since ${new Date(firing[0].startedAt).toLocaleString()}` : listNames(firing.map((i) => i.summary), 2), action: 'Open alerts', to: route('alerts') })
  for (const device of input.devices ?? []) {
    if (device.status === 'failed' || device.status === 'stale') items.push({ key: `device:${device.id}`, tone: device.status === 'failed' ? 'crit' : 'warn', title: `${device.name} is ${device.status === 'stale' ? 'not checking in' : 'failing to report'}`, detail: device.lastSeenAt ? `Last check-in ${new Date(device.lastSeenAt).toLocaleString()}` : 'No check-in recorded', action: 'Open device', to: route('devices', device.id) })
    const disk = worstDisk(device)
    if (disk && disk.percent >= 85) items.push({ key: `disk:${device.id}`, tone: disk.percent >= 90 ? 'crit' : 'warn', title: `${device.name} ${disk.mount} is ${Math.round(disk.percent)}% full`, detail: 'Free up space or grow the volume before writes start failing', action: 'Open device', to: route('devices', device.id) })
  }
  for (const stack of input.stacks ?? []) {
    if (stack.deploymentStatus === 'failed') items.push({ key: `deploy:${stack.id}`, tone: 'crit', title: `${stack.name || stack.id} failed to deploy`, detail: realDate(stack.deploymentAt) ? `Last attempt ${new Date(stack.deploymentAt!).toLocaleString()}` : 'See the stack for details', action: 'Open stack', to: route('stacks', stack.id) })
  }
  for (const host of input.hosts ?? []) {
    const bad = host.containers.filter((c) => c.state === 'restarting' || c.status?.includes('(unhealthy)'))
    if (bad.length) items.push({ key: `containers:${host.agentId}`, tone: 'warn', title: `${listNames(bad.map((c) => c.name ?? 'unnamed'))} ${bad.length === 1 ? 'is' : 'are'} ${bad.every((c) => c.state === 'restarting') ? 'restarting' : 'unhealthy or restarting'}`, detail: `On ${host.name}`, action: bad.length === 1 ? 'Open container' : 'View containers', to: bad.length === 1 && bad[0].name ? route('containers', host.agentId, bad[0].name) : route('containers') })
  }
  const available = input.updates?.filter((u) => !u.ignored && u.result.status === 'available') ?? []
  if (available.length) items.push({ key: 'updates', tone: 'warn', title: `${listNames([...new Set(available.map((u) => u.result.name))])} ${available.length === 1 ? 'has a newer image' : 'have newer images'}`, detail: 'Found by the image update check', action: 'Review updates', to: route('updates') })
  for (const stack of input.stacks ?? []) {
    if (stack.draft) items.push({ key: `draft:${stack.id}`, tone: 'warn', title: `${stack.name || stack.id} has an unsaved draft`, detail: stack.deviceName ? `On ${stack.deviceName}` : 'Not deployed yet', action: 'Open draft', to: route('stacks', stack.id) })
  }
  if (input.policies === 0) items.push({ key: 'backups', tone: 'idle', title: 'Backups aren’t set up', detail: 'Snapshots of volumes and bind mounts, on a schedule', action: 'Set up backups', to: route('backups') })
  const rank: Record<StatusTone, number> = { crit: 0, warn: 1, info: 2, idle: 3, ok: 4 }
  return items.sort((a, b) => rank[a.tone] - rank[b.tone])
}

const humanAction = (action: string) => action.replace(/^[a-z]+\./, '').replace(/[._]/g, ' ')

/** Recent events from the audit log, alert incidents and stack deployments, newest first. */
export function activityItems(input: { audit: OverviewAudit[] | null; incidents: OverviewIncident[] | null; stacks: OverviewStack[] | null }, limit = 15): ActivityItem[] {
  const items: ActivityItem[] = []
  for (const entry of input.audit ?? []) {
    if (entry.action.startsWith('scheduled_task.run') || entry.action.startsWith('mcp.')) continue
    const area = entry.action.split('.')[0]
    const failed = entry.outcome !== 'success'
    const what = humanAction(entry.action)
    const text = `${area.charAt(0).toUpperCase()}${area.slice(1)} ${what}${entry.targetId ? `: ${entry.targetId}` : ''} · ${entry.actorName || 'system'}${failed ? ' (failed)' : ''}`
    items.push({ key: `audit:${entry.id}`, tone: failed ? 'crit' : 'info', text, at: entry.occurredAt, to: area === 'stack' && entry.targetId ? route('stacks', entry.targetId) : undefined })
  }
  for (const incident of input.incidents ?? []) {
    items.push({ key: `incident:${incident.id}`, tone: 'crit', text: `Alert: ${incident.summary}`, at: incident.startedAt, to: route('alerts') })
    if (incident.resolvedAt) items.push({ key: `resolved:${incident.id}`, tone: 'ok', text: `Resolved: ${incident.summary}`, at: incident.resolvedAt, to: route('alerts') })
  }
  for (const stack of input.stacks ?? []) {
    if (!realDate(stack.deploymentAt)) continue
    const failed = stack.deploymentStatus === 'failed'
    items.push({ key: `deploy:${stack.id}`, tone: failed ? 'crit' : 'ok', text: `${failed ? 'Deploy failed' : 'Deployed'}: ${stack.name || stack.id}${stack.deploymentCommit ? ` · ${stack.deploymentCommit.slice(0, 7)}` : ''}`, at: stack.deploymentAt!, to: route('stacks', stack.id) })
  }
  return items.filter((item) => realDate(item.at)).sort((a, b) => new Date(b.at).getTime() - new Date(a.at).getTime()).slice(0, limit)
}

/** "just now", "5 min ago", "3 h ago", "2 d ago". */
export function relativeTime(at: string | null | undefined, now = Date.now()): string {
  if (!at) return '—'
  const seconds = Math.max(0, Math.round((now - new Date(at).getTime()) / 1000))
  if (seconds < 45) return 'just now'
  if (seconds < 3600) return `${Math.round(seconds / 60)} min ago`
  if (seconds < 86400) return `${Math.round(seconds / 3600)} h ago`
  return `${Math.round(seconds / 86400)} d ago`
}

export function greeting(hour: number): string {
  return hour < 5 ? 'Good evening' : hour < 12 ? 'Good morning' : hour < 18 ? 'Good afternoon' : 'Good evening'
}
