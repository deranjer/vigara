export type StackSummary = {
  id: string; name: string; projectName: string; commit: string; valid: boolean; assigned: boolean
  agentId: string; deviceName?: string; directory?: string; serviceCount: number; draft?: boolean
  digest?: string; deploymentDigest?: string; deploymentStatus?: string; deploymentCommit?: string; deploymentAt?: string
}
export type StackHost = { agentId: string; name: string; state: string; truncated?: boolean; containers: { project?: string; state: string }[] }
export function stackContainers(stack: StackSummary, hosts: StackHost[]) {
  const host = hosts.find((item) => item.agentId === stack.agentId)
  if (!host || host.state !== 'ok') return { known: false, total: 0, running: 0, partial: false }
  const containers = host.containers.filter((container) => container.project === stack.projectName)
  return { known: true, total: containers.length, running: containers.filter((container) => container.state === 'running').length, partial: !!host.truncated }
}
export function stackStatus(stack: StackSummary) {
  if (!stack.commit) return 'Draft'
  if (!stack.valid) return 'Needs review'
  if (!stack.assigned) return 'No target'
  if (stack.deploymentStatus === 'queued' || stack.deploymentStatus === 'running') return 'Deploying'
  if (stack.deploymentStatus === 'failed') return 'Deployment failed'
  if (stack.deploymentStatus === 'unknown') return 'Check deployment'
  if (stack.deploymentStatus === 'succeeded') return (stack.digest && stack.deploymentDigest ? stack.digest === stack.deploymentDigest : stack.deploymentCommit === stack.commit) ? 'Deployed' : 'Saved changes'
  return 'Not deployed'
}
export function filterStacks(stacks: StackSummary[], query: string, device: string, sort: string) {
  const words = query.trim().toLocaleLowerCase().split(/\s+/).filter(Boolean)
  return stacks.filter((stack) => (!device || stack.agentId === device) && words.every((word) => [stack.id, stack.name, stack.projectName, stack.deviceName ?? '', stackStatus(stack)].join(' ').toLocaleLowerCase().includes(word)))
    .sort((a, b) => (sort === 'device' ? (a.deviceName ?? '').localeCompare(b.deviceName ?? '') : sort === 'status' ? stackStatus(a).localeCompare(stackStatus(b)) : 0) || (a.name || a.id).localeCompare(b.name || b.id))
}
