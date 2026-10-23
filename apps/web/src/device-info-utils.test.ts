import { describe, expect, it } from 'vitest'
import { driveUsage, operatingSystem } from './device-info-utils'

describe('drive usage', () => {
  it('pairs capacity by all labels regardless of label order and keeps drives separate', () => {
    expect(driveUsage([
      { name: 'filesystem_total_bytes', value: 100, labels: { mount_path: 'C:', filesystem: 'NTFS' } },
      { name: 'filesystem_total_bytes', value: 200, labels: { mount_path: 'D:', filesystem: 'NTFS' } },
      { name: 'filesystem_free_bytes', value: 25, labels: { filesystem: 'NTFS', mount_path: 'C:' } },
      { name: 'filesystem_free_bytes', value: 100, labels: { mount_path: 'D:', filesystem: 'NTFS' } },
    ]).map(d => [d.mount, d.used, d.total])).toEqual([['C:', 75, 100], ['D:', 100, 200]])
  })
  it('keeps missing or invalid free space unknown, preserving a genuinely empty drive', () => {
    for (const value of [-1, NaN, 101]) {
      expect(driveUsage([{ name: 'filesystem_total_bytes', value: 100 }, { name: 'filesystem_free_bytes', value }])[0]?.used).toBeNull()
    }
    expect(driveUsage([{ name: 'filesystem_total_bytes', value: 100 }])[0]?.used).toBeNull()
    expect(driveUsage([{ name: 'filesystem_total_bytes', value: 100 }, { name: 'filesystem_free_bytes', value: 100 }])[0]?.used).toBe(0)
    expect(driveUsage([{ name: 'filesystem_total_bytes', value: 0 }])).toEqual([])
  })
})

describe('OS logos', () => {
  it('recognizes platform names and falls back for unknown systems', () => {
    expect(['Windows 11', 'linux', 'Ubuntu', 'darwin', 'freebsd', ''].map(operatingSystem)).toEqual(['windows', 'linux', 'linux', 'macos', 'unknown', 'unknown'])
  })
})
