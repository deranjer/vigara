import { parse } from 'yaml'

export function isComposeFile(path: string, manifest: string): boolean {
  try {
    const value = parse(manifest)
    if (Array.isArray(value?.compose?.files) && value.compose.files.includes(path)) return true
  } catch { /* Keep filename-based help available while the manifest is being edited. */ }
  return /^(?:.*\/)?(?:docker-)?compose(?:[.-][^/]*)?\.ya?ml$/i.test(path)
}

export const indentation = { tabSize: 2, indentSize: 2, insertSpaces: true, detectIndentation: false } as const
