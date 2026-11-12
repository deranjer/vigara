import { describe, expect, it } from 'vitest'
import { newestServerRelease } from './release-check'

describe('newestServerRelease', () => {
  it('ignores app releases, drafts and prereleases', () => {
    expect(newestServerRelease([{ tag_name: 'android-v1.0.0' }, { tag_name: 'v0.4.0' }, { tag_name: 'v0.10.0', draft: true }, { tag_name: 'v0.5.0-rc.1', prerelease: true }, { tag_name: 'v0.4.2' }])).toBe('v0.4.2')
    expect(newestServerRelease([{ tag_name: 'android-v1.0.0' }])).toBeNull()
  })
})
