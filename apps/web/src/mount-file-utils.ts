export type StorageTarget = { kind: 'mount' | 'volume'; id: string; mount?: string }
export type FileEntry = { name: string; kind: 'directory' | 'file' | 'symlink' | 'special'; size: number }
export type FileListing = { entries: FileEntry[]; singleFile: boolean; writable: boolean; truncated?: boolean }
export type TextFile = { content: string; version: string; writable: boolean; backedUp?: boolean }

export const canBrowseFiles = (role: string, tier?: string) => role === 'admin' && (tier === 'exec' || tier === 'deploy')
export const childPath = (folder: string, name: string) => folder ? `${folder}/${name}` : name
export const parentPath = (folder: string) => folder.split('/').slice(0, -1).join('/')
export const textBytes = (value: string) => new TextEncoder().encode(value).length

export function fileLanguage(name: string) {
  if (/(?:^|\/)Caddyfile(?:$|\.)|\.caddyfile$/i.test(name)) return 'caddyfile'
  if (/\.ya?ml$/i.test(name)) return 'yaml'
  if (/\.json$/i.test(name)) return 'json'
  if (/\.(sh|bash)$/i.test(name)) return 'shell'
  if (/\.(ini|conf|env)$/i.test(name) || name === '.env') return 'ini'
  return 'plaintext'
}

export async function fileRequest<T>(host: string, operation: 'list' | 'read' | 'write', target: StorageTarget, path: string, extra?: { content: string; version: string }, signal?: AbortSignal): Promise<T> {
  const response = await fetch(`/api/docker/${encodeURIComponent(host)}/files/${operation}`, {
    method: 'POST', credentials: 'same-origin', cache: 'no-store', signal,
    headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ ...target, path, ...extra }),
  })
  if (!response.ok) throw new Error((await response.text()).trim().slice(0, 500) || 'The file request failed.')
  return response.json() as Promise<T>
}
