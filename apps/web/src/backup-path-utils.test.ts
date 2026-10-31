import { describe, expect, it } from 'vitest'
import { addBackupPath, cleanBackupPath, isAbsoluteBackupPath, selectedFolderName } from './backup-path-utils'

describe('backup folder paths', () => {
  it('adds Windows, UNC, and Linux paths individually, including copied quoted paths', () => {
    let paths: string[] = []
    for (const input of ['  "C:\\Users\\Alex\\My Documents"  ', '\\\\nas\\backups', '/home/alex/Documents']) {
      const result = addBackupPath(paths, input)
      expect(result.error).toBeUndefined()
      paths = result.paths
    }
    expect(paths).toEqual(['C:\\Users\\Alex\\My Documents', '\\\\nas\\backups', '/home/alex/Documents'])
    expect(cleanBackupPath("'/home/alex/My Documents'")).toBe('/home/alex/My Documents')
    expect(isAbsoluteBackupPath('C:\\')).toBe(true)
    expect(isAbsoluteBackupPath('/')).toBe(true)
  })

  it('rejects relative names from a browser picker rather than inventing an absolute path', () => {
    for (const value of ['Documents', 'C:Documents', '\\Documents', '\\\\server', '../Documents', '/home/alex\n/tmp', '/bad\0path']) {
      const result = addBackupPath([], value)
      expect(result.error).toBeTruthy()
      expect(result.paths).toEqual([])
    }
    expect(selectedFolderName('Documents/work/notes.txt')).toBe('Documents')
    expect(selectedFolderName('')).toBeUndefined()
    expect(selectedFolderName('notes.txt')).toBeUndefined()
  })

  it('prevents duplicate paths without confusing Linux case-sensitive folders', () => {
    expect(addBackupPath(['C:\\Users\\Alex\\Documents'], 'c:/users/alex/Documents/').error).toBeTruthy()
    expect(addBackupPath(['\\\\NAS\\share'], '\\\\nas\\SHARE\\').error).toBeTruthy()
    expect(addBackupPath(['/home/alex/Documents'], '/home/alex/Documents/').error).toBeTruthy()
    expect(addBackupPath(['/home/alex/Documents'], '/home/alex/documents').error).toBeUndefined()
  })

  it('preserves existing entries when input is invalid or the limit is reached', () => {
    const paths = ['/data']
    expect(addBackupPath(paths, '/data').paths).toBe(paths)
    expect(paths).toEqual(['/data'])
    const full = Array.from({ length: 128 }, (_, i) => `/data/${i}`)
    expect(addBackupPath(full, '/extra').error).toContain('128')
    expect(addBackupPath([], '/' + 'x'.repeat(2048)).error).toContain('2048')
  })
})
