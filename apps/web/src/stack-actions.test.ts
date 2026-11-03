import { beforeEach, describe, expect, it, vi } from 'vitest'
import { postAction } from './container-actions'
import { runStackActions } from './stack-actions'
import type { StackHost, StackSummary } from './stack-list-utils'
vi.mock('./container-actions', () => ({ postAction: vi.fn() }))
const post = vi.mocked(postAction)
const stack = (id: string, extra: Partial<StackSummary> = {}): StackSummary => ({ id, name: id, projectName: id, agentId: 'host', commit: 'saved', assigned: true, valid: true, serviceCount: 1, deploymentStatus: 'succeeded', ...extra })
const inventory = (stacks: StackSummary[], state = 'exited'): StackHost[] => [{ agentId: 'host', name: 'Host', state: 'ok', dockerTier: 'manage', containers: stacks.map(s => ({ id: s.id + '-container', name: s.id, project: s.projectName, state })) }]
beforeEach(() => { post.mockReset(); post.mockResolvedValue({ id: '', name: '', action: '', changed: true }) })
describe('stack actions', () => {
 it('starts every deployed stack across pages, excluding protected projects and drafts', async () => {
  const stacks = [...Array.from({ length: 25 }, (_, i) => stack('app-' + i)), stack('vigara'), stack('proxy', { projectName: 'vigara-caddy' }), stack('draft', { commit: '' })]
  const results = await runStackActions(stacks, inventory(stacks), 'admin', 'start')
  expect(results).toHaveLength(25)
  expect(post).toHaveBeenCalledTimes(25)
  expect(post).not.toHaveBeenCalledWith('host', 'vigara-container', expect.anything())
  expect(post).not.toHaveBeenCalledWith('host', 'proxy-container', expect.anything())
  expect(post).toHaveBeenCalledWith('host', 'app-24-container', 'start')
 })
 it('stops all containers in the selected stack only, and leaves individual protected-stack controls available', async () => {
  const stacks = [stack('vigara'), stack('other')]
  await runStackActions(stacks, inventory(stacks, 'running'), 'admin', 'stop', 'vigara')
  expect(post).toHaveBeenCalledExactlyOnceWith('host', 'vigara-container', 'stop')
 })
 it('resumes paused containers and leaves running containers alone', async () => {
  const stacks = [stack('app')]
  const hosts = inventory(stacks, 'paused')
  hosts[0].containers.push({ id: 'running', project: 'app', state: 'running' })
  await runStackActions(stacks, hosts, 'admin', 'start')
  expect(post).toHaveBeenCalledExactlyOnceWith('host', 'app-container', 'unpause')
 })
 it('continues after failures and reports their container identity', async () => {
  const stacks = [stack('broken'), stack('good')]
  post.mockImplementation(async (_host, id) => { if (id === 'broken-container') throw new Error('Device refused'); return { id, name: id, action: 'start', changed: true } })
  const results = await runStackActions(stacks, inventory(stacks), 'admin', 'start')
  expect(results[0]).toMatchObject({ ok: false, message: 'broken: Device refused' })
  expect(results[1].ok).toBe(true)
 })
 it('reports offline and read-only hosts instead of silently skipping their stacks', async () => {
  const stacks = [stack('offline')]
  const results = await runStackActions(stacks, [], 'admin', 'start')
  expect(results[0]).toMatchObject({ ok: false, message: 'Device is unavailable.' })
  const readOnly = await runStackActions(stacks, inventory(stacks), 'viewer', 'start')
  expect(readOnly[0].ok).toBe(false)
  expect(post).not.toHaveBeenCalled()
 })
})
