import { describe, expect, it } from 'vitest'
import { stackFileChanges } from './stack-diff-utils'

describe('stack draft comparison', () => {
  it('includes every file on either side, including removed and empty files', () => {
    const entries = stackFileChanges(
      { 'compose.yaml': 'old', 'config/app.txt': 'removed', 'vigara-stack.yaml': 'same', 'empty.txt': '' },
      { 'compose.yaml': 'new', 'new.txt': '', 'vigara-stack.yaml': 'same' },
    )
    expect(entries.map(({ path, status }) => [path, status])).toEqual([
      ['compose.yaml', 'Changed'], ['config/app.txt', 'Removed'], ['empty.txt', 'Removed'], ['new.txt', 'Added'], ['vigara-stack.yaml', 'Unchanged'],
    ])
    expect(entries[0]).toMatchObject({ original: 'old', value: 'new' })
    expect(entries[1]).toMatchObject({ original: 'removed', value: '' })
  })
  it('preserves whitespace-only changes and missing trailing newlines', () => {
    expect(stackFileChanges({ 'config': '  value\n' }, { 'config': 'value' })[0]).toMatchObject({ status: 'Changed', original: '  value\n', value: 'value' })
  })
  it('reports encrypted file changes without exposing their contents', () => {
    expect(stackFileChanges({ 'env.sops': 'old-secret' }, { 'env.sops': 'new-secret' })[0]).toEqual({ path: 'env.sops', status: 'Changed', encrypted: true, original: '', value: '' })
  })
})
