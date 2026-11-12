import { describe, expect, it } from 'vitest'
import { activityItems, changedPaths, deploymentTitle, deploymentTone, draftSummary, firstReviewStep, imageTag } from './stack-workspace-utils'

describe('stack workspace', () => {
  it('marks added, removed and changed files and summarises the draft', () => {
    const changes = changedPaths({ 'compose.yaml': 'a', 'old.env.sops': 'x', 'keep.yaml': 'k' }, { 'compose.yaml': 'b', 'new.toml': '', 'keep.yaml': 'k' })
    expect([...changes]).toEqual([['compose.yaml', 'Changed'], ['new.toml', 'Added'], ['old.env.sops', 'Removed']])
    expect(draftSummary(changes)).toBe('3 files changed')
    expect(draftSummary(new Map([['compose.yaml', 'Changed']]))).toBe('compose.yaml changed')
    expect(draftSummary(new Map())).toBe('No changes')
  })

  it('merges revisions and deployments newest first', () => {
    const items = activityItems(
      [{ commit: 'aaa', message: 'Save', at: '2026-11-10T10:00:00Z' }, { commit: 'bbb', message: 'Older', at: '2026-11-01T10:00:00Z' }],
      [{ id: 'j1', commit: 'aaa1234567', digest: 'd', status: 'failed', stage: 'pull', message: 'no such image', createdAt: '2026-11-11T09:00:00Z' }],
    )
    expect(items.map((item) => item.kind === 'revision' ? item.revision.commit : item.deployment.id)).toEqual(['j1', 'aaa', 'bbb'])
  })

  it('words and colours deployments', () => {
    const job = { id: 'j', commit: '835e1f9abc', digest: '', stage: '', message: '', createdAt: '' }
    expect(deploymentTitle({ ...job, status: 'succeeded' })).toBe('Deployed 835e1f9')
    expect(deploymentTitle({ ...job, status: 'failed' })).toBe('Deploy failed 835e1f9')
    expect(['succeeded', 'failed', 'unknown', 'running'].map(deploymentTone)).toEqual(['ok', 'crit', 'warn', 'info'])
  })

  it('shows only the tag of an image in the services rail', () => {
    expect(imageTag('postgres:17-alpine')).toBe('17-alpine')
    expect(imageTag('ghcr.io/deranjer/bookorbit')).toBe('latest')
    expect(imageTag('registry:5000/app:v2@sha256:abc')).toBe('v2')
    expect(imageTag('registry:5000/app')).toBe('latest')
  })

  it('opens the review at the changes only when there is something to commit', () => {
    expect(firstReviewStep(true)).toBe('changes')
    expect(firstReviewStep(false)).toBe('plan')
  })
})
