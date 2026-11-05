import { isMap, parse, parseDocument } from 'yaml'

export function imageWithTag(image: string, tag: string): string {
  if (!/^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$/.test(tag)) throw new Error('Enter a valid image tag.')
  let repository = image.split('@')[0] ?? ''
  if (repository.lastIndexOf(':') > repository.lastIndexOf('/')) repository = repository.slice(0, repository.lastIndexOf(':'))
  if (!repository || repository.includes('$') || repository.startsWith('sha256:')) throw new Error('The image repository could not be determined.')
  return `${repository}:${tag}`
}

// Write to the last Compose file declaring this service so earlier overrides
// cannot hide the selected tag. Document edits preserve comments and other keys.
export function setServiceImage(files: Record<string, string>, service: string, image: string): Record<string, string> {
  const manifest = parse(files['vigara-stack.yaml'] ?? '')
  const paths: unknown = manifest?.compose?.files
  if (!Array.isArray(paths)) throw new Error('The stack has no Compose files.')
  for (const path of [...paths].reverse()) {
    if (typeof path !== 'string' || files[path] === undefined) continue
    const doc = parseDocument(files[path])
    if (doc.errors.length) throw new Error('Fix Compose file errors before changing image tags.')
    if (!doc.hasIn(['services', service])) continue
    const node = doc.getIn(['services', service])
    if (!isMap(node) || node.anchor || (node.get('image', true) as { anchor?: string } | undefined)?.anchor) throw new Error('Edit this service’s YAML anchor or alias in Compose files before changing its image.')
    doc.setIn(['services', service, 'image'], image)
    return { ...files, [path]: doc.toString() }
  }
  throw new Error('The service is no longer in the Compose files. Refresh the stack.')
}
