import { describe, expect, it } from 'vitest'
import { defaultImageUpdateFilters, selectImageUpdates, type ImageUpdateRow } from './image-update-list'

const row = (id: string, patch: Partial<ImageUpdateRow> = {}): ImageUpdateRow => ({ agentId: 'host-a', containerId: id, device: 'Server A', stackId: 'media', ignored: false, checkedAt: '2026-11-04T12:00:00Z', result: { name: id, image: 'app:stable', status: 'available' }, ...patch })

describe('image update table', () => {
  it('combines device, status, scope, and case-insensitive image search', () => {
    const match = row('web2')
    const items = [match, row('web3', { agentId: 'host-b' }), row('web4', { stackId: '' }), row('web5', { ignored: true })]
    expect(selectImageUpdates(items, { ...defaultImageUpdateFilters, search: ' APP:STABLE ', device: 'host-a', status: 'available', scope: 'managed' })).toEqual([match])
  })
  it('searches running references and filters ignored containers by their visible status', () => {
    const ignored = row('web', { ignored: true, result: { name: 'web', image: 'app:stable', runningImage: 'app@sha256:abc', status: 'available' } })
    expect(selectImageUpdates([ignored], { ...defaultImageUpdateFilters, search: 'SHA256:ABC', status: 'ignored' })).toEqual([ignored])
    expect(selectImageUpdates([ignored], { ...defaultImageUpdateFilters, status: 'available' })).toEqual([])
  })
  it('sorts container names naturally without mutating polling results', () => {
    const items = [row('web10'), row('web2')]
    expect(selectImageUpdates(items, defaultImageUpdateFilters).map(v => v.containerId)).toEqual(['web2', 'web10'])
    expect(items.map(v => v.containerId)).toEqual(['web10', 'web2'])
  })
  it('supports newest checks first and actionable status order', () => {
    const older = row('old', { checkedAt: '2026-11-03T12:00:00Z', result: { name: 'old', image: 'app:stable', status: 'current' } })
    const newer = row('new')
    expect(selectImageUpdates([older, newer], { ...defaultImageUpdateFilters, sort: 'checked', descending: true })).toEqual([newer, older])
    expect(selectImageUpdates([older, newer], { ...defaultImageUpdateFilters, sort: 'status' })).toEqual([newer, older])
    expect(selectImageUpdates([older, newer], { ...defaultImageUpdateFilters, scope: 'external' })).toEqual([])
  })
})
