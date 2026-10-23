export type HardwareComponent = { name?: string; manufacturer?: string; model?: string; version?: string; slot?: string; capacityBytes?: number; speedMTs?: number }
export type HardwareInventory = { observedAt?: string; gpuStatus?: string; gpus?: HardwareComponent[]; memoryStatus?: string; memoryModules?: HardwareComponent[]; boardStatus?: string; motherboard?: HardwareComponent; firmwareStatus?: string; firmware?: HardwareComponent }
type Metric = { name: string; value: number; labels?: Record<string, string> }

export function driveUsage(metrics: Metric[] = []) {
  const key = (metric: Metric) => JSON.stringify(Object.entries(metric.labels ?? {}).sort(([a], [b]) => a.localeCompare(b)))
  const free = new Map(metrics.filter(m => m.name === 'filesystem_free_bytes').map(m => [key(m), m.value]))
  return metrics.filter(m => m.name === 'filesystem_total_bytes' && Number.isFinite(m.value) && m.value > 0).map(m => {
    const available = free.get(key(m))
    const valid = available !== undefined && Number.isFinite(available) && available >= 0 && available <= m.value
    return { key: key(m), mount: m.labels?.mount_path || m.labels?.device || 'Filesystem', filesystem: m.labels?.filesystem, total: m.value, free: valid ? available : null, used: valid ? m.value - available : null }
  }).sort((a, b) => a.mount.localeCompare(b.mount))
}

export function operatingSystem(value: string) {
  const os = value.toLowerCase()
  if (os.includes('windows')) return 'windows'
  if (/linux|ubuntu|debian|synology|fedora|centos|arch|alpine/.test(os)) return 'linux'
  if (/darwin|macos|mac os/.test(os)) return 'macos'
  return 'unknown'
}
