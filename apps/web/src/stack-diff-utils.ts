export type StackFiles = Record<string, string>
export type FileChange = { path: string; status: 'Added' | 'Removed' | 'Changed' | 'Unchanged'; encrypted: boolean; original: string; value: string }

export function stackFileChanges(saved: StackFiles, draft: StackFiles): FileChange[] {
  return [...new Set([...Object.keys(saved), ...Object.keys(draft)])].sort().map((path) => {
    const before = Object.hasOwn(saved, path)
    const after = Object.hasOwn(draft, path)
    const encrypted = path.endsWith('.sops')
    return {
      path, encrypted,
      status: !before ? 'Added' : !after ? 'Removed' : saved[path] === draft[path] ? 'Unchanged' : 'Changed',
      // Encrypted environment content must never reach a comparison model.
      original: encrypted ? '' : saved[path] ?? '',
      value: encrypted ? '' : draft[path] ?? '',
    }
  })
}
