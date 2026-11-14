import { useEffect, useState } from 'react'
import { Alert, Badge, Button, Card, Group, Modal, Stack, Text, Title } from '@mantine/core'
import { postAction, streamRecreate } from './container-actions'
import { StatsPanel } from './container-detail'
import { groupRows, stateColors, type FleetRow, type HostEntry } from './container-utils'
import { HistoricalLogSearch, LogValues } from './log-values'
import { resourceRequest } from './resource-access'

type SharedHost = HostEntry & { access?: Record<string, string[]> }
type Operation = 'restart' | 'recreate' | 'pull-recreate'
const labels: Record<Operation, string> = { restart: 'Restart', recreate: 'Recreate', 'pull-recreate': 'Pull & recreate' }
type Selection = { operation: Operation; targets: FleetRow[] }

export function SharedResources() {
  const [hosts, setHosts] = useState<SharedHost[]>([])
  const [error, setError] = useState('')
  const [selection, setSelection] = useState<Selection | null>(null)
  const [busy, setBusy] = useState(false)
  const [progress, setProgress] = useState('')
  const [actionError, setActionError] = useState('')
  const [revision, setRevision] = useState(0)
  const [logs, setLogs] = useState('')
  const [stats, setStats] = useState('')
  const [loaded, setLoaded] = useState(false)
  useEffect(() => {
    const controller = new AbortController(); let timer: number | undefined
    const load = async () => {
      try { const result = await resourceRequest<{ hosts: SharedHost[] }>('/api/containers', 'GET', undefined, controller.signal); if (!controller.signal.aborted) { setHosts(result.hosts); setError(''); setLoaded(true) } }
      catch (cause) { if (!controller.signal.aborted) { setHosts([]); setError(String(cause)) } }
      finally { if (!controller.signal.aborted) timer = window.setTimeout(() => void load(), 10000) }
    }
    void load(); return () => { controller.abort(); window.clearTimeout(timer) }
  }, [revision])
  const groups = groupRows(hosts.flatMap((host) => host.containers.map((container) => ({ ...container, hostId: host.agentId, hostName: host.name, hostTier: host.dockerTier }))))
  const allowed = (row: FleetRow, operation: string) => hosts.find((host) => host.agentId === row.hostId)?.access?.[row.id]?.includes(operation)
  function choose(operation: Operation, targets: FleetRow[]) { setActionError(''); setProgress(''); setSelection({ operation, targets }) }
  async function run() {
    if (!selection) return
    setBusy(true); setActionError('')
    try {
      for (const row of selection.targets) {
        setProgress(`${labels[selection.operation]}: ${row.name}`)
        if (selection.operation === 'restart') await postAction(row.hostId, row.id, 'restart')
        else await streamRecreate(row.hostId, row.id, selection.operation === 'pull-recreate', (step) => setProgress(`${row.name}: ${step.message || step.step}`))
      }
      setSelection(null)
    } catch (cause) { setActionError(String(cause)) } finally { setBusy(false); setRevision((current) => current + 1) }
  }
  return <section><Stack gap="lg">
    <div><Title order={1}>Shared resources</Title><Text c="dimmed">Your stacks, containers, and shared server information.</Text></div>
    {error && <Alert color="red" role="alert">{error}</Alert>}
    {!loaded && !error && <Text>Loading your resources…</Text>}
    {loaded && !hosts.length && !error && <Alert color="gray">No resources have been shared with this account yet.</Alert>}
    {hosts.filter((host) => host.state !== 'ok').map((host) => <Alert key={host.agentId} color="orange" title={host.name}>This host is unavailable. Current values and controls cannot be verified.</Alert>)}
    {groups.map((group) => <Card key={group.key} withBorder radius="md"><Stack>
      <Group justify="space-between"><div><Title order={3}>{group.title}</Title><Text c="dimmed" size="sm">{group.subtitle}</Text></div>
        {group.rows.length > 1 && <Group>{(['restart', 'recreate', 'pull-recreate'] as const).filter((operation) => group.rows.every((row) => allowed(row, operation))).map((operation) => <Button key={operation} size="xs" variant="light" disabled={busy || !!error} onClick={() => choose(operation, group.rows)}>{labels[operation]} all listed</Button>)}</Group>}
      </Group>
      {group.rows.map((row) => <Card key={row.id} withBorder radius="md"><Stack gap="sm">
        <Group justify="space-between"><div><Text fw={600}>{row.service || row.name}</Text><Text size="xs" c="dimmed" style={{ overflowWrap: 'anywhere' }}>{row.image}</Text><Text size="xs" c="dimmed">{row.status}</Text></div><Badge color={stateColors[row.state] || 'gray'}>{row.state}</Badge></Group>
        <Group>{(['restart', 'recreate', 'pull-recreate'] as const).filter((operation) => allowed(row, operation)).map((operation) => <Button key={operation} size="xs" variant="light" disabled={busy || !!error} onClick={() => choose(operation, [row])}>{labels[operation]}</Button>)}
          <Button size="xs" variant="default" onClick={() => setStats(stats === row.id ? '' : row.id)}>{stats === row.id ? 'Hide stats' : 'Live stats'}</Button>
          {allowed(row, 'logs') && <Button size="xs" variant="default" onClick={() => setLogs(logs === row.id ? '' : row.id)}>{logs === row.id ? 'Hide logs' : 'Search logs'}</Button>}</Group>
        <LogValues hostId={row.hostId} reference={row.id} suspended={busy} />
        {logs === row.id && <HistoricalLogSearch hostId={row.hostId} reference={row.id} />}
        {stats === row.id && <StatsPanel hostId={row.hostId} reference={row.id} running={row.state === 'running'} />}
      </Stack></Card>)}
    </Stack></Card>)}
    <Modal title={selection ? `${labels[selection.operation]} selected resources` : 'Container action'} opened={!!selection} onClose={() => { if (!busy) setSelection(null) }}><Stack>
      <Text>This will interrupt players and other users of {selection?.targets.map((row) => row.name).join(', ')}. Containers are processed one at a time; processing stops if an action fails.</Text>
      {selection?.operation === 'pull-recreate' && <Text>A new image will be pulled using the existing image reference. An image pinned to a digest stays on that digest.</Text>}
      {progress && <Text size="sm" role="status">{progress}</Text>}{actionError && <Alert color="red" role="alert">{actionError}</Alert>}
      <Group justify="flex-end"><Button variant="default" disabled={busy} onClick={() => setSelection(null)}>Cancel</Button><Button loading={busy} onClick={() => void run()}>Confirm {selection ? labels[selection.operation].toLowerCase() : 'action'}</Button></Group>
    </Stack></Modal>
  </Stack></section>
}
