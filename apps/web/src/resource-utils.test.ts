import { describe, expect, it } from 'vitest'
import { confirmWord, filterBy, imageName, isInUse, pruneConfirmWord, pruneDescription, pullRefProblem, removeBlocker, resourcesPath, shortId, sizeLabel, sortRows, summarizePrune, usageLabel } from './resource-utils'

describe('resource-utils', () => {
  it('builds paths and short identifiers', () => {
    expect(resourcesPath('host-1', 'image')).toBe('/api/docker/host-1/images')
    expect(resourcesPath('h/1', 'volume')).toBe('/api/docker/h%2F1/volumes')
    expect(shortId('sha256:0123456789abcdef0123')).toBe('0123456789ab')
    expect(shortId('abc')).toBe('abc')
  })

  it('names images and describes size and usage', () => {
    expect(imageName({ id: 'sha256:0123456789abcdef', tags: ['nginx:1.27', 'nginx:latest'], created: 1, size: 1, dangling: false })).toBe('nginx:1.27')
    expect(imageName({ id: 'sha256:0123456789abcdef', created: 1, size: 1, dangling: true })).toBe('<untagged> 0123456789ab')
    expect(sizeLabel(2048)).toBe('2.0 KiB')
    expect(sizeLabel(0)).toBe('0 B')
    expect(sizeLabel(-1)).toBe('Unknown')
    expect(sizeLabel(undefined)).toBe('Unknown')
    expect(usageLabel(undefined)).toBe('Unused')
    expect(usageLabel([])).toBe('Unused')
    expect(usageLabel([{ id: 'a', name: 'web' }])).toBe('web')
    expect(usageLabel([{ id: 'a', name: 'web' }, { id: 'b', name: 'db' }])).toBe('2 containers')
    expect(isInUse([{ id: 'a', name: 'web' }])).toBe(true)
    expect(isInUse([])).toBe(false)
  })

  it('explains why something cannot be removed', () => {
    expect(removeBlocker('network', { builtin: true })).toContain('Built-in')
    expect(removeBlocker('network', { builtin: false })).toBeNull()
    expect(removeBlocker('volume', { usedBy: [{ id: 'a', name: 'db' }] })).toBe('In use by db. Stop and remove that container first.')
    expect(removeBlocker('image', { usedBy: ['a', 'b', 'c', 'd', 'e'].map((name) => ({ id: name, name })) })).toBe('In use by a, b, c and 2 more. Stop and remove those containers first.')
    expect(removeBlocker('image', { usedBy: [] })).toBeNull()
    expect(removeBlocker('image', {})).toBeNull()
  })

  it('asks for a typed confirmation only where data is at stake', () => {
    expect(confirmWord('volume', 'pgdata')).toBe('pgdata')
    expect(confirmWord('image', 'nginx')).toBeNull()
    expect(confirmWord('network', 'site')).toBeNull()
    expect(pruneConfirmWord('volume', 1)).toBe('delete 1 volume')
    expect(pruneConfirmWord('volume', 3)).toBe('delete 3 volumes')
    expect(pruneConfirmWord('image', 3)).toBeNull()
    expect(pruneConfirmWord('network', 3)).toBeNull()
  })

  it('summarises a prune preview', () => {
    expect(summarizePrune([])).toBe('Nothing to remove.')
    expect(summarizePrune([{ id: 'a' }])).toBe('1 item')
    expect(summarizePrune([{ id: 'a', size: 1024 }, { id: 'b', size: 1024 }])).toBe('2 items, about 2.0 KiB')
    expect(pruneDescription('image', false)).toContain('dangling')
    expect(pruneDescription('image', true)).toContain('tagged')
    expect(pruneDescription('volume', false)).toContain('anonymous')
    expect(pruneDescription('volume', true)).toContain('named volumes')
    expect(pruneDescription('network', true)).toContain('never removed')
  })

  it('validates an image reference to pull like the server does', () => {
    for (const ok of ['nginx', 'nginx:1.27-alpine', 'ghcr.io/deranjer/vigara:staging', 'repo/app@sha256:abc123', 'localhost:5000/team/app:v1', '  redis:7  ']) expect(pullRefProblem(ok)).toBeNull()
    expect(pullRefProblem('')).toContain('Enter')
    expect(pullRefProblem('   ')).toContain('Enter')
    expect(pullRefProblem('sha256:0123456789ab')).toContain('ID')
    for (const bad of ['a b', '-rf', 'repo//app', 'repo/./app', 'repo/', '../etc', 'app;id', 'x'.repeat(256)]) expect(pullRefProblem(bad)).toContain('not a valid')
  })

  it('filters and sorts rows', () => {
    const rows = [{ n: 'beta', s: 2 }, { n: 'Alpha', s: 3 }, { n: 'gamma', s: 1 }]
    expect(filterBy(rows, '', (row) => [row.n])).toHaveLength(3)
    expect(filterBy(rows, ' ALP ', (row) => [row.n]).map((row) => row.n)).toEqual(['Alpha'])
    expect(filterBy(rows, 'zzz', (row) => [row.n])).toEqual([])
    expect(sortRows(rows, (row) => row.n).map((row) => row.n)).toEqual(['Alpha', 'beta', 'gamma'])
    expect(sortRows(rows, (row) => row.s, true).map((row) => row.s)).toEqual([3, 2, 1])
    expect(rows[0].n).toBe('beta')
  })
})
