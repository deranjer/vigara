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

export type StackAction = 'start' | 'stop' | 'restart'
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
  return containers.filter((container) => container.project === stack.projectName && (
    action === 'start' ? container.state !== 'running' && container.state !== 'restarting'
      : action === 'restart' ? container.state === 'running'
        : ['running', 'restarting', 'paused'].includes(container.state)))
}

export type LabelKey = 'draft' | 'update' | 'drift' | 'failed' | 'check' | 'review' | 'saved' | 'deploying' | 'unassigned'
export type StackLabel = { key: LabelKey; text: string; tone: 'warn' | 'crit' | 'info' | 'idle'; detail?: string }

/**
 * Services running for a deployed stack that do not match its saved Compose file: some are
 * missing, or more are running than it defines. Null when the stack is not deployed as saved,
 * or when the device's list is unavailable or incomplete.
 */
export function stackDrift(stack: StackSummary, hosts: StackHost[]): string | null {
  if (stackStatus(stack) !== 'Deployed') return null
  const host = hosts.find((item) => item.agentId === stack.agentId)
  if (!host || host.state !== 'ok' || host.truncated) return null
  const containers = host.containers.filter((container) => container.project === stack.projectName)
  if (!containers.every((container) => container.service)) return null
  const observed = new Set(containers.map((container) => container.service)).size
  if (observed < stack.serviceCount) return `${stack.serviceCount - observed} of ${stack.serviceCount} saved services have no container`
  if (observed > stack.serviceCount) return `${observed - stack.serviceCount} more service${observed - stack.serviceCount === 1 ? '' : 's'} running than the saved Compose file defines`
  return null
}

/** Stack IDs with an image update waiting, from GET /api/image-updates. */
export function stacksWithUpdates(items: { stackId?: string; ignored?: boolean; result?: { status?: string } }[]) {
  return new Set(items.filter((item) => item.stackId && !item.ignored && item.result?.status === 'available').map((item) => item.stackId as string))
}

/** What differs about a stack, most urgent first. A stack with no labels is simply deployed. */
export function stackLabels(stack: StackSummary, hosts: StackHost[], updates: Set<string>): StackLabel[] {
  const labels: StackLabel[] = []
  const status = stackStatus(stack)
  if (status === 'Deployment failed') labels.push({ key: 'failed', text: 'Last deploy failed', tone: 'crit' })
  if (status === 'Needs review') labels.push({ key: 'review', text: 'Needs review', tone: 'crit', detail: 'The saved stack files are not valid.' })
  if (status === 'Check deployment') labels.push({ key: 'check', text: 'Check deployment', tone: 'warn', detail: 'The last deployment result could not be confirmed.' })
  const drift = stackDrift(stack, hosts)
  if (drift) labels.push({ key: 'drift', text: 'Drift', tone: 'warn', detail: drift })
  if (stack.draft || !stack.commit) labels.push({ key: 'draft', text: 'Draft', tone: 'warn', detail: stack.commit ? 'Unsaved edits' : 'Never saved' })
  if (updates.has(stack.id)) labels.push({ key: 'update', text: 'Update available', tone: 'info' })
  if (status === 'Saved changes') labels.push({ key: 'saved', text: 'Not deployed yet', tone: 'info', detail: 'The saved revision differs from the last deployment.' })
  if (status === 'Deploying') labels.push({ key: 'deploying', text: 'Deploying', tone: 'info' })
  if (status === 'No target') labels.push({ key: 'unassigned', text: 'No device', tone: 'idle' })
  return labels
}

export type StackFilter = 'all' | 'attention' | 'draft' | 'update' | 'drift' | 'failed'
const attention: LabelKey[] = ['failed', 'review', 'check', 'drift', 'draft', 'update']
export function matchesStackFilter(labels: StackLabel[], filter: StackFilter) {
  if (filter === 'all') return true
  if (filter === 'attention') return labels.some((label) => attention.includes(label.key))
  if (filter === 'failed') return labels.some((label) => label.key === 'failed' || label.key === 'review' || label.key === 'check')
  return labels.some((label) => label.key === filter)
}
export function stackFilterCounts(labels: StackLabel[][]): Record<StackFilter, number> {
  const filters: StackFilter[] = ['all', 'attention', 'draft', 'update', 'drift', 'failed']
  return Object.fromEntries(filters.map((filter) => [filter, labels.filter((item) => matchesStackFilter(item, filter)).length])) as Record<StackFilter, number>
}

/** A stack that fronts other sites, so stopping it takes them offline too. */
export function isReverseProxy(stack: StackSummary) {
  return /(^|[-_])(caddy|traefik|nginx-proxy|npm|haproxy|proxy)([-_]|$)/i.test(stack.id) || /(^|[-_])(caddy|traefik|nginx-proxy|haproxy)([-_]|$)/i.test(stack.projectName)
}

/** Deployed stacks that Start all / Stop all would change. Vigara's own stacks stay out unless asked. */
export function bulkStacks(stacks: StackSummary[], hosts: StackHost[], includeVigara: boolean) {
  return stacks.filter((stack) => isDeployedStack(stack, hosts) && (includeVigara || !excludedFromBulk(stack)))
}
