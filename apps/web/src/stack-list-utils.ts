import { canManage } from './container-utils'

export type StackSummary = {
  id: string; name: string; projectName: string; commit: string; valid: boolean; assigned: boolean
  agentId: string; deviceName?: string; directory?: string; serviceCount: number; draft?: boolean; imported?: boolean
  digest?: string; deploymentDigest?: string; deploymentStatus?: string; deploymentCommit?: string; deploymentAt?: string
}
export type StackHost = { agentId: string; name: string; state: string; truncated?: boolean; dockerTier?: string; containers: { id?: string; name?: string; project?: string; service?: string; state: string }[] }
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
  return stack.imported ? 'Imported' : 'Not deployed'
}
export function filterStacks(stacks: StackSummary[], query: string, device: string, sort: string) {
  const words = query.trim().toLocaleLowerCase().split(/\s+/).filter(Boolean)
  return stacks.filter((stack) => (!device || stack.agentId === device) && words.every((word) => [stack.id, stack.name, stack.projectName, stack.deviceName ?? '', stackStatus(stack)].join(' ').toLocaleLowerCase().includes(word)))
    .sort((a, b) => (sort === 'device' ? (a.deviceName ?? '').localeCompare(b.deviceName ?? '') : sort === 'status' ? stackStatus(a).localeCompare(stackStatus(b)) : 0) || (a.name || a.id).localeCompare(b.name || b.id))
}

export function isDeployedStack(stack: StackSummary, hosts: StackHost[]) {
  return stack.assigned && !!stack.commit && (stack.imported || stack.deploymentStatus === 'succeeded' || stackContainers(stack, hosts).total > 0)
}

export function excludedFromBulk(stack: StackSummary) {
  return [stack.id, stack.projectName].some((name) => ['vigara', 'vigara-caddy'].includes(name.toLowerCase()))
}

export type StackRuntimeStatus = { color: string; label: string; count: string }

export function stackRuntimeBadge(stack: StackSummary, hosts: StackHost[]): StackRuntimeStatus {
  const count = stackContainers(stack, hosts)
  if (!count.known) return { color: 'gray', label: 'Unavailable', count: '—' }
  const containers = hosts.find((host) => host.agentId === stack.agentId)?.containers.filter((container) => container.project === stack.projectName) ?? []
  const observedServices = containers.length && containers.every((container) => container.service) ? new Set(containers.map((container) => container.service)).size : count.total
  const missing = stack.commit ? Math.max(0, stack.serviceCount - observedServices) : 0
  const total = count.total + missing
  const ratio = `${count.running}/${count.partial ? '?' : total}`
  if (!count.total && !isDeployedStack(stack, hosts) && !missing) return { color: 'gray', label: 'Not running', count: ratio }
  const incomplete = count.running < total || !total
  return { color: incomplete ? 'red' : count.partial ? 'yellow' : 'green', label: (count.partial ? 'At least ' : '') + count.running + ' / ' + total + ' running' + (missing ? ' · Missing containers' : ''), count: ratio }
}

export type StackAction = 'start' | 'stop'
export function stackActionBlocker(stack: StackSummary, hosts: StackHost[], role: string) {
  const host = hosts.find((item) => item.agentId === stack.agentId)
  if (!isDeployedStack(stack, hosts)) return 'Deploy this stack first.'
  if (!host || host.state !== 'ok') return 'Device is unavailable.'
  if (!canManage(role, host.dockerTier)) return 'Container management is not available for this device or account.'
  if (host.truncated) return 'The container list is incomplete. Refresh before changing this stack.'
  if (!host.containers.some((container) => container.project === stack.projectName)) return 'No existing containers. Deploy the stack to create them.'
  if (stack.deploymentStatus === 'queued' || stack.deploymentStatus === 'running') return 'Wait for deployment to finish.'
  return null
}

export function stackActionTargets(stack: StackSummary, hosts: StackHost[], action: StackAction) {
  const containers = hosts.find((host) => host.agentId === stack.agentId)?.containers ?? []
  return containers.filter((container) => container.project === stack.projectName && (action === 'start' ? container.state !== 'running' && container.state !== 'restarting' : ['running', 'restarting', 'paused'].includes(container.state)))
}
