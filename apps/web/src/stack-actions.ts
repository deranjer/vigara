import { postAction } from './container-actions'
import { runLimited } from './container-utils'
import { excludedFromBulk, isDeployedStack, stackActionBlocker, stackActionTargets, type StackAction, type StackHost, type StackSummary } from './stack-list-utils'

const done: Record<StackAction, string> = { start: 'Started', stop: 'Stopped', restart: 'Restarted' }

/**
 * Starts, stops or restarts the existing containers of stacks. `selected` names the stacks
 * (one ID or the list a confirmation showed); without it, every deployed stack except Vigara's own.
 */
export async function runStackActions(stacks: StackSummary[], hosts: StackHost[], role: string, action: StackAction, selected?: string | string[]) {
  const ids = typeof selected === 'string' ? [selected] : selected
  const targets = ids ? stacks.filter((stack) => ids.includes(stack.id)) : stacks.filter((stack) => isDeployedStack(stack, hosts) && !excludedFromBulk(stack))
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
    return { name, ok: failures.length === 0, message: failures.length ? failures.join('; ') : targets.length ? done[action] + ' existing containers.' : action === 'restart' ? 'Nothing running to restart.' : 'Already ' + (action === 'start' ? 'running.' : 'stopped.') }
  })

  return results
}
