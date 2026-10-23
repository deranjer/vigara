export const sections = ['devices', 'monitoring', 'backups', 'containers', 'stacks', 'images', 'networks', 'volumes', 'account'] as const
export type Section = (typeof sections)[number]

/**
 * A page in the app. `id` is the first selection (a device, or a container's host),
 * `sub` a second one (a container on that host), and `tab` a view within it.
 */
export type Route = { section: Section; id: string | null; sub: string | null; tab: string | null }

// Identifiers in the address bar are UUIDs, container names or short slugs; anything else is
// dropped so a crafted link cannot smuggle markup or path tricks into the page.
const idPattern = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/
const tabPattern = /^[a-z]{1,16}$/

export const defaultRoute: Route = { section: 'devices', id: null, sub: null, tab: null }

export function route(section: Section, id: string | null = null, sub: string | null = null, tab: string | null = null): Route {
  return { section, id, sub, tab }
}

function decodeSegment(raw: string | undefined, pattern: RegExp): string | null {
  if (!raw) return null
  try {
    const decoded = decodeURIComponent(raw)
    return pattern.test(decoded) ? decoded : null
  } catch { return null }
}

/** Reads a location hash such as "#/containers/host/web/logs" into a route, falling back to the default. */
export function parseRoute(hash: string): Route {
  const [rawSection, rawId, rawSub, rawTab] = hash.replace(/^#\/?/, '').split('/')
  const section = sections.find((candidate) => candidate === rawSection)
  if (!section) return defaultRoute
  const id = decodeSegment(rawId, idPattern)
  // Later segments only mean something when the earlier ones are valid.
  const sub = id ? decodeSegment(rawSub, idPattern) : null
  const tab = sub ? decodeSegment(rawTab, tabPattern) : null
  return { section, id, sub, tab }
}

/** The location hash for a route; the inverse of parseRoute for valid routes. */
export function formatRoute(value: Route): string {
  const parts: string[] = [value.section]
  if (value.id && idPattern.test(value.id)) {
    parts.push(encodeURIComponent(value.id))
    if (value.sub && idPattern.test(value.sub)) {
      parts.push(encodeURIComponent(value.sub))
      if (value.tab && tabPattern.test(value.tab)) parts.push(value.tab)
    }
  }
  return `#/${parts.join('/')}`
}

export function sameRoute(a: Route, b: Route): boolean {
  return a.section === b.section && a.id === b.id && a.sub === b.sub && a.tab === b.tab
}
