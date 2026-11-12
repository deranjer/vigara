import { Fragment, useCallback, useEffect, useMemo, useState } from 'react'
import { Alert, Button, Checkbox, Group, Loader, Menu, SegmentedControl, Table, Text, TextInput, Title, UnstyledButton } from '@mantine/core'
import { IconChevronRight, IconColumns3, IconDots, IconSearch } from '@tabler/icons-react'
import { useAtom } from 'jotai'
import { BulkBar } from './container-actions'
import { ContainerDetail } from './container-detail'
import { imageRef, matchesSearch, matchesState, rowTone, stackGroups, stateFilterCounts, stateText, usageByContainer, usageKey, type StateFilter, type Usage } from './container-list-utils'
import { ContainerPanel } from './container-panel'
import { canManage, fleetRows, formatBytes, portSummary, type FleetRow, type HostEntry } from './container-utils'
import { loadAllDevices } from './device-types'
import { route as routeOf, type Route } from './route'
import { deviceScopeAtom } from './scope'

type Props = { route: Route; role: string; onNavigate: (route: Route) => void }
type PingResult = { state: 'running' } | { state: 'ok'; roundTripMillis: number } | { state: 'error'; message: string }
type Layout = 'stack' | 'flat'
type Columns = { cpu: boolean; memory: boolean }

const tierLabels: Record<string, string> = { read: 'read only', manage: 'manage', exec: 'manage + terminal', deploy: 'full control' }
const hostStateLabels: Record<string, string> = { ok: 'connected', offline: 'offline', unavailable: 'Docker not enabled', error: 'error' }

function stored<T>(key: string, fallback: T): T {
  try { const raw = window.localStorage.getItem(key); return raw ? { ...fallback, ...JSON.parse(raw) } as T : fallback } catch { return fallback }
}
function store(key: string, value: unknown) {
  try { window.localStorage.setItem(key, JSON.stringify(value)) } catch { /* The choice lasts for this visit when storage is unavailable. */ }
}

async function getFleet(): Promise<HostEntry[]> {
  const response = await fetch('/api/containers', { credentials: 'same-origin' })
  if (!response.ok) throw new Error((await response.text()).trim().slice(0, 200) || 'The container list could not be loaded.')
  return ((await response.json()) as { hosts: HostEntry[] }).hosts
}

/** Live CPU and memory per container, from each device's latest telemetry. Loaded only when a column or the panel needs it. */
function useUsage(enabled: boolean): Map<string, Usage> {
  const [usage, setUsage] = useState<Map<string, Usage>>(new Map())
  useEffect(() => {
    if (!enabled) return
    let active = true
    const load = () => void loadAllDevices().then((devices) => { if (active) setUsage(usageByContainer(devices)) }).catch(() => undefined)
    load()
    const timer = window.setInterval(load, 30_000)
    return () => { active = false; window.clearInterval(timer) }
  }, [enabled])
  return usage
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

  // A container with a tab in its address (#/containers/<host>/<name>/logs) is the full page;
  // without one it opens in the side panel over the list.
  if (route.id && route.sub && route.tab) {
    // Keyed so moving to another container starts with fresh state: no stale details, banners, or open dialogs.
    return <ContainerDetail key={`${route.id}/${route.sub}`} hostId={route.id} reference={route.sub} tab={route.tab} hosts={hosts} role={role} onNavigate={onNavigate} refreshFleet={() => void refresh()} />
  }
  return <Fleet hosts={hosts} error={error} role={role} refresh={refresh} onNavigate={onNavigate} open={route.id && route.sub ? { hostId: route.id, reference: route.sub } : null} />
}

function Fleet({ hosts, error, role, refresh, onNavigate, open }: { hosts: HostEntry[] | null; error: string; role: string; refresh: () => Promise<void>; onNavigate: (route: Route) => void; open: { hostId: string; reference: string } | null }) {
  const [search, setSearch] = useState('')
  const [scope, setScope] = useAtom(deviceScopeAtom)
  const [state, setState] = useState<StateFilter>('all')
  const [layout, setLayout] = useState<Layout>(() => stored<{ layout: Layout }>('vigara.containers.view', { layout: 'stack' }).layout)
  const [columns, setColumns] = useState<Columns>(() => stored<Columns>('vigara.containers.columns', { cpu: false, memory: false }))
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set())
  const [pings, setPings] = useState<Record<string, PingResult>>({})
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const usage = useUsage(columns.cpu || columns.memory || open !== null)

  const allRows = useMemo(() => fleetRows(hosts ?? []), [hosts])
  const scoped = useMemo(() => allRows.filter((row) => !scope || row.hostId === scope), [allRows, scope])
  const counts = useMemo(() => stateFilterCounts(scoped), [scoped])
  const rows = useMemo(() => scoped.filter((row) => matchesState(row, state) && matchesSearch(row, search)), [scoped, state, search])
  const groups = useMemo(() => stackGroups(rows), [rows])
  const hostsWithContainers = new Set(scoped.map((row) => row.hostId)).size
  const showDevice = hostsWithContainers > 1
  const scopeName = scope ? hosts?.find((h) => h.agentId === scope)?.name ?? 'one device' : null
  const problemHosts = (hosts ?? []).filter((h) => h.state !== 'ok' && (!scope || h.agentId === scope))

  const key = (row: FleetRow) => `${row.hostId}/${row.id}`
  const selectable = (row: FleetRow) => canManage(role, row.hostTier)
  const showSelect = scoped.some(selectable)
  const chosen = allRows.filter((row) => selected.has(key(row)))
  const toggle = (row: FleetRow, on: boolean) => setSelected((current) => { const next = new Set(current); if (on) next.add(key(row)); else next.delete(key(row)); return next })
  const toggleMany = (list: FleetRow[], on: boolean) => setSelected((current) => { const next = new Set(current); for (const row of list.filter(selectable)) { if (on) next.add(key(row)); else next.delete(key(row)) } return next })
  // Containers are opened by name, which survives a recreate; the ID changes.
  const openRow = (row: FleetRow) => onNavigate(routeOf('containers', row.hostId, row.name || row.id))
  const isOpen = (row: FleetRow) => !!open && open.hostId === row.hostId && (open.reference === row.name || open.reference === row.id)
  const openRowData = open ? allRows.find((row) => row.hostId === open.hostId && (row.name === open.reference || row.id === open.reference)) ?? null : null

  async function pingAll() {
    for (const host of (hosts ?? []).filter((h) => h.state !== 'offline')) {
      setPings((current) => ({ ...current, [host.agentId]: { state: 'running' } }))
      void fetch(`/api/channel/agents/${encodeURIComponent(host.agentId)}/ping`, { method: 'POST', credentials: 'same-origin' })
        .then(async (response) => {
          if (!response.ok) throw new Error((await response.text()).trim().slice(0, 200) || 'The host did not answer.')
          const body = await response.json() as { roundTripMillis: number }
          setPings((current) => ({ ...current, [host.agentId]: { state: 'ok', roundTripMillis: body.roundTripMillis } }))
        })
        .catch((cause: unknown) => setPings((current) => ({ ...current, [host.agentId]: { state: 'error', message: cause instanceof Error ? cause.message : 'The host did not answer.' } })))
    }
  }

  const columnCount = 4 + (showSelect ? 1 : 0) + (showDevice ? 1 : 0) + (columns.cpu ? 1 : 0) + (columns.memory ? 1 : 0)
  const renderRow = (row: FleetRow) => {
    const use = row.state === 'running' ? usage.get(usageKey(row)) : undefined
    return <Table.Tr key={key(row)} className="device-row" data-open={isOpen(row) || undefined} tabIndex={0} aria-label={`Open container ${row.name}`}
      onClick={() => openRow(row)} onKeyDown={(event) => { if (event.target === event.currentTarget && (event.key === 'Enter' || event.key === ' ')) { event.preventDefault(); openRow(row) } }}>
      {showSelect && <Table.Td onClick={(event) => event.stopPropagation()} w={36}>{selectable(row) && <Checkbox size="xs" aria-label={`Select ${row.name}`} checked={selected.has(key(row))} onChange={(event) => toggle(row, event.currentTarget.checked)} />}</Table.Td>}
      <Table.Td><span className="vg-cell-name"><span className="vg-status-dot" data-tone={rowTone(row)} aria-hidden="true" /><span><b>{row.name || row.id.slice(0, 12)}</b>{layout === 'flat' && row.project ? <small>{row.project}</small> : row.service && row.service !== row.name ? <small>{row.service}</small> : null}</span></span></Table.Td>
      <Table.Td><Text size="sm" c={rowTone(row) === 'warn' ? 'orange.7' : rowTone(row) === 'idle' ? 'dimmed' : undefined} className="vg-nowrap">{stateText(row)}</Text></Table.Td>
      <Table.Td className="vg-wide"><span className="vg-mono vg-ellipsis" title={row.image}>{imageRef(row.image)}</span></Table.Td>
      <Table.Td className="vg-wide"><span className="vg-mono vg-dim">{portSummary(row.ports) || '—'}</span></Table.Td>
      {showDevice && <Table.Td className="vg-wide"><Text size="sm" c="dimmed" className="vg-nowrap">{row.hostName}</Text></Table.Td>}
      {columns.cpu && <Table.Td ta="right" className="vg-num vg-wide">{use?.cpu == null ? '—' : `${use.cpu.toFixed(1)}%`}</Table.Td>}
      {columns.memory && <Table.Td ta="right" className="vg-num">{use?.memoryBytes == null ? '—' : formatBytes(use.memoryBytes)}</Table.Td>}
    </Table.Tr>
  }

  return <div className="vg-page vg-containers" data-panel={open ? 'open' : undefined}>
    <Group justify="space-between" align="flex-end" mb="md">
      <div>
        <Title order={1}>Containers</Title>
        <Text size="sm" c="dimmed" mt={4}>{hosts === null ? 'Loading…' : `${scoped.length} on ${scopeName ?? `${hostsWithContainers} device${hostsWithContainers === 1 ? '' : 's'}`}`}{scopeName && <> · <a href="#" onClick={(e) => { e.preventDefault(); setScope(null) }}>show all devices</a></>}</Text>
      </div>
      <Group gap="xs">
        {Object.entries(pings).map(([agentId, result]) => <Text key={agentId} size="xs" c={result.state === 'error' ? 'red' : 'dimmed'}>{hosts?.find((h) => h.agentId === agentId)?.name}: {result.state === 'running' ? '…' : result.state === 'ok' ? `${result.roundTripMillis} ms` : 'no answer'}</Text>)}
        <Button variant="default" size="xs" onClick={() => void refresh()}>Refresh</Button>
        <Menu position="bottom-end" withinPortal>
          <Menu.Target><Button variant="default" size="xs" px={8} aria-label="More"><IconDots size={16} /></Button></Menu.Target>
          <Menu.Dropdown>
            <Menu.Item onClick={() => void pingAll()}>Test connection to every device</Menu.Item>
            <Menu.Label>Docker access</Menu.Label>
            {(hosts ?? []).map((h) => <Menu.Item key={h.agentId} disabled>{h.name}: {h.state === 'ok' ? tierLabels[h.dockerTier ?? 'read'] ?? h.dockerTier : hostStateLabels[h.state] ?? h.state}</Menu.Item>)}
          </Menu.Dropdown>
        </Menu>
      </Group>
    </Group>
    {error && <Alert color="red" mb="md" title="The container list could not be loaded">{error}</Alert>}
    {problemHosts.length > 0 && <Text size="xs" c="dimmed" mb="sm">Not listed: {problemHosts.map((h) => `${h.name} (${hostStateLabels[h.state] ?? h.state})`).join(', ')}</Text>}

    <div className="vg-toolbar vg-filterbar">
      <TextInput size="xs" leftSection={<IconSearch size={14} />} placeholder="Filter by name, image, port…" value={search} onChange={(event) => setSearch(event.currentTarget.value)} className="vg-filter" aria-label="Filter containers" />
      <SegmentedControl size="xs" value={state} onChange={(v) => setState(v as StateFilter)} aria-label="State" data={(['all', 'running', 'stopped', 'unhealthy'] as const).map((value) => ({ value, label: `${value[0].toUpperCase()}${value.slice(1)} ${counts[value]}` }))} />
      <span className="vg-grow" />
      <SegmentedControl size="xs" value={layout} onChange={(v) => { setLayout(v as Layout); store('vigara.containers.view', { layout: v }) }} data={[{ value: 'stack', label: 'By stack' }, { value: 'flat', label: 'Flat' }]} aria-label="Grouping" />
      <Menu position="bottom-end" closeOnItemClick={false} withinPortal>
        <Menu.Target><Button size="xs" variant="default" leftSection={<IconColumns3 size={14} />}>Columns</Button></Menu.Target>
        <Menu.Dropdown>
          <Menu.Label>Live from telemetry</Menu.Label>
          {(['cpu', 'memory'] as const).map((column) => <Menu.Item key={column} leftSection={<Checkbox size="xs" readOnly checked={columns[column]} tabIndex={-1} />} onClick={() => { const next = { ...columns, [column]: !columns[column] }; setColumns(next); store('vigara.containers.columns', next) }}>{column === 'cpu' ? 'CPU' : 'Memory'}</Menu.Item>)}
        </Menu.Dropdown>
      </Menu>
    </div>
    {chosen.length > 0 && <BulkBar rows={chosen} role={role} onClear={() => setSelected(new Set())} onFinished={() => void refresh()} />}

    {hosts === null && !error ? <div className="device-empty"><Loader size="sm" /></div>
      : hosts?.length === 0 ? <div className="device-empty"><Text fw={600}>No devices yet</Text><Text c="dimmed" size="sm" mt={6}>Add a device, then enable Docker on its agent.</Text></div>
      : <div className="vg-panel"><div className="table-scroll"><Table className="vg-table vg-container-table" highlightOnHover verticalSpacing={7}>
        <Table.Thead><Table.Tr>
          {showSelect && <Table.Th w={36}><Checkbox size="xs" aria-label="Select all shown" checked={rows.filter(selectable).length > 0 && rows.filter(selectable).every((row) => selected.has(key(row)))} onChange={(event) => toggleMany(rows, event.currentTarget.checked)} /></Table.Th>}
          <Table.Th>Name</Table.Th><Table.Th>State</Table.Th><Table.Th className="vg-wide">Image</Table.Th><Table.Th className="vg-wide">Ports</Table.Th>{showDevice && <Table.Th className="vg-wide">Device</Table.Th>}{columns.cpu && <Table.Th ta="right" className="vg-wide">CPU</Table.Th>}{columns.memory && <Table.Th ta="right">Memory</Table.Th>}
        </Table.Tr></Table.Thead>
        <Table.Tbody>
          {rows.length === 0 && <Table.Tr><Table.Td colSpan={columnCount}><Text c="dimmed" size="sm" py="sm">{scoped.length ? 'No containers match these filters.' : 'No containers.'}</Text></Table.Td></Table.Tr>}
          {layout === 'flat' ? [...rows].sort((a, b) => a.name.localeCompare(b.name)).map(renderRow) : groups.map((group) => {
            const shut = collapsed.has(group.key)
            return <Fragment key={group.key}>
              <Table.Tr className="vg-group-row">
                <Table.Td colSpan={columnCount}>
                  <UnstyledButton className="vg-group-toggle" aria-expanded={!shut} onClick={() => setCollapsed((current) => { const next = new Set(current); if (shut) next.delete(group.key); else next.add(group.key); return next })}>
                    <IconChevronRight size={14} className="vg-caret" aria-hidden="true" />
                    <span className="vg-status-dot" data-tone={group.tone} aria-hidden="true" />
                    <b>{group.project || 'Standalone'}</b>
                    <span className="vg-dim">{group.running}/{group.rows.length} running{group.unhealthy ? ` · ${group.unhealthy} unhealthy` : ''}{showDevice ? ` · ${group.hostName}` : ''}</span>
                  </UnstyledButton>
                  {group.project && <Button size="compact-xs" variant="subtle" className="vg-group-link" onClick={() => onNavigate(routeOf('stacks'))}>Stack</Button>}
                </Table.Td>
              </Table.Tr>
              {!shut && group.rows.map(renderRow)}
            </Fragment>
          })}
        </Table.Tbody>
      </Table></div></div>}

    {open && <ContainerPanel key={`${open.hostId}/${open.reference}`} hostId={open.hostId} reference={open.reference} row={openRowData} hosts={hosts} usage={openRowData ? usage.get(usageKey(openRowData)) ?? null : null} role={role} onNavigate={onNavigate} onClose={() => onNavigate(routeOf('containers'))} refreshFleet={() => void refresh()} />}
  </div>
}
