import { MantineProvider } from '@mantine/core'
import { renderToStaticMarkup } from 'react-dom/server'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { loadStackRuntime, StackRuntimeView, type RuntimeData } from './stack-runtime'

const stack = { id: 'cookira', name: 'Cookira', projectName: 'cookira', commit: 'abc', valid: true, assigned: true, agentId: 'host-a', serviceCount: 2, deploymentStatus: 'succeeded', deploymentCommit: 'abc' }
const container = { id: 'web-id', name: 'cookira-web', project: 'cookira', service: 'web', image: 'example/web:latest', state: 'exited', status: 'Exited (1)', created: 1 }
const inspect = { id: 'web-id', name: 'cookira-web', image: 'example/web:latest', state: { status: 'exited', running: false, paused: false, restarting: false, oomKilled: false, exitCode: 1, error: 'Port 8080: address already in use' }, env: [], envRevealed: false, tty: false, privileged: false, readonlyRoot: false, restartCount: 3 }
const data: RuntimeData = { stack, hosts: [{ agentId: 'host-a', name: 'Docker host', state: 'ok', dockerTier: 'deploy', containers: [container] }], containers: [{ container, inspect }] }

const render = (value: RuntimeData, role = 'admin') => renderToStaticMarkup(<MantineProvider><StackRuntimeView data={value} role={role} busy={null} loading={false} error="" outcomes={[]} onAction={() => {}} onRefresh={() => {}} onNavigate={() => {}} /></MantineProvider>)
const button = (html: string, label: string) => html.match(/<button\b[^>]*>[\s\S]*?<\/button>/g)?.find((button) => button.includes(`>${label}<`)) ?? ''

afterEach(() => vi.unstubAllGlobals())

describe('opened stack container details', () => {
  it('shows container errors, exit information, missing services and the card controls', () => {
    const html = render(data)
    for (const text of ['Port 8080: address already in use', 'Container error', 'Exit code: 1', 'Restarts: 3', 'Missing containers', '1 of 2 saved services', 'Container details', 'Logs', 'cookira-web', 'example/web:latest']) expect(html).toContain(text)
    expect(button(html, 'Start')).not.toContain('disabled')
    expect(button(html, 'Stop')).toContain('disabled')
    expect(html).toContain('Use Preview deployment')
  })

  it('disables controls for unavailable hosts, partial inventories and viewers', () => {
    for (const value of [
      { ...data, hosts: [{ ...data.hosts[0]!, state: 'offline' as const, message: 'Agent disconnected' }], containers: [] },
      { ...data, hosts: [{ ...data.hosts[0]!, truncated: true }] },
    ]) {
      const html = render(value)
      expect(button(html, 'Start')).toContain('disabled')
      expect(button(html, 'Stop')).toContain('disabled')
    }
    expect(button(render(data, 'viewer'), 'Start')).toContain('disabled')
  })

  it('shows application exit and inspection failures without hiding the container', () => {
    const html = render({ ...data, containers: [{ container, inspect: { ...inspect, state: { ...inspect.state, error: '' } } }, { container: { ...container, id: 'other' }, error: 'Container no longer exists' }] })
    expect(html).toContain('Open logs for the application error')
    expect(html).toContain('Container no longer exists')
    expect(html).toContain('Could not inspect container')
  })

  it('inspects only the assigned device and Compose project, without revealing environment values', async () => {
    const hosts = [data.hosts[0]!, { ...data.hosts[0]!, agentId: 'host-b' }]
    hosts[0] = { ...hosts[0]!, containers: [container, { ...container, id: 'foreign', project: 'another-project' }] }
    const fetcher = vi.fn(async (path: string) => {
      if (path === '/api/stacks') return Response.json({ stacks: [stack] })
      if (path === '/api/containers') return Response.json({ hosts })
      if (path === '/api/containers/host-a/web-id/inspect') return Response.json(inspect)
      throw new Error(`Unexpected request ${path}`)
    })
    vi.stubGlobal('fetch', fetcher)
    const result = await loadStackRuntime('cookira')
    expect(result.containers).toHaveLength(1)
    expect(result.containers[0]?.inspect?.state.error).toContain('address already in use')
    expect(fetcher.mock.calls.map(([path]) => path)).toEqual(['/api/stacks', '/api/containers', '/api/containers/host-a/web-id/inspect'])
    fetcher.mockClear()
    await loadStackRuntime('cookira', result.stack)
    expect(fetcher.mock.calls.map(([path]) => path)).toEqual(['/api/containers', '/api/containers/host-a/web-id/inspect'])
  })
})
