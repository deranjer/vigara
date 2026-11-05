import { useEffect, useRef, useState } from 'react'
import { Alert, Badge, Button, Card, Group, SimpleGrid, Stack, Text, Title } from '@mantine/core'
import { fetchInspect, type InspectResult } from './container-detail'
import { portSummary, runLimited, stateColors, type ContainerInfo, type HostEntry } from './container-utils'
import { runStackActions } from './stack-actions'
import { stackActionBlocker, stackActionTargets, stackRuntimeBadge, type StackAction, type StackSummary } from './stack-list-utils'
import { route as routeOf, type Route } from './route'

type ContainerRuntime = { container: ContainerInfo; inspect?: InspectResult; error?: string }
export type RuntimeData = { stack: StackSummary; hosts: HostEntry[]; containers: ContainerRuntime[] }
type Outcome = { name: string; ok: boolean; message: string }

async function get<T>(path: string): Promise<T> {
  const response = await fetch(path, { credentials: 'same-origin' })
  if (!response.ok) throw new Error((await response.text()).trim().slice(0, 400) || 'Could not load stack containers.')
  return response.json() as Promise<T>
}

export async function loadStackRuntime(id: string, knownStack?: StackSummary): Promise<RuntimeData> {
  const listingRequest = knownStack?.id === id ? Promise.resolve({ stacks: [knownStack] }) : get<{ stacks: StackSummary[] }>('/api/stacks')
  const [listing, inventory] = await Promise.all([listingRequest, get<{ hosts: HostEntry[] }>('/api/containers')])
  const stack = listing.stacks.find((stack) => stack.id === id)
  if (!stack) throw new Error('The stack is no longer available. Refresh the stack list.')
  const host = inventory.hosts.find((host) => host.agentId === stack.agentId)
  const containers = host?.state === 'ok' ? host.containers.filter((container) => container.project === stack.projectName) : []
  const details = await runLimited(containers, 4, async (container): Promise<ContainerRuntime> => {
    try { return { container, inspect: await fetchInspect(stack.agentId, container.id, false) } }
    catch (cause) { return { container, error: cause instanceof Error ? cause.message : 'Could not inspect this container.' } }
  })
  return { stack, hosts: inventory.hosts, containers: details }
}

export function StackRuntime({ id, role, deploymentStatus, onNavigate }: { id: string; role: string; deploymentStatus?: string; onNavigate: (route: Route) => void }) {
  const [data, setData] = useState<RuntimeData | null>(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const [refresh, setRefresh] = useState(0)
  const [busy, setBusy] = useState<StackAction | null>(null)
  const [outcomes, setOutcomes] = useState<Outcome[]>([])
  const lock = useRef(false)
  useEffect(() => {
    let active = true
    let polling = false
    let knownStack: StackSummary | undefined
    async function poll() {
      if (polling || lock.current) return
      polling = true
      try {
        // Container polling must not fetch/synchronize the Git repository every ten seconds.
        const value = await loadStackRuntime(id, knownStack)
        knownStack = value.stack
        if (active && !lock.current) { setData(value); setError('') }
      } catch (cause) {
        if (active && !lock.current) { setData(null); setError(cause instanceof Error ? cause.message : 'Could not load stack containers.') }
      } finally { polling = false; if (active) setLoading(false) }
    }
    setLoading(true)
    void poll()
    const timer = window.setInterval(() => void poll(), 10_000)
    return () => { active = false; window.clearInterval(timer) }
  }, [id, refresh, deploymentStatus])

  async function change(action: StackAction) {
    if (lock.current) return
    lock.current = true; setBusy(action); setOutcomes([])
    try {
      // Choose current container IDs immediately before the action, as on the stack cards.
      const [listing, inventory] = await Promise.all([get<{ stacks: StackSummary[] }>('/api/stacks'), get<{ hosts: HostEntry[] }>('/api/containers')])
      setOutcomes(await runStackActions(listing.stacks, inventory.hosts, role, action, id))
    } catch (cause) { setOutcomes([{ name: 'Stack action', ok: false, message: cause instanceof Error ? cause.message : 'The action failed.' }]) }
    finally { lock.current = false; setBusy(null); setRefresh((value) => value + 1) }
  }

  return <StackRuntimeView data={data} role={role} busy={busy} loading={loading} error={error} outcomes={outcomes} onAction={(action) => void change(action)} onRefresh={() => setRefresh((value) => value + 1)} onNavigate={onNavigate} />
}

export function StackRuntimeView({ data, role, busy, loading, error, outcomes, onAction, onRefresh, onNavigate }: {
  data: RuntimeData | null; role: string; busy: StackAction | null; loading: boolean; error: string; outcomes: Outcome[]
  onAction: (action: StackAction) => void; onRefresh: () => void; onNavigate: (route: Route) => void
}) {
  const stack = data?.stack
  const host = data?.hosts.find((host) => host.agentId === stack?.agentId)
  const blocker = data ? stackActionBlocker(data.stack, data.hosts, role) : 'Live container information is unavailable.'
  const badge = data ? stackRuntimeBadge(data.stack, data.hosts) : null
  const serviceCount = new Set(data?.containers.map(({ container }) => container.service || container.id)).size
  const missing = stack && host?.state === 'ok' && stack.serviceCount > serviceCount
  return <Card withBorder radius="md" padding="lg"><Stack gap="sm">
    <Group justify="space-between"><Group><Title order={3}>Containers</Title>{badge && <Badge color={badge.color} variant="light" style={{ whiteSpace: 'normal', height: 'auto', minHeight: 20 }}>{badge.label}</Badge>}</Group><Group gap="xs">
      <Button size="xs" variant="light" color="green" title={blocker ?? 'Start existing stack containers'} loading={busy === 'start'} disabled={!data || !!busy || loading || !!blocker || !stackActionTargets(data.stack, data.hosts, 'start').length} onClick={() => onAction('start')}>Start</Button>
      <Button size="xs" variant="light" color="red" title={blocker ?? 'Stop stack containers'} loading={busy === 'stop'} disabled={!data || !!busy || loading || !!blocker || !stackActionTargets(data.stack, data.hosts, 'stop').length} onClick={() => onAction('stop')}>Stop</Button>
      <Button size="xs" variant="default" loading={loading} disabled={!!busy} onClick={onRefresh}>Refresh containers</Button>
    </Group></Group>
    {stack && <Text size="sm" c="dimmed">Device: {stack.deviceName || host?.name || stack.agentId || 'Unassigned'} · Project: {stack.projectName}{stack.directory ? ` · ${stack.directory}` : ''}</Text>}
    {loading && !data && <Text role="status" size="sm" c="dimmed">Loading live container details…</Text>}
    {error && <Alert color="red" title="Container information unavailable">{error}</Alert>}
    {stack && (!host || host.state !== 'ok') && <Alert color="yellow" title="Device unavailable">{host?.message || 'The device is not connected or Docker access is unavailable.'}</Alert>}
    {host?.truncated && <Alert color="yellow">The device returned an incomplete container list. Counts may be partial and Start/Stop is disabled.</Alert>}
    {missing && <Alert color="red" title="Missing containers">{serviceCount} of {stack.serviceCount} saved services have containers{host?.truncated ? ' in this partial list' : ''}. Start only starts existing containers. Use Preview deployment to review creating missing services.</Alert>}
    {!!outcomes.length && <Alert color={outcomes.some((outcome) => !outcome.ok) ? 'red' : 'green'} title="Stack action results">{outcomes.map((outcome, index) => <Text key={index} size="sm">{outcome.name}: {outcome.message}</Text>)}</Alert>}
    {data && host?.state === 'ok' && !data.containers.length && <Text size="sm" c="dimmed">No containers currently exist for this stack on this device.</Text>}
    <SimpleGrid cols={{ base: 1, lg: 2 }}>{data?.containers.map(({ container, inspect, error }) => {
      const state = inspect?.state
      const status = state?.status || container.state
      return <Card withBorder key={container.id} radius="md" padding="md"><Stack gap="xs">
        <Group justify="space-between"><Text fw={600}>{container.name || container.id.slice(0, 12)}</Text><Group gap="xs"><Badge color={stateColors[status] ?? 'gray'}>{status}</Badge>{state?.health && <Badge color={state.health === 'healthy' ? 'green' : 'red'}>{state.health}</Badge>}</Group></Group>
        <Text size="sm">Service: {container.service || 'Unknown'}</Text><Text size="xs" c="dimmed" style={{ overflowWrap: 'anywhere' }}>{inspect?.image || container.image}</Text>
        <Text size="sm">{container.status}</Text>
        {state && <Text size="sm">Exit code: {state.exitCode} · Restarts: {inspect?.restartCount ?? 0}</Text>}
        {state?.oomKilled && <Alert color="red" title="Out of memory">Docker reports that this container was killed after exceeding its memory allowance.</Alert>}
        {state?.error && <Alert color="red" title="Container error"><Text size="sm" style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{state.error}</Text></Alert>}
        {state && !state.running && state.exitCode !== 0 && !state.error && <Alert color="red" title="Container exited">The process exited with code {state.exitCode}. Open logs for the application error.</Alert>}
        {error && <Alert color="yellow" title="Could not inspect container">{error}</Alert>}
        {inspect?.user && <Text size="xs" c="dimmed">Configured user: {inspect.user}</Text>}
        {state?.startedAt && !state.startedAt.startsWith('0001') && <Text size="xs" c="dimmed">Started: {new Date(state.startedAt).toLocaleString()}</Text>}
        {state?.finishedAt && !state.finishedAt.startsWith('0001') && <Text size="xs" c="dimmed">Finished: {new Date(state.finishedAt).toLocaleString()}</Text>}
        {(inspect?.ports?.length || container.ports?.length) ? <Text size="xs" c="dimmed">Ports: {portSummary(inspect?.ports ?? container.ports)}</Text> : null}
        <Group gap="xs"><Button size="xs" variant="default" onClick={() => stack && onNavigate(routeOf('containers', stack.agentId, container.id, 'overview'))}>Container details</Button><Button size="xs" variant="light" onClick={() => stack && onNavigate(routeOf('containers', stack.agentId, container.id, 'logs'))}>Logs</Button></Group>
      </Stack></Card>
    })}</SimpleGrid>
  </Stack></Card>
}
