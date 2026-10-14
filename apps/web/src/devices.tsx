import { useEffect, useMemo, useRef, useState } from 'react'
import { Alert, LoadingOverlay, Badge, Button, Card, Checkbox, Group, Loader, SegmentedControl, Select, SimpleGrid, Stack, Tabs, Table, Text, TextInput, Title } from '@mantine/core'
import { LineChart } from '@mantine/charts'
import { ReferenceArea } from 'recharts'
import { DeviceInfo, DeviceSettings, MonitoringOverview } from './device-panels'
import { chartRows, formatMetric, type MetricSeries, type RateUnit } from './metric-utils'

type Metric = { name: string; value: number; unit: string; labels?: Record<string, string> }
type DockerContainer = { id: string; name?: string; image?: string; state: string }
type HardwareInventory = { observedAt: string; cpuStatus: string; cpu: { vendor?: string; model?: string; physicalPackages?: number; physicalCores?: number; logicalThreads?: number }; gpuStatus: string; gpus: { vendor?: string; model?: string; vramBytes?: number }[]; memoryStatus: string; memoryModules: { capacityBytes?: number; type?: string; speedMHz?: number }[]; boardStatus: string; motherboard: { manufacturer?: string; model?: string }; firmware: { manufacturer?: string; version?: string; releaseDate?: string }; diskStatus: string; disks: { model?: string; capacityBytes?: number; type?: string }[] }
type Snapshot = { observedAt?: string; host?: { os?: string; osVersion?: string; architecture?: string; agentVersion?: string; cpuModel?: string; logicalCpus?: number; totalMemoryBytes?: number; uptimeSeconds?: number; hardwareInventory?: HardwareInventory }; metrics?: Metric[]; dockerContainers?: DockerContainer[]; errors?: { capability: string; message: string }[] }
export type Device = { id: string; name: string; platformId?: string | null; status: 'healthy' | 'stale' | 'failed' | 'unsupported'; agentId?: string | null; platform: string; architecture: string; agentVersion: string; capabilities: string[]; lastSeenAt?: string | null; latest?: Snapshot; telemetryObservedAt?: string | null; collectionErrors?: Snapshot['errors'] }
type Point = { at: string; value: number | null; min?: number; max?: number; samples: number; resolutionSeconds?: number }
type Series = { name: string; unit: string; labels: Record<string, string>; points: Point[] }
type History = { range: string; windowStart: string; windowEnd: string; metrics: string[]; series: Series[]; resolutionSeconds: number; resolution: string }
type DevicePage = { devices: Device[]; total: number; offset: number; limit: number }
type Availability = { expectedAwake: boolean; maintenanceActive: boolean }
type BackupProgress = { observedAt: string; phase: string; filesDone?: number; filesTotal?: number; bytesDone?: number; bytesTotal?: number; percentDone?: number }
type BackupJob = { id: string; profileId: string; scheduledFor: string; status: string; executionStatus: string; outcome?: string; snapshotIds?: string[]; errorCode?: string; errorMessage?: string; progress?: BackupProgress }
type BackupPolicy = { deviceId: string; id: string; name: string; profileId: string }

async function get<T>(path: string): Promise<T> {
  const response = await fetch(path, { credentials: 'same-origin' })
  if (!response.ok) {
    const detail = (await response.text()).slice(0, 300).trim()
    throw new Error(response.status === 401 ? 'Your session expired. Log in again.' : detail || 'Could not load device information.')
  }
  return response.json() as Promise<T>
}

const statusColor = { healthy: 'green', stale: 'yellow', failed: 'red', unsupported: 'gray' } as const
const statusLabel = { healthy: 'Healthy', stale: 'Stale', failed: 'Failed', unsupported: 'Unsupported' } as const
const ranges = [{ label: '1 hour', value: '1h' }, { label: '6 hours', value: '6h' }, { label: '24 hours', value: '24h' }, { label: '7 days', value: '7d' }, { label: '30 days', value: '30d' }]

function timeLabel(value?: string | null) { return value ? new Date(value).toLocaleString() : 'Never' }
function relativeLabel(value?: string | null) {
  if (!value) return 'Never checked in'
  const minutes = Math.max(0, Math.floor((Date.now() - new Date(value).getTime()) / 60_000))
  if (minutes < 1) return 'Just now'
  if (minutes < 60) return `${minutes}m ago`
  const hours = Math.floor(minutes / 60)
  return hours < 48 ? `${hours}h ago` : `${Math.floor(hours / 24)}d ago`
}
const metricLabels: Record<string, string> = {
  docker_cpu_capacity_used_percent: 'Container CPU capacity use', docker_memory_usage_bytes: 'Container memory use', docker_memory_limit_bytes: 'Container memory limit', docker_memory_used_percent: 'Container memory use', docker_processes: 'Container processes', docker_network_receive_bytes_per_second: 'Container network receive rate', docker_network_transmit_bytes_per_second: 'Container network send rate',
  cpu_used_percent: 'CPU use', memory_used_percent: 'Memory use', memory_available_bytes: 'Available memory', memory_total_bytes: 'Total memory',
  filesystem_free_bytes: 'Free disk space', filesystem_total_bytes: 'Total disk space',
  network_receive_bytes_per_second: 'Network receive rate', network_send_bytes_per_second: 'Network send rate',
  disk_read_bytes_per_second: 'Disk read rate', disk_write_bytes_per_second: 'Disk write rate', uptime_seconds: 'Uptime',
}
function prettyName(name: string) { return metricLabels[name] ?? name.replaceAll('_', ' ').replace(/\b\w/g, (part) => part.toUpperCase()) }
function formatValue(value: number, unit: string, rateUnit: RateUnit = 'B/s') { return formatMetric(value, unit, rateUnit) }

export function summaryMetric(device: Device, name: string, rateUnit: RateUnit = 'B/s') {
  const metrics = device.latest?.metrics?.filter((metric) => metric.name === name) ?? []
  return metrics.length ? metrics.map((metric) => `${formatValue(metric.value, metric.unit, rateUnit)}${metric.labels && Object.keys(metric.labels).length ? ` (${Object.values(metric.labels).join(', ')})` : ''}`).join(' · ') : '—'
}

function labelValueForChart(key: string, value: string) { return key === 'container_id' ? value.slice(0, 12) : value }

function toLocalInput(value: Date) { const local = new Date(value.getTime() - value.getTimezoneOffset() * 60_000); return local.toISOString().slice(0, 23) }

function LineChartWrapper({ loading, series, history, normalized, onZoom, rateUnit }: { loading: boolean; series: Series[]; range: string; history: History; normalized: boolean; onZoom: (start: Date, end: Date) => void; rateUnit: RateUnit }) {
  const windowStart = history.windowStart; const windowEnd = history.windowEnd; const resolutionSeconds = history.resolutionSeconds
  const hasMixedUnits = new Set(series.map((item) => item.unit)).size > 1
  const useNormalized = normalized || hasMixedUnits
  const container = useMemo(() => chartRows(series as MetricSeries[], resolutionSeconds || 60, useNormalized, rateUnit), [series, resolutionSeconds, useNormalized, rateUnit])
  const [dragStart, setDragStart] = useState<number | null>(null)
  const [dragEnd, setDragEnd] = useState<number | null>(null)
  const tooltip = (props: { active?: boolean; payload?: Array<{ dataKey: string; value: number | null; payload: Record<string, number | null> }> }) => {
    if (!props.active || !props.payload?.length) return null
    return <Card withBorder shadow="sm" p="xs">{props.payload.map((item) => { const keyIndex = Number(item.dataKey.replace('series_', '')); const original = item.payload[`actual_${keyIndex}`]; const itemSeries = series[keyIndex]; return original == null || !itemSeries ? null : <Text key={item.dataKey} size="xs">{prettyName(itemSeries.name)}{Object.keys(itemSeries.labels).length ? ` · ${Object.entries(itemSeries.labels).map(([key, value]) => `${key}=${value}`).join(', ')}` : ''}: {formatMetric(original, itemSeries.unit, rateUnit)} · {new Date(Number(item.payload.timestamp)).toLocaleString()}</Text> })}</Card>
  }
  if (!series.length) return <Stack h={300} pos="relative" align="center" justify="center"><LoadingOverlay visible={loading} /><Text c="dimmed">No samples selected or available for this interval.</Text><Text size="xs" c="dimmed">Select metrics above. Older history contains complete hourly summaries only.</Text></Stack>
  return <Stack gap="xs" pos="relative">
<LoadingOverlay visible={loading} overlayProps={{ blur: 1 }} />
<LineChart h={300} data={container.data} dataKey="timestamp" series={container.chartSeries.map((value) => ({ name: value.name, label: prettyName(series[Number(value.name.replace('series_', ''))]!.name) + (Object.keys(series[Number(value.name.replace('series_', ''))]!.labels).length ? ` · ${Object.entries(series[Number(value.name.replace('series_', ''))]!.labels).map(([key, label]) => labelValueForChart(key, label)).join(', ')}` : ''), color: value.color }))} withLegend withDots dotProps={{ r: 2 }} activeDotProps={{ r: 5 }} connectNulls={false} yAxisLabel={useNormalized ? 'Normalized scale (%)' : series[0]?.unit === 'By/s' ? rateUnit : series[0]?.unit} xAxisProps={{ type: 'number', domain: [new Date(windowStart).getTime(), new Date(windowEnd).getTime()], tickFormatter: (value: number) => new Date(windowEnd).getTime() - new Date(windowStart).getTime() > 86400000 ? new Date(value).toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit' }) : new Date(value).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) }} yAxisProps={{ domain: useNormalized ? [0, 100] : ['auto', 'auto'], tickFormatter: (value: number) => useNormalized ? `${value}%` : String(value) }} tooltipProps={{ content: tooltip as never }} lineChartProps={{ onMouseDown: (state) => { const at = Number(state?.activeLabel); if (Number.isFinite(at)) { setDragStart(at); setDragEnd(at) } }, onMouseMove: (state) => { const at = Number(state?.activeLabel); if (dragStart !== null && Number.isFinite(at)) setDragEnd(at) }, onMouseUp: (state) => { const at = Number(state?.activeLabel ?? dragEnd); if (dragStart !== null && Number.isFinite(at) && Math.abs(at - dragStart) > 5000) onZoom(new Date(Math.min(at, dragStart)), new Date(Math.max(at, dragStart))); setDragStart(null); setDragEnd(null) }, onMouseLeave: () => { setDragStart(null); setDragEnd(null) } }}>
<ReferenceArea yAxisId="left" x1={dragStart ?? undefined} x2={dragEnd ?? undefined} fill="var(--mantine-color-blue-5)" fillOpacity={0.18} stroke="var(--mantine-color-blue-6)" strokeOpacity={0.7} />
</LineChart>
    <Group justify="space-between">
<Text size="xs" c="dimmed">{timeLabel(windowStart)}</Text>
<Text size="xs" c="dimmed">{timeLabel(windowEnd)}</Text>
</Group>
<Text size="xs" c="dimmed">Drag across the plot to zoom. {useNormalized ? 'Each line is normalized to its own 0–100% range; tooltip values retain original units.' : 'Lines share the same unit.'}</Text>
</Stack>
}

function BackupHistory({ refresh, deviceId }: { refresh: number; deviceId?: string }) {
  const [rows, setRows] = useState<{ policy: BackupPolicy; jobs: BackupJob[] }[]>([])
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  useEffect(() => {
    let active = true
    const load = async () => {
      try {
        const policies: BackupPolicy[] = []
        for (let offset = 0; ; offset += 100) {
          const response = await fetch(`/api/backups/policies?limit=100&offset=${offset}`, { credentials: 'same-origin' })
          if (!response.ok) throw new Error('Could not load backup policies.')
          policies.push(...await response.json() as BackupPolicy[])
          if (response.headers.get('X-Has-More') !== 'true') break
        }
        const result = await Promise.all(policies.filter((policy) => !deviceId || policy.deviceId === deviceId).map(async (policy) => ({ policy, jobs: await get<BackupJob[]>(`/api/backups/policies/${encodeURIComponent(policy.id)}/history?limit=20`) })))
        if (active) { setRows(result); setError('') }
      } catch (cause) { if (active) setError(cause instanceof Error ? cause.message : 'Could not load backup history.') }
      finally { if (active) setLoading(false) }
    }
    void load()
    const timer = window.setInterval(() => { void load() }, 15_000)
    return () => { active = false; window.clearInterval(timer) }
  }, [refresh, deviceId])
  return <Card withBorder radius="md" padding="lg" mb="lg">
    <Group justify="space-between" mb="md">
<div>
<Title order={3}>Backup history</Title>
<Text size="sm" c="dimmed">Execution state and recent snapshots for configured backup policies</Text>
</div>
<Badge variant="light">Refreshes every 15 seconds</Badge>
</Group>
    {error ? <Alert color="red" title="Backup history could not be loaded">{error}</Alert> : loading ? <Group>
<Loader size="xs" />
<Text size="sm" c="dimmed">Loading backup history…</Text>
</Group> : rows.length === 0 ? <Text c="dimmed">{deviceId ? 'No backup policies are configured for this device.' : 'No backup policies are configured.'}</Text> : <Stack gap="md">{rows.map(({ policy, jobs }) => <div key={policy.id}>
<Group justify="space-between" mb="xs">
<Text fw={600}>{policy.name}</Text>
<Text size="xs" c="dimmed">Profile {policy.profileId}</Text>
</Group>{jobs.length === 0 ? <Text size="sm" c="dimmed">No scheduled runs yet.</Text> : <Table.ScrollContainer minWidth={650}>
<Table striped>
<Table.Thead>
<Table.Tr>
<Table.Th>Scheduled</Table.Th>
<Table.Th>Execution</Table.Th>
<Table.Th>Outcome</Table.Th>
<Table.Th>Progress</Table.Th>
<Table.Th>Snapshot</Table.Th>
<Table.Th>Details</Table.Th>
</Table.Tr>
</Table.Thead>
<Table.Tbody>{jobs.map((job) => <Table.Tr key={job.id}>
<Table.Td>{timeLabel(job.scheduledFor)}</Table.Td>
<Table.Td>
<Badge color={['running', 'succeeded'].includes(job.executionStatus) ? 'green' : ['stale', 'unknown'].includes(job.executionStatus) ? 'yellow' : ['failed', 'partial', 'interrupted', 'canceled', 'timed_out'].includes(job.executionStatus) ? 'red' : 'gray'} variant="light">{job.executionStatus}</Badge>
</Table.Td>
<Table.Td>{job.outcome || job.status}</Table.Td>
<Table.Td>{job.progress ? <Text size="sm">{job.progress.phase}{job.progress.percentDone ? ` · ${job.progress.percentDone.toFixed(1)}%` : ''}{job.progress.filesTotal ? ` · ${job.progress.filesDone ?? 0}/${job.progress.filesTotal} files` : ''}{job.progress.bytesTotal ? ` · ${formatValue(job.progress.bytesDone ?? 0, 'By')}/${formatValue(job.progress.bytesTotal, 'By')}` : ''}</Text> : '—'}</Table.Td>
<Table.Td>{job.snapshotIds?.length ? job.snapshotIds.map((id) => <Text key={id} size="xs" ff="monospace">{id}</Text>) : '—'}</Table.Td>
<Table.Td>{job.errorMessage ? <Text size="xs" c="red">{job.errorMessage}{job.errorCode ? ` (${job.errorCode})` : ''}</Text> : job.errorCode || '—'}</Table.Td>
</Table.Tr>)}</Table.Tbody>
</Table>
</Table.ScrollContainer>}</div>)}</Stack>}
  </Card>
}

export function Devices({ section, selectedId, onSelect }: { section: 'devices' | 'monitoring' | 'backups'; selectedId: string | null; onSelect: (id: string | null) => void }) {
  const [allDevices, setAllDevices] = useState<Device[]>([]);
  const [detailTab, setDetailTab] = useState<string | null>('info'); const [history, setHistory] = useState<History | null>(null); const [range, setRange] = useState('24h'); const [error, setError] = useState(''); const [historyError, setHistoryError] = useState(''); const [historyLoading, setHistoryLoading] = useState(false); const [loading, setLoading] = useState(true); const [offset, setOffset] = useState(0); const [total, setTotal] = useState(0); const [refresh, setRefresh] = useState(0); const [metricNames, setMetricNames] = useState<string[]>([]); const [normalized, setNormalized] = useState(false); const [rateUnit, setRateUnit] = useState<RateUnit>('MB/s')
  const [customStart, setCustomStart] = useState(''); const [customEnd, setCustomEnd] = useState(''); const [appliedInterval, setAppliedInterval] = useState<{ start: string; end: string } | null>(null); const [customError, setCustomError] = useState('')
  const initialMetric = useRef(true)
  const [zoomBase, setZoomBase] = useState<{ range: string; interval: { start: string; end: string } | null } | null>(null)
  const [availability, setAvailability] = useState<Availability | null>(null)
  const devices = allDevices.slice(offset, offset + 100)
  const selected = allDevices.find((device) => device.id === selectedId) ?? null
  const latestMetrics = useMemo(() => selected?.latest?.metrics ?? [], [selected])
  const labelValue = (key: string, value: string) => key === 'container_id' ? selected?.latest?.dockerContainers?.find((container) => container.id === value)?.name || value.slice(0, 12) : value

  useEffect(() => {
    let active = true
    if (!allDevices.length) setLoading(true)
    const load = async () => {
      const result: Device[] = []
      for (let pageOffset = 0; ; pageOffset += 100) {
        const page = await get<DevicePage>(`/api/inventory/devices?limit=100&offset=${pageOffset}`)
        result.push(...page.devices)
        if (!page.devices.length || result.length >= page.total) break
      }
      if (active) { setAllDevices(result); setTotal(result.length); setError('') }
    }
    void load().catch((cause: unknown) => { if (active) setError(cause instanceof Error ? cause.message : 'Could not load devices.') }).finally(() => { if (active) setLoading(false) })
    return () => { active = false }
    // Refresh deliberately preserves selection and tab drafts.
  }, [refresh])
  useEffect(() => {
    if (section !== 'monitoring' || selectedId) return
    const timer = window.setInterval(() => setRefresh((value) => value + 1), 30_000)
    return () => window.clearInterval(timer)
  }, [section, selectedId])
  useEffect(() => { initialMetric.current = true; setZoomBase(null); setHistory(null); setHistoryError(''); setMetricNames([]); setCustomStart(''); setCustomEnd(''); setAppliedInterval(null); setRange('24h') }, [selectedId])
  useEffect(() => { setAvailability(null); if (!selectedId) return; let active = true; void get<Availability>(`/api/devices/${encodeURIComponent(selectedId)}/availability`).then((value) => { if (active) setAvailability(value) }).catch(() => { /* Availability rules are optional until configured. */ }); return () => { active = false } }, [selectedId, refresh])
  useEffect(() => { if (!selectedId || detailTab !== 'monitoring') return; let active = true; setHistoryLoading(true); setHistoryError(''); const query = new URLSearchParams()
    if (range === 'custom' && appliedInterval) { query.set('start', appliedInterval.start); query.set('end', appliedInterval.end) } else query.set('range', range === 'custom' ? '24h' : range)
    metricNames.slice(0, 5).forEach((name) => query.append('metrics', name))
    void get<History>(`/api/inventory/devices/${encodeURIComponent(selectedId)}/history?${query}`).then((data) => { if (active) { setHistory(data); if (initialMetric.current && data.metrics.length) { initialMetric.current = false; setMetricNames([data.metrics[0]!]) } } }).catch((cause: unknown) => { if (active) setHistoryError(cause instanceof Error ? cause.message : 'Could not load metric history.') }).finally(() => { if (active) setHistoryLoading(false) }); return () => { active = false } }, [selectedId, detailTab, range, metricNames, appliedInterval, refresh])

  function backToList() { onSelect(null); setHistory(null); setError('') }
  function openDevice(id: string, tab = 'info') { setDetailTab(tab); onSelect(id); setError('') }
  return <section className="devices-page">
    <Group justify="space-between" align="center" mb="xl">
<div>
<Text c="dimmed" size="sm" fw={600} tt="uppercase">Home infrastructure</Text>
<Title order={1} mt={4}>{selected ? selected.name : section === 'backups' ? 'Backup history' : section === 'monitoring' ? 'Monitoring' : 'Devices'}</Title>
<Text c="dimmed" mt={6}>{selected ? 'Device information, monitoring, backups and settings.' : section === 'backups' ? 'Execution state and recent snapshots for configured backup policies.' : section === 'monitoring' ? 'Compare current measurements across devices. Open a device to explore its history.' : 'Your enrolled and discovered devices. Open a device for information, monitoring, backups and settings.'}</Text>
</div>
<Group>
<Button variant="default" onClick={() => setRefresh((value) => value + 1)}>Refresh</Button>
{selected && <Button variant="subtle" onClick={backToList}>← All devices</Button>}
</Group>
</Group>
    {error && <Alert color="red" mb="md" title="Unable to load information" onClose={() => setError('')} withCloseButton>{error}</Alert>}
    {!selected && section === 'monitoring' && !loading && (!error || allDevices.length > 0) && <MonitoringOverview devices={allDevices} refresh={refresh} onOpen={(id) => openDevice(id, 'monitoring')} />}
    {!selected && section === 'backups' ? <BackupHistory refresh={refresh} /> : loading ? <div className="device-empty">
<Loader size="sm" />
<Text c="dimmed" mt="sm">Loading devices…</Text>
</div> : selected ? <Stack gap="lg">
      <Card withBorder radius="md" padding="lg">
<Group justify="space-between" align="flex-start">
<div>
<Group>
<Title order={3}>{selected.name}</Title>
<Badge color={statusColor[selected.status]} variant="light">{statusLabel[selected.status]}</Badge>{availability?.maintenanceActive && <Badge color="blue" variant="light">Maintenance active</Badge>}{availability && !availability.expectedAwake && <Badge color="gray" variant="light">Expected offline</Badge>}</Group>
<Text c="dimmed" mt="xs">{[selected.platform, selected.architecture].filter(Boolean).join(' · ') || 'Platform not reported'}{selected.agentVersion ? ` · Agent ${selected.agentVersion}` : ''}</Text>
</div>
<div className="checkin">
<Text size="xs" c="dimmed">Last check-in</Text>
<Text fw={600}>{relativeLabel(selected.lastSeenAt)}</Text>
<Text size="xs" c="dimmed">{timeLabel(selected.lastSeenAt)}</Text>
</div>
</Group>

      </Card>
      <Tabs value={detailTab} onChange={setDetailTab} keepMounted={false}>
        <Tabs.List mb="lg">
          <Tabs.Tab value="info">Device info</Tabs.Tab>
          <Tabs.Tab value="monitoring">Monitoring</Tabs.Tab>
          <Tabs.Tab value="backups">Backups</Tabs.Tab>
          <Tabs.Tab value="settings">Device settings</Tabs.Tab>
          {!!selected.latest?.dockerContainers?.length && <Tabs.Tab value="containers">Containers</Tabs.Tab>}
        </Tabs.List>
        <Tabs.Panel value="info"><DeviceInfo device={selected} /></Tabs.Panel>
        <Tabs.Panel value="backups"><BackupHistory key={selected.id} refresh={refresh} deviceId={selected.id} /></Tabs.Panel>
        <Tabs.Panel value="settings"><DeviceSettings key={selected.id} device={selected} onSaved={() => setRefresh((value) => value + 1)} /></Tabs.Panel>
        <Tabs.Panel value="monitoring"><Stack gap="lg">
      {selected.collectionErrors && selected.collectionErrors.length > 0 && <Alert color="red" title="Collection errors">{selected.collectionErrors.map((item, index) => <Text key={`${item.capability}-${index}`} size="sm">{item.capability}: {item.message}</Text>)}</Alert>}
      <Card withBorder radius="md" padding="lg">
<Group justify="space-between" mb="md">
<div>
<Title order={3}>Latest measurements</Title>
<Select label="Network rate units" aria-label="Latest network rate units" value={rateUnit} onChange={(value) => { if (value) setRateUnit(value as RateUnit) }} data={['B/s','kB/s','MB/s','GB/s','kb/s','Mb/s','Gb/s']} w={140} size="xs" />
<Text size="sm" c="dimmed">Observed {timeLabel(selected.telemetryObservedAt)}</Text>
</div>
</Group>{latestMetrics.length === 0 ? <Text c="dimmed">No measurements were reported in the latest snapshot. Missing values are not treated as zero.</Text> : <SimpleGrid cols={{ base: 1, sm: 2, lg: 3 }}>{latestMetrics.map((metric, index) => <div className="metric-value" key={`${metric.name}-${JSON.stringify(metric.labels ?? {})}-${index}`}>
<Text size="sm" c="dimmed">{prettyName(metric.name)}{metric.labels && Object.keys(metric.labels).length ? ` · ${Object.entries(metric.labels).map(([key, value]) => labelValue(key, value)).join(', ')}` : ''}</Text>
<Text fw={700} size="xl">{formatValue(metric.value, metric.unit, rateUnit)}</Text>
</div>)}</SimpleGrid>}{selected.latest?.host?.logicalCpus && latestMetrics.find((metric) => metric.name === 'cpu_used_percent') && <div className="cpu-estimate"><Text size="sm" c="dimmed">CPU in use (estimated)</Text><Text fw={700} size="lg">{((latestMetrics.find((metric) => metric.name === 'cpu_used_percent')!.value * selected.latest.host.logicalCpus) / 100).toFixed(2)} of {selected.latest.host.logicalCpus} logical cores</Text><Text size="xs" c="dimmed">Derived from total CPU capacity percent × logical CPU count.</Text></div>}</Card>

      <Card withBorder radius="md" padding="lg">
<Group justify="space-between" align="flex-start" mb="md">
<div>
<Title order={3}>Resource history</Title>
<Text size="sm" c="dimmed">{history?.resolution ?? 'Raw samples and retained hourly summaries'} · times use your local timezone</Text>
</div>
<SegmentedControl value={range} onChange={(value) => { setZoomBase(null); setCustomError(''); setCustomStart(''); setCustomEnd(''); setAppliedInterval(null); setRange(value) }} data={ranges} size="xs" />
</Group>
<Group align="end" mb="md">
<TextInput type="datetime-local" step="0.001" label="Start" value={customStart} onChange={(event) => { setCustomStart(event.currentTarget.value) }} />
<TextInput type="datetime-local" step="0.001" label="End" value={customEnd} onChange={(event) => { setCustomEnd(event.currentTarget.value) }} />
<Button variant="light" onClick={() => { const start = new Date(customStart); const end = new Date(customEnd); const now = new Date(); if (!customStart || !customEnd || !Number.isFinite(start.getTime()) || !Number.isFinite(end.getTime()) || start >= end || end.getTime() - start.getTime() > 30 * 86400000 || end > new Date(now.getTime() + 60000)) { setCustomError('Enter a valid interval up to 30 days ending no more than one minute in the future.'); return } setZoomBase(null); setCustomError(''); setAppliedInterval({ start: start.toISOString(), end: end.toISOString() }); setRange('custom') }}>Apply interval</Button>
<Text size="xs" c="dimmed">Timezone: {Intl.DateTimeFormat().resolvedOptions().timeZone}</Text>
</Group>{customError && <Alert color="red" mb="sm">{customError}</Alert>}<Group justify="space-between" mb="sm">
<Text size="sm" fw={600}>Overlay metrics</Text>
<Group>
<Checkbox label="Normalize each line to 0–100%" checked={normalized || new Set(history?.series.map((item) => item.unit)).size > 1} disabled={new Set(history?.series.map((item) => item.unit)).size > 1} onChange={(event) => setNormalized(event.currentTarget.checked)} />
<Select aria-label="Network rate units" value={rateUnit} onChange={(value) => { if (value) setRateUnit(value as RateUnit) }} data={['B/s','kB/s','MB/s','GB/s','kb/s','Mb/s','Gb/s']} w={105} size="xs" />
</Group>
</Group>{historyError && <Alert color="red" title="History could not be loaded" mb="md">{historyError}<Button size="xs" variant="light" ml="sm" onClick={() => setRefresh((value) => value + 1)}>Retry</Button>
</Alert>}{!history ? <Group>
<Loader size="xs" />
<Text size="sm" c="dimmed">Loading history…</Text>
</Group> : history.metrics.length === 0 ? <Text c="dimmed">No measurements were recorded during this range.</Text> : <>
<Checkbox.Group value={metricNames} onChange={(values) => setMetricNames(values.slice(0, 5))} description="Select up to five metrics. Label variants remain separate lines.">
<Group>{history.metrics.map((name) => <Checkbox key={name} value={name} label={prettyName(name)} disabled={!metricNames.includes(name) && metricNames.length >= 5} />)}
</Group>
</Checkbox.Group>{history.series.length > 64 && <Alert color="yellow" mt="sm">More than 64 label variants match. Select fewer metrics to keep the chart readable.</Alert>}<div className="chart-frame">
<LineChartWrapper loading={historyLoading} series={history.series.slice(0,64)} range={range} history={history} normalized={normalized} onZoom={(start, end) => { if (!zoomBase) setZoomBase({ range, interval: appliedInterval }); setCustomStart(toLocalInput(start)); setCustomEnd(toLocalInput(end)); setAppliedInterval({ start: start.toISOString(), end: end.toISOString() }); setCustomError(''); setRange('custom') }} rateUnit={rateUnit} />
</div>{range === 'custom' && <Button size="xs" variant="default" mt="sm" onClick={() => { setCustomStart(zoomBase?.interval ? toLocalInput(new Date(zoomBase.interval.start)) : ''); setCustomEnd(zoomBase?.interval ? toLocalInput(new Date(zoomBase.interval.end)) : ''); setAppliedInterval(zoomBase?.interval ?? null); setRange(zoomBase?.range ?? '24h'); setZoomBase(null); setCustomError('') }}>Reset zoom</Button>}</>}</Card>
        </Stack></Tabs.Panel>
        <Tabs.Panel value="containers">      {selected.latest?.dockerContainers && selected.latest.dockerContainers.length > 0 && <Card withBorder radius="md" padding="lg">
<Group justify="space-between" mb="md">
<div>
<Title order={3}>Docker containers</Title>
<Text size="sm" c="dimmed">Containers from the latest Docker collection</Text>
</div>
<Badge variant="light">{selected.latest.dockerContainers.length}</Badge>
</Group>
<Table.ScrollContainer minWidth={520}>
<Table>
<Table.Thead>
<Table.Tr>
<Table.Th>Name</Table.Th>
<Table.Th>Image</Table.Th>
<Table.Th>State</Table.Th>
<Table.Th>Container ID</Table.Th>
</Table.Tr>
</Table.Thead>
<Table.Tbody>{selected.latest.dockerContainers.map((container) => <Table.Tr key={container.id}>
<Table.Td>{container.name || '—'}</Table.Td>
<Table.Td>{container.image || '—'}</Table.Td>
<Table.Td>
<Badge size="sm" variant="light" color={container.state === 'running' ? 'green' : 'gray'}>{container.state}</Badge>
</Table.Td>
<Table.Td>
<Text size="xs" ff="monospace">{container.id.slice(0, 12)}</Text>
</Table.Td>
</Table.Tr>)}</Table.Tbody>
</Table>
</Table.ScrollContainer>
</Card>}<Text size="sm" c="dimmed">Compose stack details are not yet available.</Text></Tabs.Panel>
      </Tabs>
    </Stack> : devices.length === 0 ? <div className="device-empty">
<Text fw={600}>No devices yet</Text>
<Text c="dimmed" size="sm" mt={6}>Enrolled agents and discovered devices will appear here.</Text>
</div> : <>
      <Group gap="xs" mb="md">{(['healthy', 'stale', 'failed', 'unsupported'] as const).map((status) => <Badge key={status} color={statusColor[status]} variant="light">{allDevices.filter((device) => device.status === status).length} {statusLabel[status]}</Badge>)}</Group>
      <Card withBorder radius="md" padding={0} className="inventory-card">
<div className="table-scroll">
<Table highlightOnHover verticalSpacing="md" horizontalSpacing="lg">
<Table.Thead>
<Table.Tr>
<Table.Th>Device</Table.Th>
<Table.Th>Platform</Table.Th>
<Table.Th>Status</Table.Th>
<Table.Th>Last check-in</Table.Th>
{section === 'monitoring' ? <><Table.Th>CPU</Table.Th><Table.Th>Memory</Table.Th><Table.Th>Network receive / send</Table.Th></> : <Table.Th>Basic stats</Table.Th>}
<Table.Th />
</Table.Tr>
</Table.Thead>
<Table.Tbody>{devices.map((device) => <Table.Tr key={device.id} className="device-row" onClick={() => openDevice(device.id, section === 'monitoring' ? 'monitoring' : 'info')} onKeyDown={(event) => { if (event.target === event.currentTarget && (event.key === 'Enter' || event.key === ' ')) { event.preventDefault(); openDevice(device.id, section === 'monitoring' ? 'monitoring' : 'info') } }} tabIndex={0} aria-label={`Open device ${device.name}`}>

<Table.Td>
<Text fw={600}>{device.name}</Text>
<Text size="xs" c="dimmed">{device.platformId || device.id.slice(0, 8)}</Text>
</Table.Td>
<Table.Td>{[device.platform, device.architecture].filter(Boolean).join(' · ') || 'Unknown'}</Table.Td>
<Table.Td>
<Badge color={statusColor[device.status]} variant="light">{statusLabel[device.status]}</Badge>
</Table.Td>
<Table.Td>
<Text size="sm">{relativeLabel(device.lastSeenAt)}</Text>
<Text size="xs" c="dimmed">{timeLabel(device.lastSeenAt)}</Text>
</Table.Td>
{section === 'monitoring' ? <>
<Table.Td>{summaryMetric(device, 'cpu_used_percent')}</Table.Td>
<Table.Td>{summaryMetric(device, 'memory_used_percent')}</Table.Td>
<Table.Td><Text size="sm">↓ {summaryMetric(device, 'network_receive_bytes_per_second', 'MB/s')}</Text><Text size="sm">↑ {summaryMetric(device, 'network_send_bytes_per_second', 'MB/s')}</Text></Table.Td>
</> : <Table.Td><Text size="sm">CPU: {summaryMetric(device, 'cpu_used_percent')} · Memory: {summaryMetric(device, 'memory_used_percent')}</Text><Text size="xs" c="dimmed">Disk total: {summaryMetric(device, 'filesystem_total_bytes')}</Text></Table.Td>}
<Table.Td>
<Button size="xs" variant="light" onClick={(event) => { event.stopPropagation(); openDevice(device.id, section === 'monitoring' ? 'monitoring' : 'info') }} aria-label={`View ${device.name}`}>View device</Button>
</Table.Td>
</Table.Tr>)}</Table.Tbody>
</Table>
</div>
</Card>
<Group justify="space-between" mt="sm">
<Text size="sm" c="dimmed">Showing {offset + 1}–{Math.min(offset + devices.length, total)} of {total}</Text>
<Group>
<Button size="xs" variant="default" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - 100))}>Previous</Button>
<Button size="xs" variant="default" disabled={offset + devices.length >= total} onClick={() => setOffset(offset + 100)}>Next</Button>
</Group>
</Group>
    </>}
  </section>
}
