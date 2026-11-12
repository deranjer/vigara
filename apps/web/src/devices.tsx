import { useEffect, useMemo, useState } from 'react'
import { Alert, Button, Group, Loader, SegmentedControl, Table, Text, Title } from '@mantine/core'
import { LineChart } from '@mantine/charts'
import { AddDevice } from './add-device'
import { DevicePage } from './device-page'
import { getJson, loadAllDevices, type Device } from './device-types'
import { agentHealth, compareRows, compareValues, osName, type CompareMetric } from './device-utils'
import { formatMetric } from './metric-utils'
import { deviceRows, relativeTime } from './overview-utils'
import { route, type Route } from './route'
import type { TrendResponse } from './trend-utils'
import { MetricBar, Panel, Sparkline } from './ui'

type View = 'table' | 'compare'
type CompareRange = '6h' | '24h' | '7d' | '30d'
const lineColors = ['blue.6', 'teal.6', 'orange.6', 'grape.6', 'red.6', 'cyan.6', 'lime.7', 'pink.6', 'indigo.6', 'yellow.7']

function storedView(): View {
  try { return window.localStorage.getItem('vigara-device-view') === 'compare' ? 'compare' : 'table' } catch { return 'table' }
}

/** One trends request per device (fine for a home fleet), shared by the sparklines and the Compare chart. */
function useTrends(devices: Device[] | null, range: CompareRange): Record<string, TrendResponse> {
  const [trends, setTrends] = useState<Record<string, TrendResponse>>({})
  const ids = devices?.slice(0, 24).map((d) => d.id).join(',') ?? ''
  useEffect(() => {
    if (!ids) return
    let active = true
    setTrends({})
    const load = () => {
      for (const id of ids.split(',')) void getJson<TrendResponse>(`/api/inventory/devices/${encodeURIComponent(id)}/trends?range=${range}`).then((value) => { if (active) setTrends((current) => ({ ...current, [id]: value })) }).catch(() => undefined)
    }
    load()
    const timer = window.setInterval(load, 120_000)
    return () => { active = false; window.clearInterval(timer) }
  }, [ids, range])
  return trends
}

export function Devices({ route: current, role = '', onNavigate }: { route: Route; role?: string; onNavigate: (route: Route) => void }) {
  const [devices, setDevices] = useState<Device[] | null>(null)
  const [error, setError] = useState('')
  const [refresh, setRefresh] = useState(0)
  const [addOpen, setAddOpen] = useState(false)
  const [reinstall, setReinstall] = useState<Device | null>(null)
  const selectedId = current.id
  const selected = devices?.find((d) => d.id === selectedId) ?? null

  useEffect(() => {
    let active = true
    void loadAllDevices().then((result) => { if (active) { setDevices(result); setError('') } }).catch((cause: unknown) => { if (active) setError(cause instanceof Error ? cause.message : 'Could not load devices.') })
    return () => { active = false }
  }, [refresh])
  useEffect(() => {
    // An open device refreshes every 15 seconds; the list every 30.
    const timer = window.setInterval(() => setRefresh((v) => v + 1), selectedId ? 15_000 : 30_000)
    return () => window.clearInterval(timer)
  }, [selectedId])

  const modals = <>
    <AddDevice opened={addOpen} onClose={() => setAddOpen(false)} />
    {reinstall && <AddDevice key={reinstall.id} device={reinstall} opened onClose={() => { setReinstall(null); setRefresh((v) => v + 1) }} />}
  </>

  if (selectedId) {
    if (!devices) return error ? <Alert color="red" title="Unable to load the device">{error}</Alert> : <div className="device-empty"><Loader size="sm" /></div>
    if (!selected) return <div className="device-empty"><Text fw={600}>Device not found</Text><Text c="dimmed" size="sm" mt={6}>It may have been deleted.</Text><Button mt="md" variant="light" onClick={() => onNavigate(route('devices'))}>All devices</Button></div>
    return <>
      {error && <Alert color="red" mb="md" withCloseButton onClose={() => setError('')}>{error}</Alert>}
      <DevicePage key={selected.id} device={selected} tab={current.sub} role={role} onNavigate={onNavigate}
        onRefresh={() => setRefresh((v) => v + 1)} onReinstall={() => setReinstall(selected)}
        onDeleted={() => { setDevices((list) => list?.filter((d) => d.id !== selected.id) ?? null); onNavigate(route('devices')) }} />
      {modals}
    </>
  }
  return <>
    <DeviceList devices={devices} error={error} onNavigate={onNavigate} onAdd={() => setAddOpen(true)} onRefresh={() => setRefresh((v) => v + 1)} />
    {modals}
  </>
}

function DeviceList({ devices, error, onNavigate, onAdd, onRefresh }: { devices: Device[] | null; error: string; onNavigate: (route: Route) => void; onAdd: () => void; onRefresh: () => void }) {
  const [view, setView] = useState<View>(storedView)
  const [metric, setMetric] = useState<CompareMetric>('cpu')
  const [range, setRange] = useState<CompareRange>('24h')
  const trends = useTrends(devices, view === 'compare' ? range : '24h')
  const rows = useMemo(() => deviceRows(devices ?? []), [devices])
  const online = devices?.filter((d) => d.status === 'healthy').length ?? 0
  function changeView(value: string) {
    setView(value as View)
    try { window.localStorage.setItem('vigara-device-view', value) } catch { /* The choice lasts for this visit when storage is unavailable. */ }
  }
  const meta = !devices ? 'Loading…' : !devices.length ? 'None enrolled yet' : `${devices.length} enrolled · ${online === devices.length ? (devices.length === 2 ? 'both' : 'all') + ' online' : `${online} online`}`

  return <div className="vg-page">
    <Group justify="space-between" align="flex-end" mb="lg">
      <div><Title order={1}>Devices</Title><Text size="sm" c="dimmed" mt={4}>{meta}</Text></div>
      <Group gap="sm">
        <SegmentedControl size="xs" value={view} onChange={changeView} data={[{ value: 'table', label: 'Table' }, { value: 'compare', label: 'Compare' }]} aria-label="Device view" />
        <Button variant="default" size="xs" onClick={onRefresh}>Refresh</Button>
        <Button size="xs" onClick={onAdd}>Add device</Button>
      </Group>
    </Group>
    {error && <Alert color="red" mb="md" title="Unable to load devices">{error}</Alert>}
    {!devices ? !error && <div className="device-empty"><Loader size="sm" /><Text c="dimmed" mt="sm">Loading devices…</Text></div>
      : !devices.length ? <div className="device-empty"><Text fw={600}>No devices yet</Text><Text c="dimmed" size="sm" mt={6}>Install the agent on a computer to see it here.</Text><Button mt="md" onClick={onAdd}>Add device</Button></div>
      : view === 'compare' ? <Compare devices={devices} trends={trends} metric={metric} range={range} onMetric={setMetric} onRange={setRange} />
      : <Panel flush><div className="table-scroll"><Table className="vg-table vg-device-table" highlightOnHover verticalSpacing={9}>
        <Table.Thead><Table.Tr><Table.Th>Device</Table.Th><Table.Th>CPU · 24 h</Table.Th><Table.Th>Memory</Table.Th><Table.Th>Disk (fullest)</Table.Th><Table.Th ta="right">Containers</Table.Th><Table.Th>Agent</Table.Th><Table.Th ta="right">Check-in</Table.Th></Table.Tr></Table.Thead>
        <Table.Tbody>{rows.map((row) => {
          const device = devices.find((d) => d.id === row.id)!
          const health = agentHealth(device)
          const os = osName(device.latest?.host, device.platform)
          const open = () => onNavigate(route('devices', row.id))
          return <Table.Tr key={row.id} className="device-row" tabIndex={0} onClick={open} onKeyDown={(e) => { if (e.target === e.currentTarget && (e.key === 'Enter' || e.key === ' ')) { e.preventDefault(); open() } }} aria-label={`Open device ${row.name}`}>
            <Table.Td><span className="vg-cell-name"><span className="vg-status-dot" data-tone={row.tone} title={row.status} /><span><b>{row.name}</b><small>{[row.detail, os].filter(Boolean).join(' · ') || row.status}</small></span></span></Table.Td>
            <Table.Td><span className="vg-metric"><Sparkline width={80} values={trends[row.id] ? compareValues(trends[row.id], 'cpu') : []} label={`${row.name} CPU over 24 hours`} /><span className="vg-num">{row.cpu === null ? '—' : `${row.cpu.toFixed(1)}%`}</span></span></Table.Td>
            <Table.Td><MetricBar width={64} value={row.memory} label={`${row.name} memory used`} /></Table.Td>
            <Table.Td><span className="vg-metric"><MetricBar width={64} warnAt={85} value={row.disk} label={`${row.name} fullest disk used`} />{row.diskMount && <small className="vg-dim vg-mount">{row.diskMount}</small>}</span></Table.Td>
            <Table.Td ta="right" className="vg-num">{row.containers ?? '—'}</Table.Td>
            <Table.Td><span className="vg-agent" title={`${health.label} · ${health.detail}`}><span className="vg-status-dot" data-tone={health.tone} aria-hidden="true" />{device.agentId ? (device.latest?.host?.agentVersion || device.agentVersion || 'unknown') : 'none'}</span></Table.Td>
            <Table.Td ta="right"><Text size="sm" c="dimmed" title={row.lastSeenAt ? new Date(row.lastSeenAt).toLocaleString() : undefined}>{relativeTime(row.lastSeenAt)}</Text></Table.Td>
          </Table.Tr>
        })}</Table.Tbody>
      </Table></div></Panel>}
  </div>
}

const metricOptions: { value: CompareMetric; label: string }[] = [{ value: 'cpu', label: 'CPU' }, { value: 'memory', label: 'Memory' }, { value: 'disk', label: 'Disk' }, { value: 'network', label: 'Network' }]

function Compare({ devices, trends, metric, range, onMetric, onRange }: { devices: Device[]; trends: Record<string, TrendResponse>; metric: CompareMetric; range: CompareRange; onMetric: (m: CompareMetric) => void; onRange: (r: CompareRange) => void }) {
  const loaded = devices.filter((d) => trends[d.id])
  const lines = loaded.filter((d) => compareValues(trends[d.id], metric).some((v) => v !== null))
  const data = useMemo(() => compareRows(lines.map((d) => ({ id: d.id, trend: trends[d.id] })), metric), [lines, trends, metric])
  const percent = metric !== 'network'
  const format = (v: number) => percent ? `${v.toFixed(1)}%` : formatMetric(v, 'By/s', 'MB/s')
  const missing = devices.filter((d) => trends[d.id] && !lines.includes(d))
  return <Panel title={metric === 'disk' ? 'Fullest filesystem, % used' : metric === 'network' ? 'Network, receive + send' : `${metricOptions.find((m) => m.value === metric)!.label} use`} actions={<>
    <SegmentedControl size="xs" value={metric} onChange={(v) => onMetric(v as CompareMetric)} data={metricOptions} aria-label="Metric" />
    <SegmentedControl size="xs" value={range} onChange={(v) => onRange(v as CompareRange)} data={['6h', '24h', '7d', '30d']} aria-label="Range" />
  </>}>
    {loaded.length < Math.min(devices.length, 24) && !lines.length ? <Group h={300} justify="center"><Loader size="sm" /></Group>
      : !lines.length ? <Text c="dimmed" size="sm">No device reported this metric in the range.</Text>
      : <LineChart h={320} data={data} dataKey="timestamp" connectNulls={false} withDots={false} strokeWidth={1.5} curveType="linear" withLegend legendProps={{ verticalAlign: 'bottom', height: 40 }}
        series={lines.map((d, i) => ({ name: d.id, label: d.name, color: lineColors[i % lineColors.length] }))}
        valueFormatter={format} yAxisProps={{ domain: percent ? [0, 100] : [0, 'auto'], width: percent ? 40 : 70, tickFormatter: (v: number) => percent ? `${v}%` : formatMetric(v, 'By/s', 'MB/s') }}
        xAxisProps={{ type: 'number', domain: ['dataMin', 'dataMax'], tickFormatter: (v: number) => range === '7d' || range === '30d' ? new Date(v).toLocaleDateString([], { month: 'short', day: 'numeric' }) : new Date(v).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) }}
        tooltipProps={{ labelFormatter: (v) => new Date(Number(v)).toLocaleString() }} />}
    {missing.length > 0 && <Text size="xs" c="dimmed" mt="xs">No data in this range: {missing.map((d) => d.name).join(', ')}</Text>}
    {devices.length > 24 && <Text size="xs" c="dimmed" mt="xs">Showing the first 24 devices.</Text>}
  </Panel>
}
