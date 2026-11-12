import { describe, expect, it } from 'vitest'
import { bulkStacks, isReverseProxy, matchesStackFilter, stackActionTargets, stackDrift, stackFilterCounts, stackLabels, stacksWithUpdates, type StackHost, type StackSummary } from './stack-list-utils'

const stack = (id: string, extra: Partial<StackSummary> = {}): StackSummary => ({ id, name: id, projectName: id, agentId: 'h1', commit: 'abc1234', valid: true, assigned: true, serviceCount: 2, deploymentStatus: 'succeeded', deploymentCommit: 'abc1234', ...extra })
const host = (containers: StackHost['containers'], extra: Partial<StackHost> = {}): StackHost[] => [{ agentId: 'h1', name: 'nas', state: 'ok', dockerTier: 'manage', containers, ...extra }]
const running = (project: string, ...services: string[]) => services.map((service) => ({ id: `${project}-${service}`, project, service, state: 'running' }))

describe('stack labels', () => {
  it('shows no labels for a stack deployed as saved', () => {
    expect(stackLabels(stack('blog'), host(running('blog', 'web', 'db')), new Set())).toEqual([])
  })

  it('labels drafts, updates, failures and saved-but-not-deployed revisions', () => {
    const keys = (s: StackSummary, updates: string[] = []) => stackLabels(s, host(running(s.projectName, 'web', 'db')), new Set(updates)).map((label) => label.key)
    expect(keys(stack('a', { draft: true }), ['a'])).toEqual(['draft', 'update'])
    expect(keys(stack('b', { deploymentStatus: 'failed' }))).toEqual(['failed'])
    expect(keys(stack('c', { commit: '' }))).toEqual(['draft'])
    expect(keys(stack('d', { deploymentCommit: 'old0000' }))).toEqual(['saved'])
    expect(keys(stack('e', { assigned: false }))).toEqual(['unassigned'])
  })

  it('reports drift when running services differ from the saved Compose file', () => {
    expect(stackDrift(stack('blog'), host(running('blog', 'web')))).toBe('1 of 2 saved services have no container')
    expect(stackDrift(stack('blog'), host(running('blog', 'web', 'db', 'cache')))).toBe('1 more service running than the saved Compose file defines')
    // Not deployed as saved, or an incomplete list: no claim either way.
    expect(stackDrift(stack('blog', { deploymentStatus: 'failed' }), host(running('blog', 'web')))).toBeNull()
    expect(stackDrift(stack('blog'), host(running('blog', 'web'), { truncated: true }))).toBeNull()
    expect(stackDrift(stack('blog'), host([{ id: 'x', project: 'blog', state: 'running' }]))).toBeNull()
  })

  it('counts filters, with failures and drift under Needs attention', () => {
    const labels = [stack('a'), stack('b', { draft: true }), stack('c', { deploymentStatus: 'failed' }), stack('d', { deploymentCommit: 'old0000' })].map((s) => stackLabels(s, host(running(s.projectName, 'web', 'db')), new Set()))
    expect(stackFilterCounts(labels)).toEqual({ all: 4, attention: 2, draft: 1, update: 0, drift: 0, failed: 1 })
    expect(matchesStackFilter(labels[3], 'attention')).toBe(false)
  })

  it('reads image updates for stacks, ignoring muted containers', () => {
    const ids = stacksWithUpdates([{ stackId: 'a', result: { status: 'available' } }, { stackId: 'b', ignored: true, result: { status: 'available' } }, { stackId: 'c', result: { status: 'current' } }, { stackId: '', result: { status: 'available' } }])
    expect([...ids]).toEqual(['a'])
  })
})

describe('stack bulk actions', () => {
  it('leaves Vigara out of Start all / Stop all unless asked, and spots reverse proxies', () => {
    const stacks = [stack('blog'), stack('vigara'), stack('caddy'), stack('draft', { commit: '' })]
    const hosts = host([...running('blog', 'web'), ...running('vigara', 'app'), ...running('caddy', 'caddy')])
    expect(bulkStacks(stacks, hosts, false).map((s) => s.id)).toEqual(['blog', 'caddy'])
    expect(bulkStacks(stacks, hosts, true).map((s) => s.id)).toEqual(['blog', 'vigara', 'caddy'])
    expect(stacks.filter(isReverseProxy).map((s) => s.id)).toEqual(['caddy'])
    expect(isReverseProxy(stack('media-traefik'))).toBe(true)
    expect(isReverseProxy(stack('npmjs-mirror'))).toBe(false)
  })

  it('restarts only running containers', () => {
    const hosts = host([...running('blog', 'web'), { id: 'blog-db', project: 'blog', service: 'db', state: 'exited' }])
    expect(stackActionTargets(stack('blog'), hosts, 'restart').map((c) => c.id)).toEqual(['blog-web'])
  })
})
