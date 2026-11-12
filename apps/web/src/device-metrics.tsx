import { useEffect, useMemo, useRef, useState } from 'react'
import { Alert, Button, Card, Checkbox, Group, Loader, LoadingOverlay, SegmentedControl, Select, Stack, Text, TextInput } from '@mantine/core'
import { LineChart } from '@mantine/charts'
import { ReferenceArea } from 'recharts'
import { DeviceMonitoring } from './device-monitoring'
import { getJson, type Device } from './device-types'
import { chartRows, formatMetric, type MetricSeries, type RateUnit } from './metric-utils'
import { Panel } from './ui'

type Point = { at: string; value: number | null; min?: number; max?: number; samples: number; resolutionSeconds?: number }
type Series = { name: string; unit: string; labels: Record<string, string>; points: Point[] }
type History = { range: string; windowStart: string; windowEnd: string; metrics: string[]; series: Series[]; resolutionSeconds: number; resolution: string }
type Interval = { start: string; end: string }

export const metricRanges = [{ label: '1h', value: '1h' }, { label: '6h', value: '6h' }, { label: '24h', value: '24h' }, { label: '7d', value: '7d' }, { label: '30d', value: '30d' }, { label: '90d', value: '90d' }, { label: '1y', value: '365d' }]
const rateUnits: RateUnit[] = ['B/s', 'kB/s', 'MB/s', 'GB/s', 'kb/s', 'Mb/s', 'Gb/s']

const metricLabels: Record<string, string> = {
  docker_cpu_capacity_used_percent: 'Container CPU capacity use', docker_memory_usage_bytes: 'Container memory use', docker_memory_limit_bytes: 'Container memory limit', docker_memory_used_percent: 'Container memory use', docker_processes: 'Container processes', docker_network_receive_bytes_per_second: 'Container network receive rate', docker_network_transmit_bytes_per_second: 'Container network send rate',
  cpu_used_percent: 'CPU use', memory_used_percent: 'Memory use', memory_available_bytes: 'Available memory', memory_total_bytes: 'Total memory',
  filesystem_free_bytes: 'Free disk space', filesystem_total_bytes: 'Total disk space',
  network_receive_bytes_per_second: 'Network receive rate', network_send_bytes_per_second: 'Network send rate',
  disk_read_bytes_per_second: 'Disk read rate', disk_write_bytes_per_second: 'Disk write rate', uptime_seconds: 'Uptime',
}
const prettyName = (name: string) => metricLabels[name] ?? name.replaceAll('_', ' ').replace(/\b\w/g, (part) => part.toUpperCase())
const labelValue = (key: string, value: string) => key === 'container_id' ? value.slice(0, 12) : value
const timeLabel = (value: string) => new Date(value).toLocaleString()
const toLocalInput = (value: Date) => new Date(value.getTime() - value.getTimezoneOffset() * 60_000).toISOString().slice(0, 23)

/** The Metrics tab: one range control drives every card and the history explorer below them. */
export function DeviceMetrics({ device }: { device: Device }) {
  const [range, setRange] = useState('24h')
  const [rateUnit, setRateUnit] = useState<RateUnit>('MB/s')
  return <Stack gap="lg">
    <Group justify="space-between" align="center" className="vg-toolbar">
      <Text size="sm" c="dimmed">Latest values observed {device.telemetryObservedAt ? new Date(device.telemetryObservedAt).toLocaleString() : '— time not reported'}</Text>
      <Group gap="sm">
        <SegmentedControl aria-label="Time range" size="xs" value={range} onChange={setRange} data={metricRanges} />
        <Select aria-label="Network rate units" size="xs" w={96} value={rateUnit} onChange={(value) => { if (value) setRateUnit(value as RateUnit) }} data={rateUnits} allowDeselect={false} />
      </Group>
    </Group>
    {!!device.collectionErrors?.length && <Alert color="red" title="Collection errors">{device.collectionErrors.map((item, index) => <Text key={`${item.capability}-${index}`} size="sm">{item.capability}: {item.message}</Text>)}</Alert>}
    <DeviceMonitoring key={device.id} device={device} range={range} rateUnit={rateUnit} />
    <HistoryExplorer key={`history-${device.id}`} deviceId={device.id} range={range} rateUnit={rateUnit} />
  </Stack>
}

/** Overlay up to five metrics over the range, or over a custom interval chosen by dragging across the chart. */
function HistoryExplorer({ deviceId, range, rateUnit }: { deviceId: string; range: string; rateUnit: RateUnit }) {
  const [history, setHistory] = useState<History | null>(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const [metricNames, setMetricNames] = useState<string[]>([])
  const [normalized, setNormalized] = useState(false)
  const [zoom, setZoom] = useState<Interval | null>(null)
  const [editing, setEditing] = useState(false)
  const [customStart, setCustomStart] = useState('')
  const [customEnd, setCustomEnd] = useState('')
  const [customError, setCustomError] = useState('')
  const [retry, setRetry] = useState(0)
  const initialMetric = useRef(true)

  // A new range from the toolbar replaces any zoomed interval.
  useEffect(() => { setZoom(null); setCustomError('') }, [range])
  useEffect(() => {
    let active = true
    setLoading(true); setError('')
    const query = new URLSearchParams()
    if (zoom) { query.set('start', zoom.start); query.set('end', zoom.end) } else query.set('range', range)
    metricNames.slice(0, 5).forEach((name) => query.append('metrics', name))
    void getJson<History>(`/api/inventory/devices/${encodeURIComponent(deviceId)}/history?${query}`)
      .then((data) => { if (!active) return; setHistory(data); if (initialMetric.current && data.metrics.length) { initialMetric.current = false; setMetricNames([data.metrics.includes('cpu_used_percent') ? 'cpu_used_percent' : data.metrics[0]!]) } })
      .catch((cause: unknown) => { if (active) setError(cause instanceof Error ? cause.message : 'Could not load metric history.') })
      .finally(() => { if (active) setLoading(false) })
    return () => { active = false }
  }, [deviceId, range, metricNames, zoom, retry])

  function applyCustom() {
    const start = new Date(customStart); const end = new Date(customEnd)
    if (!customStart || !customEnd || !Number.isFinite(start.getTime()) || !Number.isFinite(end.getTime()) || start >= end || end.getTime() - start.getTime() > 365 * 86400000 || end.getTime() > Date.now() + 60000) {
      setCustomError('Enter a valid interval up to 365 days ending no more than one minute in the future.'); return
    }
    setCustomError(''); setZoom({ start: start.toISOString(), end: end.toISOString() }); setEditing(false)
  }
  const mixedUnits = new Set(history?.series.map((item) => item.unit)).size > 1

  return <Panel title="Explore history" actions={<>
    {zoom && <Text size="xs" c="dimmed">{timeLabel(zoom.start)} – {timeLabel(zoom.end)}</Text>}
    {zoom && <Button size="xs" variant="subtle" onClick={() => setZoom(null)}>Reset zoom</Button>}
    <Button size="xs" variant="subtle" onClick={() => { setEditing((v) => !v); if (!customStart && history) { setCustomStart(toLocalInput(new Date(history.windowStart))); setCustomEnd(toLocalInput(new Date(history.windowEnd))) } }}>Custom interval</Button>
  </>}>
    <Stack gap="sm">
      {editing && <Group align="end">
        <TextInput type="datetime-local" step="0.001" label="Start" size="xs" value={customStart} onChange={(event) => setCustomStart(event.currentTarget.value)} />
        <TextInput type="datetime-local" step="0.001" label="End" size="xs" value={customEnd} onChange={(event) => setCustomEnd(event.currentTarget.value)} />
        <Button size="xs" variant="light" onClick={applyCustom}>Apply</Button>
        <Text size="xs" c="dimmed">Timezone: {Intl.DateTimeFormat().resolvedOptions().timeZone}</Text>
      </Group>}
      {customError && <Alert color="red">{customError}</Alert>}
      {error && <Alert color="red" title="History could not be loaded">{error}<Button size="xs" variant="light" ml="sm" onClick={() => setRetry((v) => v + 1)}>Retry</Button></Alert>}
      {!history ? <Group><Loader size="xs" /><Text size="sm" c="dimmed">Loading history…</Text></Group>
        : history.metrics.length === 0 ? <Text c="dimmed" size="sm">No measurements were recorded during this range.</Text>
        : <>
          <Checkbox.Group value={metricNames} onChange={(values) => setMetricNames(values.slice(0, 5))} description="Up to five metrics. Each label variant (a disk, an interface, a container) is its own line.">
            <Group gap="sm" mt={6}>{history.metrics.map((name) => <Checkbox size="xs" key={name} value={name} label={prettyName(name)} disabled={!metricNames.includes(name) && metricNames.length >= 5} />)}</Group>
          </Checkbox.Group>
          <Checkbox size="xs" label="Normalize each line to 0–100%" checked={normalized || mixedUnits} disabled={mixedUnits} onChange={(event) => setNormalized(event.currentTarget.checked)} />
          {history.series.length > 64 && <Alert color="yellow">More than 64 label variants match. Select fewer metrics to keep the chart readable.</Alert>}
          <HistoryChart loading={loading} series={history.series.slice(0, 64)} history={history} normalized={normalized} rateUnit={rateUnit}
            onZoom={(start, end) => { setCustomStart(toLocalInput(start)); setCustomEnd(toLocalInput(end)); setZoom({ start: start.toISOString(), end: end.toISOString() }) }} />
          <Text size="xs" c="dimmed">{history.resolution} · times use your local timezone · drag across the plot to zoom</Text>
        </>}
    </Stack>
  </Panel>
}

function HistoryChart({ loading, series, history, normalized, onZoom, rateUnit }: { loading: boolean; series: Series[]; history: History; normalized: boolean; onZoom: (start: Date, end: Date) => void; rateUnit: RateUnit }) {
  const useNormalized = normalized || new Set(series.map((item) => item.unit)).size > 1
  const rows = useMemo(() => chartRows(series as MetricSeries[], history.resolutionSeconds || 60, useNormalized, rateUnit), [series, history.resolutionSeconds, useNormalized, rateUnit])
  const [dragStart, setDragStart] = useState<number | null>(null)
  const [dragEnd, setDragEnd] = useState<number | null>(null)
  const name = (item: Series) => prettyName(item.name) + (Object.keys(item.labels).length ? ` · ${Object.entries(item.labels).map(([key, value]) => labelValue(key, value)).join(', ')}` : '')
  const tooltip = (props: { active?: boolean; payload?: Array<{ dataKey: string; value: number | null; payload: Record<string, number | null> }> }) => {
    if (!props.active || !props.payload?.length) return null
    return <Card withBorder shadow="sm" p="xs">{props.payload.map((item) => {
      const index = Number(item.dataKey.replace('series_', '')); const original = item.payload[`actual_${index}`]; const itemSeries = series[index]
      return original == null || !itemSeries ? null : <Text key={item.dataKey} size="xs">{name(itemSeries)}: {formatMetric(original, itemSeries.unit, rateUnit)} · {new Date(Number(item.payload.timestamp)).toLocaleString()}</Text>
    })}</Card>
  }
  if (!series.length) return <Stack h={260} pos="relative" align="center" justify="center"><LoadingOverlay visible={loading} /><Text c="dimmed" size="sm">Select a metric above.</Text></Stack>
  const start = new Date(history.windowStart).getTime(); const end = new Date(history.windowEnd).getTime()
  return <div className="chart-frame" style={{ position: 'relative' }}>
    <LoadingOverlay visible={loading} overlayProps={{ blur: 1 }} />
    <LineChart h={280} data={rows.data} dataKey="timestamp" series={rows.chartSeries.map((value) => ({ name: value.name, label: name(series[Number(value.name.replace('series_', ''))]!), color: value.color }))}
      withLegend connectNulls={false} withDots={false} strokeWidth={1.5}
      yAxisLabel={useNormalized ? 'Normalized (%)' : series[0]?.unit === 'By/s' ? rateUnit : series[0]?.unit}
      xAxisProps={{ type: 'number', domain: [start, end], tickFormatter: (value: number) => end - start > 86400000 ? new Date(value).toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit' }) : new Date(value).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) }}
      yAxisProps={{ domain: useNormalized ? [0, 100] : ['auto', 'auto'], tickFormatter: (value: number) => useNormalized ? `${value}%` : String(value) }}
      tooltipProps={{ content: tooltip as never }}
      lineChartProps={{
        onMouseDown: (state) => { const at = Number(state?.activeLabel); if (Number.isFinite(at)) { setDragStart(at); setDragEnd(at) } },
        onMouseMove: (state) => { const at = Number(state?.activeLabel); if (dragStart !== null && Number.isFinite(at)) setDragEnd(at) },
        onMouseUp: (state) => { const at = Number(state?.activeLabel ?? dragEnd); if (dragStart !== null && Number.isFinite(at) && Math.abs(at - dragStart) > 5000) onZoom(new Date(Math.min(at, dragStart)), new Date(Math.max(at, dragStart))); setDragStart(null); setDragEnd(null) },
        onMouseLeave: () => { setDragStart(null); setDragEnd(null) },
      }}>
      <ReferenceArea yAxisId="left" x1={dragStart ?? undefined} x2={dragEnd ?? undefined} fill="var(--mantine-color-blue-5)" fillOpacity={0.18} stroke="var(--mantine-color-blue-6)" strokeOpacity={0.7} />
    </LineChart>
  </div>
}
