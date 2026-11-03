import { describe, expect, it } from 'vitest'
import { excludedFromBulk, filterStacks, stackActionBlocker, stackActionTargets, stackContainers, stackRuntimeBadge, stackStatus, type StackSummary } from './stack-list-utils'
const sample = (changes: Partial<StackSummary> = {}): StackSummary => ({ id: 'media', name: 'Home media', projectName: 'media', agentId: 'host-1', deviceName: 'Living room', commit: 'a', valid: true, assigned: true, serviceCount: 2, ...changes })
describe('stack list', () => {
 it('searches across name, project, device and deployment status, with a device filter', () => {
  const stacks = [sample(), sample({ id: 'backup', name: 'Backup', projectName: 'backup', agentId: 'host-2', deviceName: 'Office', deploymentStatus: 'failed' })]
  expect(filterStacks(stacks, 'office failed', '', 'name').map((s) => s.id)).toEqual(['backup'])
  expect(filterStacks(stacks, 'media', 'host-2', 'name')).toEqual([])
  expect(filterStacks(stacks, 'living', 'host-1', 'name')).toHaveLength(1)
 })
 it('counts only containers on the assigned device and matching project', () => {
  const result = stackContainers(sample(), [{ agentId: 'host-1', name: 'A', state: 'ok', containers: [{ project: 'media', state: 'running' }, { project: 'media', state: 'exited' }, { project: 'other', state: 'running' }] }, { agentId: 'host-2', name: 'B', state: 'ok', containers: [{ project: 'media', state: 'running' }] }])
  expect(result).toEqual({ known: true, total: 2, running: 1, partial: false })
 })
 it('does not present offline or incomplete counts as exact zeroes', () => {
  expect(stackContainers(sample(), []).known).toBe(false)
  expect(stackContainers(sample(), [{ agentId: 'host-1', name: 'A', state: 'offline', containers: [] }]).known).toBe(false)
  expect(stackContainers(sample(), [{ agentId: 'host-1', name: 'A', state: 'ok', truncated: true, containers: [] }]).partial).toBe(true)
 })
 it('distinguishes drafts, failed attempts and revisions waiting to deploy', () => {
  expect(stackStatus(sample({ commit: '' }))).toBe('Draft')
  expect(stackStatus(sample({ imported: true }))).toBe('Imported')
  expect(stackStatus(sample({ imported: true, deploymentStatus: 'failed' }))).toBe('Deployment failed')
  expect(stackStatus(sample({ deploymentStatus: 'failed' }))).toBe('Deployment failed')
  expect(stackStatus(sample({ deploymentStatus: 'succeeded', deploymentCommit: 'b' }))).toBe('Saved changes')
  expect(stackStatus(sample({ deploymentStatus: 'succeeded', deploymentCommit: 'a' }))).toBe('Deployed')
  expect(stackStatus(sample({ deploymentStatus: 'succeeded', deploymentCommit: 'old-commit', digest: 'same-files', deploymentDigest: 'same-files' }))).toBe('Deployed')
 })
})

const host = (states: string[], extra = {}) => [{ agentId: 'host-1', name: 'A', state: 'ok', dockerTier: 'manage', containers: states.map((state, index) => ({ id: String(index), project: 'media', state, service: 'svc-' + index })), ...extra }]
describe('stack lifecycle status and targets', () => {
 it('shows red for partially and entirely stopped stacks, and green only for all running', () => {
  expect(stackRuntimeBadge(sample(), host(['running', 'exited'])).color).toBe('red')
  expect(stackRuntimeBadge(sample(), host(['exited', 'paused'])).color).toBe('red')
  expect(stackRuntimeBadge(sample(), host(['running', 'running'])).color).toBe('green')
  expect(stackRuntimeBadge(sample(), host(['running', 'restarting'])).color).toBe('red')
 })
 it('flags missing deployed services even when another service has multiple replicas', () => {
  const hosts = host(['running', 'running'])
  hosts[0].containers[1].service = hosts[0].containers[0].service
  const badge = stackRuntimeBadge(sample({ deploymentStatus: 'succeeded', deploymentCommit: 'a' }), hosts)
  expect(badge.color).toBe('red')
  expect(badge.label).toContain('Missing containers')
  expect(stackRuntimeBadge(sample({ deploymentStatus: 'succeeded', deploymentCommit: 'a' }), host([])).color).toBe('red')
 })
 it('keeps offline, truncated, and never deployed observations distinct', () => {
  expect(stackRuntimeBadge(sample(), []).color).toBe('gray')
  expect(stackRuntimeBadge(sample(), host(['running', 'running'], { truncated: true })).color).toBe('yellow')
  expect(stackRuntimeBadge(sample({ commit: '' }), host([])).color).toBe('gray')
 })
 it('protects Vigara and Caddy by stack ID and Compose project for bulk actions', () => {
  for (const name of ['vigara', 'vigara-caddy', 'VIGARA']) {
   expect(excludedFromBulk(sample({ id: name }))).toBe(true)
   expect(excludedFromBulk(sample({ projectName: name }))).toBe(true)
  }
  expect(excludedFromBulk(sample({ id: 'vigara-tools', projectName: 'caddy' }))).toBe(false)
 })
 it('blocks unavailable, read-only, truncated, undeployed and actively deploying stacks', () => {
  expect(stackActionBlocker(sample(), host(['exited']), 'admin')).toBeNull()
  expect(stackActionBlocker(sample(), host(['exited']), 'viewer')).toBeTruthy()
  expect(stackActionBlocker(sample(), host(['exited'], { dockerTier: 'read' }), 'admin')).toBeTruthy()
  expect(stackActionBlocker(sample({ imported: true }), [], 'admin')).toBeTruthy()
  expect(stackActionBlocker(sample(), host(['exited'], { truncated: true }), 'admin')).toBeTruthy()
  expect(stackActionBlocker(sample({ commit: '' }), host([]), 'admin')).toBeTruthy()
  expect(stackActionBlocker(sample({ deploymentStatus: 'running' }), host(['exited']), 'admin')).toBeTruthy()
 })
 it('targets only matching project containers whose state needs the action', () => {
  const hosts = host(['running', 'exited', 'paused', 'restarting'])
  hosts[0].containers.push({ id: 'other', project: 'other', state: 'exited', service: 'other' })
  expect(stackActionTargets(sample(), hosts, 'start').map(c => c.id)).toEqual(['1', '2'])
  expect(stackActionTargets(sample(), hosts, 'stop').map(c => c.id)).toEqual(['0', '2', '3'])
 })
})
