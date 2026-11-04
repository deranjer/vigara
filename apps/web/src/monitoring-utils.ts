export type Measurement = { name: string; value: number; unit: string; labels?: Record<string, string> }
export type MonitoringCard = { id: string; title: string; metrics: Measurement[]; trends: Measurement[]; capacity?: { total?: Measurement; free?: Measurement } }
export type MonitoringGroup = { id: string; title: string; cards: MonitoringCard[] }
export type MonitoringSection = { id: string; title: string; description: string; groups: MonitoringGroup[] }

export function labelKey(labels: Record<string, string> = {}) {
  return JSON.stringify(Object.entries(labels).sort(([a], [b]) => a.localeCompare(b)))
}

const labels: Record<string, string> = {
  cpu_used_percent: 'CPU use', memory_used_percent: 'Memory use', memory_available_bytes: 'Available', memory_total_bytes: 'Total',
  filesystem_free_bytes: 'Free', filesystem_total_bytes: 'Total', disk_read_bytes_per_second: 'Read', disk_write_bytes_per_second: 'Write',
  network_receive_bytes_per_second: 'Receive', network_send_bytes_per_second: 'Send', uptime_seconds: 'Uptime',
  docker_cpu_capacity_used_percent: 'CPU use', docker_memory_usage_bytes: 'Used', docker_memory_limit_bytes: 'Limit',
  docker_memory_used_percent: 'Memory use', docker_network_receive_bytes_per_second: 'Receive', docker_network_transmit_bytes_per_second: 'Send', docker_processes: 'Processes',
  network_check_success: 'Check success', network_check_latency_milliseconds: 'Latency',
}
export function measurementName(name: string) {
  return labels[name] ?? name.replaceAll('_', ' ').replace(/\b\w/g, part => part.toUpperCase())
}

// Only join volume capacity to I/O when the reported identifiers actually match.
// Linux mounts and physical devices cannot reliably be mapped from these labels.
function storageIdentity(metric: Measurement) {
  const { mount_path, device, ...remaining } = metric.labels ?? {}
  const scope = Object.fromEntries(Object.entries(remaining).filter(([key]) => key !== 'filesystem'))
  const raw = mount_path || device || 'Unspecified storage'
  const identity = /^[a-z]:[\\/]*$/i.test(raw) ? raw.slice(0, 2).toUpperCase() : raw
  return { id: labelKey({ ...scope, storage: identity }), title: [identity, ...Object.values(scope)].join(' · ') }
}

export function monitoringSections(metrics: Measurement[], containers: { id: string; name?: string }[] = []): MonitoringSection[] {
  const definitions = [
    ['system', 'CPU & memory', 'Compute usage and available memory.'],
    ['storage', 'Storage', 'Free space and read/write activity across your drives.'],
    ['network', 'Network', 'Receive and send traffic for each interface.'],
    ['checks', 'Connectivity checks', 'Reachability and response time by check.'],
    ['containers', 'Containers', 'Resource use grouped by container.'],
    ['synology', 'NAS health', 'System and disk measurements grouped by target.'],
    ['other', 'Other measurements', 'Additional measurements reported by this device.'],
  ]
  const sections: MonitoringSection[] = definitions.map(([id, title, description]) => ({ id: id!, title: title!, description: description!, groups: [] }))
  for (const metric of metrics) {
    const scope = metric.labels ?? {}
    let section = 'other'; let card = metric.name; let title = measurementName(metric.name)
    let groupId = labelKey(scope); let groupTitle = Object.values(scope).join(' · ')
    if (metric.name.startsWith('filesystem_') || metric.name.startsWith('disk_')) {
      section = 'storage'
      const identity = storageIdentity(metric); groupId = identity.id; groupTitle = identity.title
      card = metric.name.startsWith('filesystem_') ? 'capacity' : 'activity'; title = card === 'capacity' ? 'Disk space' : 'Disk activity'
      // Preserve filesystem label variants without merging their measurements.
      if (card === 'capacity') card += labelKey(scope)
    } else if (metric.name.startsWith('network_check_')) {
      section = 'checks'; card = metric.unit; title = metric.unit === 'ms' ? 'Response time' : 'Reachability'
    } else if (metric.name.startsWith('network_')) {
      section = 'network'; card = metric.unit; title = 'Traffic'
    } else if (metric.name.startsWith('docker_')) {
      section = 'containers'
      const { container_id, interface: networkInterface, ...rest } = scope
      groupId = labelKey({ ...rest, ...(container_id ? { container_id } : {}) })
      groupTitle = [containers.find(item => item.id === container_id)?.name || container_id?.slice(0, 12), ...Object.values(rest)].filter(Boolean).join(' · ') || 'Container'
      if (metric.name.startsWith('docker_memory_')) { card = 'memory'; title = 'Memory' }
      else if (metric.name.startsWith('docker_network_')) { card = `network:${networkInterface ?? ''}`; title = networkInterface ? `Traffic · ${networkInterface}` : 'Traffic' }
    } else if (metric.name.startsWith('synology_')) {
      section = 'synology'
    } else if (metric.name.startsWith('memory_')) {
      section = 'system'; card = 'memory'; title = 'Memory'
    } else if (metric.name.startsWith('cpu_') || metric.name === 'uptime_seconds') {
      section = 'system'
    }
    const target = sections.find(item => item.id === section)!
    let group = target.groups.find(item => item.id === groupId)
    if (!group) { group = { id: groupId, title: groupTitle, cards: [] }; target.groups.push(group) }
    let entry = group.cards.find(item => item.id === card)
    if (!entry) { entry = { id: card, title, metrics: [], trends: [] }; group.cards.push(entry) }
    entry.metrics.push(metric)
  }
  for (const section of sections) {
    section.groups.sort((a, b) => a.title.localeCompare(b.title))
    for (const group of section.groups) for (const card of group.cards) {
      card.metrics.sort((a, b) => a.name.localeCompare(b.name))
      const total = card.metrics.find(item => item.name === 'filesystem_total_bytes' || item.name === 'memory_total_bytes')
      const free = card.metrics.find(item => item.name === 'filesystem_free_bytes' || item.name === 'memory_available_bytes')
      if (total || free) card.capacity = { total, free }
      const preferred = free ?? card.metrics.find(item => item.name === 'docker_memory_usage_bytes') ?? card.metrics.find(item => item.name.endsWith('used_percent'))
      // Capacity is context, while available/used space is the changing signal.
      card.trends = preferred ? [preferred] : card.metrics.filter(item => !item.name.endsWith('_total_bytes') && !item.name.endsWith('_limit_bytes'))
    }
    for (const group of section.groups) group.cards.sort((a, b) => {
      const rank = (card: MonitoringCard) => card.capacity ? 0 : card.id === 'activity' ? 1 : 2
      return rank(a) - rank(b)
    })
  }
  return sections.filter(section => section.groups.length)
}
