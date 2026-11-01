export const maxBackupPaths = 128

export function cleanBackupPath(value: string): string {
  const trimmed = value.trim()
  return trimmed.length >= 2 && ((trimmed.startsWith('"') && trimmed.endsWith('"')) || (trimmed.startsWith("'") && trimmed.endsWith("'"))) ? trimmed.slice(1, -1) : trimmed
}

export function isAbsoluteBackupPath(value: string): boolean {
  if (!value || /[\r\n\0]/.test(value)) return false
  return value.startsWith('/') || /^[a-z]:[\\/]/i.test(value) || /^\\\\[^\\/]+[\\/][^\\/]+/.test(value)
}

function pathKey(value: string): string {
  const path = cleanBackupPath(value)
  if (/^[a-z]:[\\/]/i.test(path) || path.startsWith('\\\\')) return path.replaceAll('\\', '/').replace(/\/+$/, '').toLowerCase()
  return path.replace(/\/+$/, '') || '/'
}

export function addBackupPath(paths: string[], draft: string): { paths: string[]; error?: string } {
  const value = cleanBackupPath(draft)
  if (!isAbsoluteBackupPath(value)) return { paths, error: 'Enter the full path on the backup device, such as C:\\Users\\Alex\\Documents or /home/alex/Documents.' }
  if (value.length > 2048) return { paths, error: 'Paths must be no more than 2048 characters.' }
  if (paths.some((path) => pathKey(path) === pathKey(value))) return { paths, error: 'This folder is already in the list.' }
  if (paths.length >= maxBackupPaths) return { paths, error: `You can add up to ${maxBackupPaths} folders.` }
  return { paths: [...paths, value] }
}

// Browsers provide paths relative to the selected folder, never its disk path.
export function selectedFolderName(relativePath: string): string | undefined {
  const slash = relativePath.indexOf('/')
  return slash > 0 ? relativePath.slice(0, slash) : undefined
}
