import { useEffect, useMemo, useState } from 'react'
import { Alert, Badge, Button, Card, Group, Loader, Progress, SegmentedControl, Select, SimpleGrid, Stack, Text, Title } from '@mantine/core'
import { LineChart } from '@mantine/charts'
import { type Device } from './devices'
import { chartRows, formatMetric, type MetricSeries, type RateUnit } from './metric-utils'
import { labelKey, measurementName, monitoringSections, type MonitoringCard } from './monitoring-utils'

type TrendHistory = { series: MetricSeries[]; resolutionSeconds: number; windowStart: string; windowEnd: string }
type TrendState = { key: string; history: TrendHistory | null; loading: boolean; error: string }
const colors = ['cyan.6', 'grape.5', 'teal.6', 'orange.6']
const intervals = [{ label: '1h', value: '1h' }, { label: '6h', value: '6h' }, { label: '24h', value: '24h' }, { label: '7d', value: '7d' }]

function Trend({ card, history, loading, failed, rateUnit }: { card: MonitoringCard; history: TrendHistory | null; loading: boolean; failed: boolean; rateUnit: RateUnit }) {
  const series = useMemo(() => card.trends.flatMap(metric => history?.series.filter(item => item.name === metric.name && labelKey(item.labels) === labelKey(metric.labels)) ?? []), [card, history])
  // Keep original units in chart rows; apply the selected rate units once, in the formatter.
  const rows = useMemo(() => chartRows(series, history?.resolutionSeconds || 60, false, 'B/s'), [series, history])
  const hasPoints = series.some(item => item.points.some(point => point.value !== null))
  const unit = series[0]?.unit ?? card.trends[0]?.unit ?? ''
  return <div className="monitoring-trend">
    <Group gap="xs" justify="space-between" mb={6}><Text size="xs" c="dimmed">{card.trends.map(metric => measurementName(metric.name)).join(' / ')} trend</Text>{loading && <Loader size={12} aria-label="Loading trend" />}</Group>
    {hasPoints ? <div role="img" aria-label={`${card.title}: ${card.trends.map(metric => measurementName(metric.name)).join(' and ')} over the selected interval`}>
      <LineChart h={110} data={rows.data} dataKey="timestamp"
        series={rows.chartSeries.map((item, index) => ({ name: item.name, label: measurementName(series[index]!.name), color: colors[index % colors.length]! }))}
        withXAxis={false} withYAxis={false} gridAxis="none" withDots={series.every(item => item.points.filter(point => point.value !== null).length <= 1)} strokeWidth={2} curveType="linear" connectNulls={false}
        valueFormatter={value => formatMetric(value, unit, rateUnit)}
        xAxisProps={{ type: 'number', domain: history ? [new Date(history.windowStart).getTime(), new Date(history.windowEnd).getTime()] : ['dataMin', 'dataMax'] }}
        yAxisProps={{ domain: unit === '%' ? [0, 100] : [0, 'auto'] }}
        tooltipProps={{ labelFormatter: value => new Date(Number(value)).toLocaleString() }}
      />
    </div> : <div className="monitoring-trend-empty"><Text size="xs" c="dimmed">{loading ? 'Loading trend…' : failed ? 'Trend unavailable' : 'No history in this interval'}</Text></div>}
  </div>
}

function ResourceCard({ card, history, loading, failed, rateUnit, logicalCpus }: { card: MonitoringCard; history: TrendHistory | null; loading: boolean; failed: boolean; rateUnit: RateUnit; logicalCpus?: number }) {
  const total = card.capacity?.total?.value
  const free = card.capacity?.free?.value
  const validCapacity = total !== undefined && free !== undefined && Number.isFinite(total) && Number.isFinite(free) && total > 0 && free >= 0 && free <= total
  const usedPercent = validCapacity ? (total - free) / total * 100 : null
  const primary = card.trends[0]
  return <Card withBorder radius="md" padding="lg" className="monitoring-resource-card">
    <Group justify="space-between" mb="sm"><Text fw={600}>{card.title}</Text>{card.metrics[0]?.labels?.filesystem && <Badge size="xs" color="gray" variant="light">{card.metrics[0].labels.filesystem}</Badge>}</Group>
    <Group align="flex-end" gap="xl" className="monitoring-values">
      {card.metrics.map(metric => <div key={`${metric.name}:${labelKey(metric.labels)}`}>
        <Group gap={5}>{card.trends.length > 1 && <span className="monitoring-series-dot" style={{ background: `var(--mantine-color-${colors[Math.max(0, card.trends.indexOf(metric)) % colors.length]!.replace('.', '-')})` }} />}<Text size="xs" c="dimmed">{measurementName(metric.name)}</Text></Group>
        <Text size={metric === primary ? 'xl' : 'lg'} fw={metric === primary ? 700 : 500}>{Number.isFinite(metric.value) ? formatMetric(metric.value, metric.unit, rateUnit) : 'Unavailable'}</Text>
      </div>)}
    </Group>
    {usedPercent !== null && <div className="monitoring-capacity"><Progress value={usedPercent} color={usedPercent >= 90 ? 'red' : usedPercent >= 80 ? 'orange' : 'cyan'} size={6} aria-label={`${card.title}: ${usedPercent.toFixed(1)}% used`} /><Text size="xs" c="dimmed" mt={5}>{usedPercent.toFixed(1)}% used · {formatMetric(total! - free!, 'By')} used</Text></div>}
    {logicalCpus && primary?.name === 'cpu_used_percent' ? <Text size="xs" c="dimmed" mt="xs">≈ {(primary.value * logicalCpus / 100).toFixed(2)} of {logicalCpus} logical cores in use</Text> : null}
    {card.capacity && usedPercent === null && <Text size="xs" c="dimmed" mt="xs">{free === undefined ? 'Free space not reported' : total === undefined ? 'Total capacity not reported' : 'Capacity usage unavailable'}</Text>}
    {card.trends.length > 0 && <Trend card={card} history={history} loading={loading} failed={failed} rateUnit={rateUnit} />}
  </Card>
}

export function DeviceMonitoring({ device, refresh, rateUnit, onRateUnitChange }: { device: Device; refresh: number; rateUnit: RateUnit; onRateUnitChange: (unit: RateUnit) => void }) {
  const [range, setRange] = useState('24h')
  const [retry, setRetry] = useState(0)
  const sections = useMemo(() => monitoringSections(device.latest?.metrics ?? [], device.latest?.dockerContainers), [device.latest])
  const names = JSON.stringify([...new Set(sections.flatMap(section => section.groups.flatMap(group => group.cards.flatMap(card => card.trends.map(metric => metric.name)))))].sort())
  const requestKey = `${device.id}:${range}:${names}:${refresh}:${retry}`
  const [state, setState] = useState<TrendState>({ key: '', history: null, loading: true, error: '' })
  useEffect(() => {
    const controller = new AbortController()
    const metricNames = JSON.parse(names) as string[]
    setState({ key: requestKey, history: null, loading: true, error: '' })
    async function load() {
      // The history API accepts five metric names per request. Bound concurrent work.
      const batches: string[][] = []
      for (let index = 0; index < metricNames.length; index += 5) batches.push(metricNames.slice(index, index + 5))
      const results: TrendHistory[] = []; let failures = 0
      async function readBatch(batch: string[]): Promise<TrendHistory[]> {
        const query = new URLSearchParams({ range })
        batch.forEach(name => query.append('metrics', name))
        const response = await fetch(`/api/inventory/devices/${encodeURIComponent(device.id)}/history?${query}`, { credentials: 'same-origin', signal: controller.signal })
        // Many interfaces or containers may exceed the API's point/series limit
        // even with five names. Retry smaller batches without losing other trends.
        if (response.status === 422 && batch.length > 1) {
          const middle = Math.ceil(batch.length / 2)
          const parts: TrendHistory[] = []
          for (const half of [batch.slice(0, middle), batch.slice(middle)]) {
            try { parts.push(...await readBatch(half)) } catch { if (!controller.signal.aborted) failures++ }
          }
          return parts
        }
        if (!response.ok) throw new Error('Could not load trends.')
        return [await response.json() as TrendHistory]
      }
      for (let index = 0; index < batches.length; index += 3) {
        const responses = await Promise.allSettled(batches.slice(index, index + 3).map(readBatch))
        if (controller.signal.aborted) return
        responses.forEach(result => { if (result.status === 'fulfilled') results.push(...result.value); else failures++ })
      }
      if (!controller.signal.aborted) setState({ key: requestKey, loading: false, error: failures ? 'Some trends could not be loaded. Try a shorter interval or retry. Latest measurements are still shown.' : '', history: results[0] ? { ...results[0], series: results.flatMap(result => result.series) } : null })
    }
    void load()
    return () => controller.abort()
  }, [device.id, range, names, requestKey])
  const current = state.key === requestKey ? state : { history: null, loading: true, error: '' }
  return <Stack gap="xl">
    <Group justify="space-between" align="flex-end">
      <div><Title order={3}>Resource overview</Title><Text size="sm" c="dimmed" mt={4}>Latest values · observed {device.telemetryObservedAt ? new Date(device.telemetryObservedAt).toLocaleString() : 'time not reported'}</Text></div>
      <Group align="flex-end"><div><Text size="xs" c="dimmed" mb={4}>Trend interval</Text><SegmentedControl aria-label="Overview trend interval" size="xs" value={range} onChange={setRange} data={intervals} /></div><Select label="Rate units" aria-label="Overview rate units" size="xs" w={110} value={rateUnit} onChange={value => { if (value) onRateUnitChange(value as RateUnit) }} data={['B/s', 'kB/s', 'MB/s', 'GB/s', 'kb/s', 'Mb/s', 'Gb/s']} /></Group>
    </Group>
    {current.error && <Alert color="yellow" title="Trends unavailable">{current.error}<Button size="xs" variant="light" ml="sm" onClick={() => setRetry(value => value + 1)}>Retry trends</Button></Alert>}
    {!sections.length && <Text c="dimmed">No measurements were reported in the latest snapshot. Missing values are not treated as zero.</Text>}
    {sections.map(section => <section key={section.id} aria-labelledby={`monitoring-${section.id}`}>
      <Title order={4} id={`monitoring-${section.id}`} mb={4}>{section.title}</Title>
      <Text size="sm" c="dimmed" mb="md">{section.description}</Text>
      <div className="monitoring-groups">{section.groups.map(group => <div className={`${group.title ? 'monitoring-resource-group' : ''} ${group.cards.length > 1 ? 'monitoring-group-wide' : ''}`} key={group.id}>
        {group.title && <Text fw={600} size="sm" mb="sm" className="monitoring-group-label">{group.title}</Text>}
        <SimpleGrid cols={{ base: 1, sm: Math.min(2, group.cards.length), xl: Math.min(3, group.cards.length) }} spacing="md">{group.cards.map(card => <ResourceCard key={card.id} card={card} history={current.history} loading={current.loading} failed={!!current.error} rateUnit={rateUnit} logicalCpus={device.latest?.host?.logicalCpus} />)}</SimpleGrid>
      </div>)}</div>
    </section>)}
    {current.history && <Text size="xs" c="dimmed">Trends: {new Date(current.history.windowStart).toLocaleString()} – {new Date(current.history.windowEnd).toLocaleString()}. Hover for values. Gaps indicate missing measurements.</Text>}
  </Stack>
}
