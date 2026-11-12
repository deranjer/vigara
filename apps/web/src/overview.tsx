import { useEffect, useMemo, useState } from 'react'
import { Alert, Button, Group, Loader, SegmentedControl, Skeleton, Table, Text, Title } from '@mantine/core'
import { formatMetric } from './metric-utils'
import { activityItems, attentionItems, deviceRows, greeting, realDate, relativeTime, topContainers, type OverviewAudit, type OverviewDevice, type OverviewHost, type OverviewIncident, type OverviewStack, type OverviewUpdate } from './overview-utils'
import { route, type Route } from './route'
import { StatusText } from './status'
import type { TrendResponse } from './trend-utils'
import { AttentionRow, MetricBar, Panel, Sparkline, Stat } from './ui'

type Data = {
  devices: OverviewDevice[] | null; incidents: OverviewIncident[] | null; updates: OverviewUpdate[] | null
  stacks: OverviewStack[] | null; hosts: OverviewHost[] | null; policies: number | null; audit: OverviewAudit[] | null
}
const empty: Data = { devices: null, incidents: null, updates: null, stacks: null, hosts: null, policies: null, audit: null }
type Range = '1h' | '24h' | '7d'

async function read<T>(path: string): Promise<T> {
  const response = await fetch(path, { credentials: 'same-origin' })
  if (!response.ok) throw new Error(String(response.status))
  return await response.json() as T
}

/** Loads every source on its own, so one slow or forbidden API leaves only its part blank. */
function useOverviewData(refresh: number): { data: Data; failed: string[] } {
  const [data, setData] = useState<Data>(empty)
  const [failed, setFailed] = useState<string[]>([])
  useEffect(() => {
    let active = true
    const set = <K extends keyof Data>(key: K, value: Data[K]) => { if (active) setData((current) => ({ ...current, [key]: value })) }
    const fail = (name: string) => () => { if (active) setFailed((current) => current.includes(name) ? current : [...current, name]) }
    const load = () => {
      void read<{ devices: OverviewDevice[] }>('/api/inventory/devices?limit=100&offset=0').then((v) => set('devices', v.devices)).catch(fail('devices'))
      void read<OverviewIncident[]>('/api/alerts/incidents').then((v) => set('incidents', v)).catch(fail('alerts'))
      void read<{ items: OverviewUpdate[] }>('/api/image-updates').then((v) => set('updates', v.items)).catch(fail('image updates'))
      void read<{ stacks: OverviewStack[] }>('/api/stacks').then((v) => set('stacks', v.stacks)).catch(fail('stacks'))
      void read<{ hosts: OverviewHost[] }>('/api/containers').then((v) => set('hosts', v.hosts)).catch(fail('containers'))
      void read<unknown[]>('/api/backups/policies?limit=1&offset=0').then((v) => set('policies', v.length)).catch(fail('backups'))
      // The audit log needs audit permission; without it the feed shows alerts and deployments only.
      void read<OverviewAudit[]>('/api/audit?limit=50').then((v) => set('audit', v)).catch(() => set('audit', []))
    }
    load()
    const timer = window.setInterval(load, 30_000)
    return () => { active = false; window.clearInterval(timer) }
  }, [refresh])
  return { data, failed }
}

/** CPU trend per device for the sparkline column; one request per device (fine for a home fleet). */
function useCpuTrends(devices: OverviewDevice[] | null, range: Range): Record<string, (number | null)[]> {
  const [trends, setTrends] = useState<Record<string, (number | null)[]>>({})
  const ids = devices?.slice(0, 24).map((d) => d.id).join(',') ?? ''
  useEffect(() => {
    if (!ids) return
    let active = true
    setTrends({})
    for (const id of ids.split(',')) {
      void read<TrendResponse>(`/api/inventory/devices/${encodeURIComponent(id)}/trends?range=${range}`)
        .then((v) => { const cpu = v.series.find((s) => s.name === 'cpu_used_percent'); if (active && cpu) setTrends((current) => ({ ...current, [id]: cpu.values })) })
        .catch(() => undefined)
    }
    return () => { active = false }
  }, [ids, range])
  return trends
}

export function Overview({ userName, onNavigate }: { userName: string; onNavigate: (route: Route) => void }) {
  const [refresh, setRefresh] = useState(0)
  const [range, setRange] = useState<Range>('24h')
  const [feed, setFeed] = useState<'all' | 'deploys' | 'alerts'>('all')
  const { data, failed } = useOverviewData(refresh)
  const trends = useCpuTrends(data.devices, range)
  const rows = useMemo(() => deviceRows(data.devices ?? []), [data.devices])
  const top = useMemo(() => topContainers(data.devices ?? []), [data.devices])
  const attention = useMemo(() => attentionItems(data), [data])
  const activity = useMemo(() => activityItems(data, 40).filter((item) => feed === 'all' || (feed === 'deploys' ? item.key.startsWith('deploy:') || item.text.startsWith('Stack') : item.key.startsWith('incident:') || item.key.startsWith('resolved:'))).slice(0, 15), [data, feed])

  const online = data.devices?.filter((d) => d.status === 'healthy').length
  const containers = data.hosts?.flatMap((h) => h.containers)
  const running = containers?.filter((c) => c.state === 'running').length
  const unhealthy = containers?.filter((c) => c.status?.includes('(unhealthy)')).length ?? 0
  const restarting = containers?.filter((c) => c.state === 'restarting').length ?? 0
  const drafts = data.stacks?.filter((s) => s.draft).length ?? 0
  const deployed = data.stacks?.filter((s) => realDate(s.deploymentAt) && s.deploymentStatus !== 'failed').length
  const available = data.updates?.filter((u) => !u.ignored && u.result.status === 'available') ?? []
  const worst = attention[0]?.tone
  const headline = !data.devices ? 'Checking your devices…' : worst === 'crit' ? 'Something needs you now' : worst === 'warn' ? 'A few things need a look' : 'All systems reporting'

  return <div className="vg-page vg-overview">
    <Group justify="space-between" align="flex-end" mb="lg">
      <div>
        <Title order={1}>{greeting(new Date().getHours())}, {userName}</Title>
        <Group gap={8} mt={6} className="vg-meta">
          <StatusText tone={!data.devices ? 'idle' : worst === 'crit' ? 'crit' : worst === 'warn' ? 'warn' : 'ok'}>{headline}</StatusText>
          {data.devices && <Text size="sm" c="dimmed">· {data.devices.length} devices{containers ? ` · ${containers.length} containers` : ''}{data.stacks ? ` · ${data.stacks.length} stacks` : ''}</Text>}
        </Group>
      </div>
      <Button variant="default" onClick={() => setRefresh((v) => v + 1)}>Refresh</Button>
    </Group>
    {failed.length > 0 && <Alert color="yellow" mb="md">Some parts could not be loaded: {failed.join(', ')}. They will retry in 30 seconds.</Alert>}

    <div className="vg-panel vg-strip">
      <Stat label="Devices" value={online ?? '…'} unit={data.devices ? `/ ${data.devices.length} online` : undefined} tone={data.devices && online !== data.devices.length ? 'warn' : 'ok'} detail={data.devices ? (online === data.devices.length ? 'all checking in' : `${data.devices.length - (online ?? 0)} not reporting`) : undefined} />
      <Stat label="Containers" value={running ?? '…'} unit={containers ? 'running' : undefined} tone={unhealthy + restarting ? 'warn' : 'ok'} detail={containers ? `${unhealthy} unhealthy · ${restarting} restarting` : undefined} />
      <Stat label="Stacks" value={deployed ?? '…'} unit={data.stacks ? 'deployed' : undefined} tone={drafts ? 'warn' : 'ok'} detail={data.stacks ? (drafts ? `${drafts} unsaved draft${drafts === 1 ? '' : 's'}` : 'no drafts') : undefined} />
      <Stat label="Image updates" value={data.updates ? available.length : '…'} unit={data.updates ? 'available' : undefined} detail={available.length ? [...new Set(available.map((u) => u.result.name))].slice(0, 3).join(', ') : data.updates ? 'everything current' : undefined} />
      <Stat label="Backups" value={data.policies === null ? '…' : data.policies ? 'On' : '—'} tone={data.policies ? 'ok' : 'idle'} detail={data.policies === null ? undefined : data.policies ? 'policies configured' : 'not set up yet'} />
    </div>

    <div className="vg-split">
      <div className="vg-col">
        <Panel title="Needs attention" count={attention.length} actions={<Button size="xs" variant="subtle" onClick={() => onNavigate(route('alerts'))}>All alerts</Button>} flush>
          {!data.devices ? <div className="vg-panel-body"><Skeleton height={36} mb={8} /><Skeleton height={36} /></div>
            : attention.length === 0 ? <Text c="dimmed" size="sm" p="md">Nothing needs you right now.</Text>
            : attention.map((item) => <AttentionRow key={item.key} tone={item.tone} title={item.title} detail={item.detail} action={item.action} onAction={() => onNavigate(item.to)} />)}
        </Panel>

        <Panel title="Devices" actions={<SegmentedControl size="xs" value={range} onChange={(v) => setRange(v as Range)} data={['1h', '24h', '7d']} aria-label="CPU trend range" />} flush>
          {!data.devices ? <div className="vg-panel-body"><Loader size="sm" /></div> : <div className="table-scroll"><Table className="vg-table" highlightOnHover verticalSpacing={8}>
            <Table.Thead><Table.Tr><Table.Th>Device</Table.Th><Table.Th>CPU · {range}</Table.Th><Table.Th className="vg-wide">Memory</Table.Th><Table.Th className="vg-wide">Disk (fullest)</Table.Th><Table.Th ta="right" className="vg-wide">Containers</Table.Th><Table.Th ta="right">Check-in</Table.Th></Table.Tr></Table.Thead>
            <Table.Tbody>{rows.map((row) => <Table.Tr key={row.id} className="device-row" tabIndex={0} onClick={() => onNavigate(route('devices', row.id))} onKeyDown={(e) => { if (e.key === 'Enter') onNavigate(route('devices', row.id)) }} aria-label={`Open device ${row.name}`}>
              <Table.Td><span className="vg-cell-name"><span className="vg-status-dot" data-tone={row.tone} title={row.status} /><span><b>{row.name}</b><small>{row.detail || row.status}</small></span></span></Table.Td>
              <Table.Td><span className="vg-metric"><Sparkline width={72} values={trends[row.id] ?? []} label={`${row.name} CPU over ${range}`} /><span className="vg-num">{row.cpu === null ? '—' : `${row.cpu.toFixed(1)}%`}</span></span></Table.Td>
              <Table.Td className="vg-wide"><MetricBar width={56} value={row.memory} label={`${row.name} memory used`} /></Table.Td>
              <Table.Td className="vg-wide"><span title={row.diskMount ?? undefined}><MetricBar width={56} value={row.disk} label={`${row.name} fullest disk used`} /></span></Table.Td>
              <Table.Td ta="right" className="vg-num vg-wide">{row.containers ?? '—'}</Table.Td>
              <Table.Td ta="right"><Text size="sm" c="dimmed">{relativeTime(row.lastSeenAt)}</Text></Table.Td>
            </Table.Tr>)}</Table.Tbody>
          </Table></div>}
        </Panel>

        <Panel title="Top containers by memory" actions={<><Text size="xs" c="dimmed">all devices</Text><Button size="xs" variant="subtle" onClick={() => onNavigate(route('containers'))}>All containers</Button></>} flush>
          {top.length === 0 ? <Text c="dimmed" size="sm" p="md">{data.devices ? 'No container memory reported yet.' : 'Loading…'}</Text> : <Table className="vg-table" verticalSpacing={8}><Table.Tbody>{top.map((c) => {
            const share = top[0].memoryBytes ? c.memoryBytes / top[0].memoryBytes * 100 : 0
            const hostId = data.devices?.find((d) => d.id === c.deviceId)?.agentId
            return <Table.Tr key={`${c.deviceId}/${c.name}`} className={hostId ? 'device-row' : undefined} onClick={hostId ? () => onNavigate(route('containers', hostId, c.name)) : undefined}>
              <Table.Td><span className="vg-cell-name"><span><b>{c.name}</b><small>{c.device}</small></span></span></Table.Td>
              <Table.Td w="45%"><span className="vg-bar" style={{ width: '100%' }}><i data-tone="info" style={{ width: `${share}%` }} /></span></Table.Td>
              <Table.Td ta="right" className="vg-num">{formatMetric(c.memoryBytes, 'By')}</Table.Td>
              <Table.Td ta="right"><Text size="sm" c="dimmed" className="vg-num">{c.cpu === null ? '—' : `${c.cpu.toFixed(1)}% CPU`}</Text></Table.Td>
            </Table.Tr>
          })}</Table.Tbody></Table>}
        </Panel>
      </div>

      <Panel title="Activity" actions={<SegmentedControl size="xs" value={feed} onChange={(v) => setFeed(v as typeof feed)} data={[{ value: 'all', label: 'All' }, { value: 'deploys', label: 'Stacks' }, { value: 'alerts', label: 'Alerts' }]} />} flush>
        {activity.length === 0 ? <Text c="dimmed" size="sm" p="md">{data.audit === null ? 'Loading…' : 'Nothing recorded yet.'}</Text> : <ol className="vg-feed">{activity.map((item) => <li key={item.key}>
          <span className="vg-status-dot" data-tone={item.tone} aria-hidden="true" />
          {item.to ? <a href="#" onClick={(e) => { e.preventDefault(); onNavigate(item.to!) }}>{item.text}</a> : <span>{item.text}</span>}
          <time dateTime={item.at} title={new Date(item.at).toLocaleString()}>{relativeTime(item.at)}</time>
        </li>)}</ol>}
      </Panel>
    </div>
  </div>
}
