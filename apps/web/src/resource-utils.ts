import { formatBytes } from './container-utils'

export type ResourceKind = 'image' | 'network' | 'volume'
export type UsedBy = { id: string; name: string }

export type ImageRow = { id: string; tags?: string[]; created: number; size: number; dangling: boolean; usedBy?: UsedBy[] }
export type NetworkRow = { id: string; name: string; driver: string; scope: string; internal: boolean; attachable: boolean; ipv6: boolean; subnets?: string[]; gateways?: string[]; builtin: boolean; created?: string; usedBy?: UsedBy[] }
export type VolumeRow = { name: string; driver: string; scope?: string; mountpoint?: string; created?: string; size: number; anonymous: boolean; labels?: Record<string, string>; usedBy?: UsedBy[] }

export type ImageDetail = ImageRow & { digests?: string[]; architecture?: string; os?: string; author?: string; layers: number; user?: string; workingDir?: string; entrypoint?: string[]; command?: string[]; exposedPorts?: string[]; envNames?: string[]; labels?: Record<string, string> }
export type NetworkDetail = NetworkRow & { members?: { id: string; name: string; ipv4?: string; ipv6?: string; mac?: string }[]; options?: Record<string, string>; labels?: Record<string, string> }
export type VolumeDetail = VolumeRow & { options?: Record<string, string> }
export type InspectResult = { kind: ResourceKind; image?: ImageDetail; network?: NetworkDetail; volume?: VolumeDetail }

export type PruneItem = { id: string; name?: string; size?: number }
export type PruneResult = { kind: ResourceKind; dryRun: boolean; candidates?: PruneItem[]; removed?: string[]; spaceReclaimed?: number }

export const plurals: Record<ResourceKind, string> = { image: 'images', network: 'networks', volume: 'volumes' }

export function resourcesPath(hostId: string, kind: ResourceKind): string {
  return `/api/docker/${encodeURIComponent(hostId)}/${plurals[kind]}`
}

export function shortId(id: string): string {
  return id.replace(/^sha256:/, '').slice(0, 12)
}

export function imageName(image: ImageRow): string {
  return image.tags && image.tags.length > 0 ? image.tags[0] : `<untagged> ${shortId(image.id)}`
}

/** A size for display; Docker does not always report one, and -1 marks that. */
export function sizeLabel(size: number | undefined): string {
  return size === undefined || size < 0 ? 'Unknown' : formatBytes(size)
}

export function usageLabel(usedBy: UsedBy[] | undefined): string {
  const count = usedBy?.length ?? 0
  return count === 0 ? 'Unused' : count === 1 ? usedBy![0].name : `${count} containers`
}

export function isInUse(usedBy: UsedBy[] | undefined): boolean {
  return (usedBy?.length ?? 0) > 0
}

export function filterBy<T>(rows: T[], search: string, text: (row: T) => string[]): T[] {
  const needle = search.trim().toLowerCase()
  return needle ? rows.filter((row) => text(row).some((value) => value.toLowerCase().includes(needle))) : rows
}

/** Why a resource cannot be removed right now, or null when it can. */
export function removeBlocker(kind: ResourceKind, row: { usedBy?: UsedBy[]; builtin?: boolean }): string | null {
  if (kind === 'network' && row.builtin) return 'Built-in networks cannot be removed.'
  if (isInUse(row.usedBy)) {
    const names = row.usedBy!.slice(0, 3).map((user) => user.name).join(', ')
    const more = row.usedBy!.length > 3 ? ` and ${row.usedBy!.length - 3} more` : ''
    return `In use by ${names}${more}. Stop and remove ${row.usedBy!.length === 1 ? 'that container' : 'those containers'} first.`
  }
  return null
}

/** The word a person must type to confirm removing something that holds data. */
export function confirmWord(kind: ResourceKind, name: string): string | null {
  return kind === 'volume' ? name : null
}

export function summarizePrune(items: PruneItem[]): string {
  if (items.length === 0) return 'Nothing to remove.'
  const total = items.reduce((sum, item) => sum + (item.size ?? 0), 0)
  return `${items.length} item${items.length === 1 ? '' : 's'}${total > 0 ? `, about ${formatBytes(total)}` : ''}`
}

export function pruneConfirmWord(kind: ResourceKind, count: number): string | null {
  return kind === 'volume' ? `delete ${count} volume${count === 1 ? '' : 's'}` : null
}

const imageRefPattern = /^[A-Za-z0-9][A-Za-z0-9_.:/@-]{0,254}$/

/** The same rule the server applies to an image reference to pull. */
export function pullRefProblem(ref: string): string | null {
  const value = ref.trim()
  if (!value) return 'Enter an image name, such as nginx:alpine.'
  if (value.startsWith('sha256:')) return 'An image ID cannot be pulled; enter a name such as nginx:alpine.'
  if (!imageRefPattern.test(value) || /\/(\/|\.|$)/.test(value)) return 'That is not a valid image name.'
  return null
}

export function pruneDescription(kind: ResourceKind, all: boolean): string {
  switch (kind) {
    case 'image': return all ? 'every image that no container uses, including tagged ones' : 'untagged ("dangling") images that no container uses'
    case 'network': return 'networks that no container is attached to (built-in networks are never removed)'
    default: return all ? 'every volume that no container uses, including named volumes' : 'anonymous volumes that no container uses'
  }
}

export function sortRows<T>(rows: T[], key: (row: T) => string | number, descending = false): T[] {
  return [...rows].sort((a, b) => {
    const left = key(a)
    const right = key(b)
    const order = typeof left === 'number' && typeof right === 'number' ? left - right : String(left).localeCompare(String(right))
    return descending ? -order : order
  })
}
