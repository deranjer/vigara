import type { DockerPort } from './container-utils'
import type { StackHost, StackSummary } from './stack-list-utils'

type BrowserLink = { kind: 'Caddy' | 'Direct port'; href: string }

function caddyURL(site: string): URL | null {
  // Wildcards and matcher expressions do not identify a browser destination.
  if (!site || /[\s*{}\\@?#]/.test(site) || site.startsWith('/')) return null
  const explicitScheme = site.includes('://')
  const url = URL.parse(explicitScheme ? site : `https://${site}`)
  if (!url || !['https:', 'http:'].includes(url.protocol) || !url.hostname || url.username || url.password) return null
  if (!explicitScheme && url.port === '80') {
    url.protocol = 'http:'
    url.port = ''
  }
  return url
}

function addressLiteral(address: string): string | null {
  const literal = address.includes(':') ? `[${address}]` : address
  if (!/^(?:\d+\.\d+\.\d+\.\d+|\[[\da-f:.]+\])$/i.test(literal)) return null
  const ip = URL.parse(`http://${literal}`)?.hostname
  if (!ip || /^(?:127\.|169\.254\.|0\.)/.test(ip) || ['[::]', '[::1]'].includes(ip) || /^\[(?:fe[89ab]|::ffff:)/i.test(ip)) return null
  return ip
}

function directURLs(port: DockerPort, hostAddresses: string[]): URL[] {
  const published = port.publicPort
  if (port.type !== 'tcp' || !published || !Number.isInteger(published) || published < 1 || published > 65535) return []
  const bind = port.ip
  const addresses = !bind || bind === '0.0.0.0' || bind === '::' ? hostAddresses.filter((address) => (
    !bind || address.includes(':') === (bind === '::')
  )) : [bind]
  const scheme = [443, 8443, 9443].includes(port.privatePort) ? 'https' : 'http'
  return addresses.flatMap((address) => {
    const host = addressLiteral(address)
    return host ? [new URL(`${scheme}://${host}:${published}`)] : []
  })
}

export function stackBrowserLinks(stack: Pick<StackSummary, 'agentId' | 'projectName'>, hosts: StackHost[]): BrowserLink[] {
  const host = hosts.find((item) => item.agentId === stack.agentId && item.state === 'ok')
  if (!host || !stack.projectName) return []
  const containers = host.containers.filter((container) => container.project === stack.projectName && container.state === 'running')
  const links = new Map<string, BrowserLink>()
  function add(url: URL, kind: BrowserLink['kind']) {
    if (!links.has(url.href)) links.set(url.href, { kind, href: url.href })
  }
  for (const container of containers) {
    for (const site of container.caddySites ?? []) {
      const url = caddyURL(site)
      if (url) add(url, 'Caddy')
    }
  }
  for (const container of containers) {
    for (const port of container.ports ?? []) {
      for (const url of directURLs(port, host.hostAddresses ?? [])) add(url, 'Direct port')
    }
  }
  return [...links.values()]
}
