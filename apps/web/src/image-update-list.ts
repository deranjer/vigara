export type ImageUpdateRow = {
  agentId: string
  containerId: string
  device: string
  stackId: string
  ignored: boolean
  checkedAt: string
  result: { name: string; image: string; runningImage?: string; status: string; platform?: string }
}
export type ImageUpdateSort = 'container' | 'device' | 'image' | 'status' | 'checked'
export type ImageUpdateFilters = { search: string; device: string; status: string; scope: string; sort: ImageUpdateSort; descending: boolean }
export const defaultImageUpdateFilters: ImageUpdateFilters = { search: '', device: '', status: '', scope: '', sort: 'container', descending: false }
const compareText = (a: string, b: string) => a.localeCompare(b, 'en', { numeric: true, sensitivity: 'base' })
const statusRank: Record<string, number> = { available: 0, unknown: 1, pinned: 2, current: 3, ignored: 4 }
const statusOf = (item: ImageUpdateRow) => item.ignored ? 'ignored' : item.result.status

export function selectImageUpdates<T extends ImageUpdateRow>(items: T[], filters: ImageUpdateFilters): T[] {
  const search = filters.search.trim().toLocaleLowerCase()
  return items.filter(item => {
    if (filters.device && item.agentId !== filters.device) return false
    if (filters.status && statusOf(item) !== filters.status) return false
    if (filters.scope === 'managed' && !item.stackId || filters.scope === 'external' && item.stackId) return false
    return !search || [item.result.name, item.containerId, item.device, item.result.image, item.result.runningImage, item.result.platform, item.stackId].some(value => value?.toLocaleLowerCase().includes(search))
  }).sort((a, b) => {
    let primary = 0
    switch (filters.sort) {
      case 'container': primary = compareText(a.result.name || a.containerId, b.result.name || b.containerId); break
      case 'device': primary = compareText(a.device, b.device); break
      case 'image': primary = compareText(a.result.image, b.result.image); break
      case 'status': primary = (statusRank[statusOf(a)] ?? 1) - (statusRank[statusOf(b)] ?? 1); break
      case 'checked': primary = (Date.parse(a.checkedAt) || 0) - (Date.parse(b.checkedAt) || 0); break
    }
    if (primary) return filters.descending ? -primary : primary
    return compareText(a.result.name || a.containerId, b.result.name || b.containerId) || compareText(a.device, b.device) || compareText(`${a.agentId}/${a.containerId}`, `${b.agentId}/${b.containerId}`)
  })
}
