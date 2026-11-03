import { postAction } from './container-actions'
import { runLimited } from './container-utils'
import { excludedFromBulk, isDeployedStack, stackActionBlocker, stackActionTargets, type StackAction, type StackHost, type StackSummary } from './stack-list-utils'

export async function runStackActions(stacks: StackSummary[], hosts: StackHost[], role: string, action: StackAction, selectedId?: string) {
  const targets = selectedId ? stacks.filter((stack) => stack.id === selectedId) : stacks.filter((stack) => isDeployedStack(stack, hosts) && !excludedFromBulk(stack))
  if (!targets.length) throw new Error('No deployed stacks are available for this action. Refresh the list.')
  const results = await runLimited(targets, 3, async (stack) => {
    const name = (stack.name || stack.id) + ' · ' + (stack.deviceName || stack.agentId)
    const blocker = stackActionBlocker(stack, hosts, role)
    if (blocker) return { name, ok: false, message: blocker }
    const failures: string[] = []
    const targets = stackActionTargets(stack, hosts, action)
    for (const container of targets) {
      try {
        if (!container.id) throw new Error('Container identity is unavailable. Refresh and retry.')
        await postAction(stack.agentId, container.id, action === 'start' && container.state === 'paused' ? 'unpause' : action)
      } catch (cause) { failures.push((container.name || container.id || 'Container') + ': ' + (cause instanceof Error ? cause.message : 'Action failed.')) }
    }
    return { name, ok: failures.length === 0, message: failures.length ? failures.join('; ') : targets.length ? (action === 'start' ? 'Started' : 'Stopped') + ' existing containers.' : 'Already ' + (action === 'start' ? 'running.' : 'stopped.') }
  })

  return results
}
