import { describe, expect, it } from 'vitest'
import { stackBrowserLinks } from './stack-browser-links'
import type { StackHost } from './stack-list-utils'

const stack = { agentId: 'server', projectName: 'photos' }
const host: StackHost = {
  agentId: 'server', name: 'Home server', state: 'ok', hostAddresses: ['192.168.1.2', 'fd00::2'],
  containers: [{
    state: 'running', project: 'photos', caddySites: ['photos.example.com', 'https://photos.example.com/', 'http://other.example.com:8080/gallery'],
    ports: [
      { type: 'tcp', privatePort: 80, publicPort: 8080, ip: '0.0.0.0' },
      { type: 'tcp', privatePort: 80, publicPort: 8080, ip: '192.168.1.2' },
      { type: 'tcp', privatePort: 443, publicPort: 8443, ip: '::' },
    ],
  }],
}

describe('stack browser links', () => {
  it('prefers Caddy and lists unique direct addresses for the published bind families', () => {
    expect(stackBrowserLinks(stack, [host])).toEqual([
      { kind: 'Caddy', href: 'https://photos.example.com/' },
      { kind: 'Caddy', href: 'http://other.example.com:8080/gallery' },
      { kind: 'Direct port', href: 'http://192.168.1.2:8080/' },
      { kind: 'Direct port', href: 'https://[fd00::2]:8443/' },
    ])
  })

  it('ignores unsafe or ambiguous sites and ports the browser cannot address', () => {
    const container = {
      state: 'running', project: 'photos',
      caddySites: ['*.example.com', ':443', 'ftp://example.com', 'https://user@example.com', '//other.example.com', 'photos.example.com:80'],
      ports: [
        { type: 'tcp', privatePort: 80, publicPort: 8080, ip: '127.0.0.1' },
        { type: 'tcp', privatePort: 80, publicPort: 8080, ip: '::1' },
        { type: 'tcp', privatePort: 80, publicPort: 8080, ip: '0.0.0.0' },
        { type: 'tcp', privatePort: 80 },
        { type: 'udp', privatePort: 80, publicPort: 8080, ip: '192.168.1.2' },
        { type: 'tcp', privatePort: 80, publicPort: 80, ip: '192.168.1.2' },
      ],
    }
    expect(stackBrowserLinks(stack, [{ ...host, hostAddresses: undefined, containers: [container] }])).toEqual([
      { kind: 'Caddy', href: 'http://photos.example.com/' },
      { kind: 'Direct port', href: 'http://192.168.1.2/' },
    ])
  })

  it('uses only running containers from the stack project on its assigned device', () => {
    const containers = [{ ...host.containers[0], state: 'exited' }, { ...host.containers[0], project: 'different' }]
    expect(stackBrowserLinks(stack, [{ ...host, containers }, { ...host, agentId: 'another' }])).toEqual([])
    expect(stackBrowserLinks(stack, [{ ...host, state: 'offline' }])).toEqual([])
  })
})
