import { describe, expect, it } from 'vitest'
import { canBrowseFiles, childPath, fileLanguage, parentPath, textBytes } from './mount-file-utils'

describe('mount file access', () => {
  it('restricts credential-bearing files to administrators on an exec-enabled host', () => {
    for (const role of ['viewer', 'operator']) for (const tier of ['read', 'manage', 'exec', 'deploy']) expect(canBrowseFiles(role, tier)).toBe(false)
    expect(canBrowseFiles('admin', 'manage')).toBe(false)
    expect(canBrowseFiles('admin', undefined)).toBe(false)
    expect(canBrowseFiles('admin', 'exec')).toBe(true)
    expect(canBrowseFiles('admin', 'deploy')).toBe(true)
  })

  it('navigates mount-relative paths and counts encoded bytes for the size limit', () => {
    expect(childPath('', 'Caddyfile')).toBe('Caddyfile')
    expect(childPath('config', 'site.conf')).toBe('config/site.conf')
    expect(parentPath('config/sites')).toBe('config')
    expect(parentPath('config')).toBe('')
    expect(textBytes('é')).toBe(2)
    expect(textBytes('🦀')).toBe(4)
  })

  it('uses appropriate text languages without applying Compose schemas', () => {
    expect(fileLanguage('settings.yaml')).toBe('yaml')
    expect(fileLanguage('.env')).toBe('ini')
    expect(fileLanguage('config.json')).toBe('json')
    expect(fileLanguage('Caddyfile')).toBe('caddyfile')
  })
})
