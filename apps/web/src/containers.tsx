import { useCallback, useEffect, useMemo, useState } from 'react'
import { Alert, Badge, Button, Card, Group, Loader, Pagination, Select, Stack, Switch, Table, Text, TextInput, Title } from '@mantine/core'
import { ContainerDetail } from './container-detail'
import { filterRows, fleetRows, groupRows, paginate, portSummary, stateColors, stateCounts, type FleetRow, type HostEntry } from './container-utils'
import { route as routeOf, type Route } from './route'

const pageSize = 25
const tierLabels: Record<string, { label: string; color: string; hint: string }> = {
  read: { label: 'Read only', color: 'gray', hint: 'Containers can be viewed but not changed.' },
  manage: { label: 'Manage', color: 'blue', hint: 'Containers, images, networks and volumes can be changed.' },
  exec: { label: 'Manage + terminal', color: 'orange', hint: 'Includes interactive terminals inside containers.' },
  deploy: { label: 'Full control', color: 'red', hint: 'Includes deploying Compose stacks.' },
}
const hostStateColors: Record<string, string> = { ok: 'green', offline: 'gray', unavailable: 'yellow', error: 'red' }
const hostStateLabels: Record<string, string> = { ok: 'Connected', offline: 'Offline', unavailable: 'Docker not enabled', error: 'Error' }

type Props = { route: Route; role: string; onNavigate: (route: Route) => void }
type PingResult = { state: 'running' } | { state: 'ok'; roundTripMillis: number } | { state: 'error'; message: string }

async function getFleet(): Promise<HostEntry[]> {
  const response = await fetch('/api/containers', { credentials: 'same-origin' })
  if (!response.ok) throw new Error((await response.text()).trim().slice(0, 200) || 'The container list could not be loaded.')
  return ((await response.json()) as { hosts: HostEntry[] }).hosts
}

export function Containers({ route, role, onNavigate }: Props) {
  const [hosts, setHosts] = useState<HostEntry[] | null>(null)
  const [error, setError] = useState('')
  const refresh = useCallback(async () => {
    try { setHosts(await getFleet()); setError('') } catch (cause) { setError(cause instanceof Error ? cause.message : 'The container list could not be loaded.') }
  }, [])

  useEffect(() => {
    const first = window.setTimeout(() => { void refresh() }, 0)
    const timer = window.setInterval(() => { void refresh() }, 10_000)
    return () => { window.clearTimeout(first); window.clearInterval(timer) }
  }, [refresh])

  if (route.id && route.sub) {
    return <ContainerDetail hostId={route.id} reference={route.sub} tab={route.tab ?? 'overview'} hosts={hosts} role={role} onNavigate={onNavigate} />
  }
  return <Fleet hosts={hosts} error={error} refresh={refresh} onNavigate={onNavigate} />
}

function Fleet({ hosts, error, refresh, onNavigate }: { hosts: HostEntry[] | null; error: string; refresh: () => Promise<void>; onNavigate: (route: Route) => void }) {
  const [search, setSearch] = useState('')
  const [hostId, setHostId] = useState<string | null>(null)
  const [state, setState] = useState<string | null>(null)
  const [grouped, setGrouped] = useState(false)
  const [page, setPage] = useState(1)
  const [pings, setPings] = useState<Record<string, PingResult>>({})

  const rows = useMemo(() => fleetRows(hosts ?? []), [hosts])
  const filtered = useMemo(() => filterRows(rows, { search, hostId, state }), [rows, search, hostId, state])
  const counts = useMemo(() => stateCounts(rows), [rows])
  const shown = paginate(filtered, page, pageSize)
  const states = Object.keys(counts).sort()

  async function ping(agentId: string) {
    setPings((current) => ({ ...current, [agentId]: { state: 'running' } }))
    try {
      const response = await fetch(`/api/channel/agents/${encodeURIComponent(agentId)}/ping`, { method: 'POST', credentials: 'same-origin' })
      if (!response.ok) throw new Error((await response.text()).trim().slice(0, 200) || 'The host did not answer.')
      const body = await response.json() as { roundTripMillis: number }
      setPings((current) => ({ ...current, [agentId]: { state: 'ok', roundTripMillis: body.roundTripMillis } }))
    } catch (cause) {
      setPings((current) => ({ ...current, [agentId]: { state: 'error', message: cause instanceof Error ? cause.message : 'The host did not answer.' } }))
    }
  }

  const open = (row: FleetRow) => onNavigate(routeOf('containers', row.hostId, row.id))
  const renderRow = (row: FleetRow, showHost: boolean) => <Table.Tr key={`${row.hostId}/${row.id}`} className="device-row" tabIndex={0} aria-label={`Open container ${row.name}`}
    onClick={() => open(row)} onKeyDown={(event) => { if (event.target === event.currentTarget && (event.key === 'Enter' || event.key === ' ')) { event.preventDefault(); open(row) } }}>
    <Table.Td><Text fw={600} size="sm">{row.name || row.id.slice(0, 12)}</Text>{row.service && <Text size="xs" c="dimmed">{row.service}</Text>}</Table.Td>
    <Table.Td><Text size="sm" style={{ wordBreak: 'break-all' }}>{row.image}</Text></Table.Td>
    <Table.Td><Badge color={stateColors[row.state] ?? 'gray'} variant="light">{row.state}</Badge><Text size="xs" c="dimmed" mt={2}>{row.status}</Text></Table.Td>
    <Table.Td><Text size="sm">{portSummary(row.ports) || '–'}</Text></Table.Td>
    {showHost && <Table.Td><Text size="sm">{row.hostName}</Text></Table.Td>}
  </Table.Tr>
  const head = (showHost: boolean) => <Table.Thead><Table.Tr><Table.Th>Name</Table.Th><Table.Th>Image</Table.Th><Table.Th>State</Table.Th><Table.Th>Ports</Table.Th>{showHost && <Table.Th>Host</Table.Th>}</Table.Tr></Table.Thead>

  return <>
    <Text size="xs" fw={700} c="dimmed" tt="uppercase">Home infrastructure</Text>
    <Title order={1} mt={4}>Containers</Title>
    <Text c="dimmed" mt={6}>Every container on your connected hosts. Open one to see its details, logs and live resource use.</Text>
    {error && <Alert color="red" mt="md" title="The container list could not be loaded">{error}</Alert>}

    <Card withBorder radius="md" padding="lg" mt="lg">
      <Group justify="space-between" mb="sm"><Title order={3}>Hosts</Title><Button variant="default" size="xs" onClick={() => void refresh()}>Refresh</Button></Group>
      {hosts === null && !error ? <Group><Loader size="sm" /><Text c="dimmed">Loading…</Text></Group>
        : hosts && hosts.length === 0 ? <Text c="dimmed">No hosts have enrolled yet. Add a device first, then enable Docker on its agent.</Text>
        : <Stack gap="xs">{hosts?.map((host) => {
          const tier = host.dockerTier ? (tierLabels[host.dockerTier] ?? tierLabels.read) : null
          const result = pings[host.agentId]
          return <Group key={host.agentId} justify="space-between" wrap="nowrap" align="flex-start">
            <div>
              <Group gap="xs"><Text fw={600} size="sm">{host.name}</Text><Badge color={hostStateColors[host.state] ?? 'gray'} variant="light">{hostStateLabels[host.state] ?? host.state}</Badge>
                {tier && <Badge color={tier.color} variant="outline" title={tier.hint}>{tier.label}</Badge>}
                {host.state === 'ok' && <Text size="xs" c="dimmed">{host.containers.length} container{host.containers.length === 1 ? '' : 's'}{host.truncated ? ' (list truncated)' : ''}</Text>}</Group>
              {host.message && host.state !== 'ok' && <Text size="xs" c="dimmed" mt={2}>{host.message}</Text>}
            </div>
            {host.state !== 'offline' && <Group gap="xs" wrap="nowrap">
              {result?.state === 'ok' && <Text size="xs" c="green">Replied in {result.roundTripMillis} ms</Text>}
              {result?.state === 'error' && <Text size="xs" c="red">{result.message}</Text>}
              <Button size="xs" variant="light" loading={result?.state === 'running'} onClick={() => void ping(host.agentId)}>Test connection</Button>
            </Group>}
          </Group>
        })}</Stack>}
    </Card>

    {rows.length > 0 && <Card withBorder radius="md" padding="lg" mt="lg">
      <Group gap="sm" mb="md" align="flex-end">
        <TextInput label="Search" placeholder="Name, image, project or host" value={search} onChange={(event) => { setSearch(event.currentTarget.value); setPage(1) }} style={{ flex: '1 1 220px' }} />
        <Select label="Host" placeholder="All hosts" clearable value={hostId} onChange={(value) => { setHostId(value); setPage(1) }} data={(hosts ?? []).filter((host) => host.containers.length > 0).map((host) => ({ value: host.agentId, label: host.name }))} />
        <Select label="State" placeholder="Any state" clearable value={state} onChange={(value) => { setState(value); setPage(1) }} data={states.map((value) => ({ value, label: `${value} (${counts[value]})` }))} />
        <Switch label="Group by Compose project" checked={grouped} onChange={(event) => { setGrouped(event.currentTarget.checked); setPage(1) }} mb={6} />
      </Group>
      {filtered.length === 0 ? <Text c="dimmed">No containers match these filters.</Text>
        : grouped ? <Stack gap="lg">{groupRows(shown.items).map((group) => <div key={group.key}>
          <Group gap="xs" mb={4}><Text fw={700} size="sm">{group.title}</Text><Text size="xs" c="dimmed">on {group.subtitle}</Text><Badge variant="light" color="gray">{group.rows.length}</Badge></Group>
          <Table.ScrollContainer minWidth={560}><Table highlightOnHover verticalSpacing="xs">{head(false)}<Table.Tbody>{group.rows.map((row) => renderRow(row, false))}</Table.Tbody></Table></Table.ScrollContainer></div>)}</Stack>
        : <Table.ScrollContainer minWidth={640}><Table highlightOnHover verticalSpacing="xs">{head(true)}<Table.Tbody>{shown.items.map((row) => renderRow(row, true))}</Table.Tbody></Table></Table.ScrollContainer>}
      <Group justify="space-between" mt="md">
        <Text size="xs" c="dimmed">{filtered.length} of {rows.length} containers</Text>
        {shown.pages > 1 && <Pagination total={shown.pages} value={shown.page} onChange={setPage} size="sm" />}
      </Group>
    </Card>}
  </>
}
