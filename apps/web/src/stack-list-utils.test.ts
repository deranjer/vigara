import { describe, expect, it } from 'vitest'
import { filterStacks, stackContainers, stackStatus, type StackSummary } from './stack-list-utils'
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
  expect(stackStatus(sample({ deploymentStatus: 'failed' }))).toBe('Deployment failed')
  expect(stackStatus(sample({ deploymentStatus: 'succeeded', deploymentCommit: 'b' }))).toBe('Saved changes')
  expect(stackStatus(sample({ deploymentStatus: 'succeeded', deploymentCommit: 'a' }))).toBe('Deployed')
  expect(stackStatus(sample({ deploymentStatus: 'succeeded', deploymentCommit: 'old-commit', digest: 'same-files', deploymentDigest: 'same-files' }))).toBe('Deployed')
 })
})
