export const sections = ['devices', 'monitoring', 'backups', 'containers'] as const
export type Section = (typeof sections)[number]
export type Route = { section: Section; id: string | null }

// Identifiers in the address bar are UUIDs or short slugs; anything else is dropped
// so a crafted link cannot smuggle markup or path tricks into the page.
const idPattern = /^[A-Za-z0-9._-]{1,64}$/

export const defaultRoute: Route = { section: 'devices', id: null }

/** Reads a location hash such as "#/containers/abc" into a route, falling back to the default. */
export function parseRoute(hash: string): Route {
  const [rawSection, rawId] = hash.replace(/^#\/?/, '').split('/')
  const section = sections.find((candidate) => candidate === rawSection)
  if (!section) return defaultRoute
  let id: string | null = null
  if (rawId) {
    try {
      const decoded = decodeURIComponent(rawId)
      if (idPattern.test(decoded)) id = decoded
    } catch { /* A malformed escape is treated as no selection. */ }
  }
  return { section, id }
}

/** The location hash for a route; the inverse of parseRoute for valid routes. */
export function formatRoute(route: Route): string {
  return `#/${route.section}${route.id && idPattern.test(route.id) ? `/${encodeURIComponent(route.id)}` : ''}`
}

export function sameRoute(a: Route, b: Route): boolean {
  return a.section === b.section && a.id === b.id
}
