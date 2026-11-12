import { useEffect, useMemo, useState } from 'react'
import { Alert, Badge, Button, Card, Group, Loader, Progress, SimpleGrid, Stack, Text, Title } from '@mantine/core'
import { LineChart } from '@mantine/charts'
import { type Device } from './device-types'
import { chartRows, formatMetric, type RateUnit } from './metric-utils'
import { labelKey, measurementName, monitoringSections, type MonitoringCard } from './monitoring-utils'
import { trendRefreshMs, trendToHistory, type TrendHistory, type TrendResponse } from './trend-utils'

// `range` ties the held history to the interval it was loaded for, so a different
// interval never shows the previous one's points while it loads.
type TrendState = { range: string; history: TrendHistory | null; loading: boolean; error: string }
const colors = ['cyan.6', 'grape.5', 'teal.6', 'orange.6']

function Trend({ card, history, loading, failed, rateUnit }: { card: MonitoringCard; history: TrendHistory | null; loading: boolean; failed: boolean; rateUnit: RateUnit }) {
  const series = useMemo(() => card.trends.flatMap(metric => history?.series.filter(item => item.name === metric.name && labelKey(item.labels) === labelKey(metric.labels)) ?? []), [card, history])
  // Keep original units in chart rows; apply the selected rate units once, in the formatter.
  const rows = useMemo(() => chartRows(series, history?.resolutionSeconds || 60, false, 'B/s'), [series, history])
  const hasPoints = series.some(item => item.points.some(point => point.value !== null))
  const unit = series[0]?.unit || card.trends[0]?.unit || ''
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

/**
 * Every measurement in the device's latest snapshot, grouped by section (host, filesystems,
 * network, containers…), each card with a trend over `range`. One request loads all trends.
 */
export function DeviceMonitoring({ device, range, rateUnit }: { device: Device; range: string; rateUnit: RateUnit }) {
  const [retry, setRetry] = useState(0)
  const sections = useMemo(() => monitoringSections(device.latest?.metrics ?? [], device.latest?.dockerContainers), [device.latest])
  const [state, setState] = useState<TrendState>({ range, history: null, loading: true, error: '' })
  useEffect(() => {
    // Refreshing keeps the charts on screen and swaps the data in place; only a new
    // interval or device clears them, because the old points belong to a different grid.
    const controller = new AbortController()
    let timer: number | undefined
    let loadedAt = 0
    const period = trendRefreshMs(range)
    setState(current => current.range === range && current.history ? { ...current, error: '' } : { range, history: null, loading: true, error: '' })
    async function load() {
      window.clearTimeout(timer)
      try {
        const response = await fetch(`/api/inventory/devices/${encodeURIComponent(device.id)}/trends?range=${encodeURIComponent(range)}`, { credentials: 'same-origin', signal: controller.signal })
        if (!response.ok) throw new Error('Could not load trends.')
        const history = trendToHistory(await response.json() as TrendResponse)
        if (controller.signal.aborted) return
        loadedAt = Date.now()
        setState({ range, history, loading: false, error: '' })
      } catch {
        if (controller.signal.aborted) return
        setState(current => ({ range, history: current.range === range ? current.history : null, loading: false, error: 'Trends could not be loaded. Latest measurements are still shown.' }))
      }
      schedule()
    }
    function schedule() { timer = window.setTimeout(() => { if (document.hidden) schedule(); else void load() }, period) }
    // Background tabs skip refreshes; returning shows fresh data without waiting a full period.
    function onVisible() { if (!document.hidden && Date.now() - loadedAt >= period) void load() }
    document.addEventListener('visibilitychange', onVisible)
    void load()
    return () => { controller.abort(); window.clearTimeout(timer); document.removeEventListener('visibilitychange', onVisible) }
  }, [device.id, range, retry])
  const current = state.range === range ? state : { range, history: null, loading: true, error: '' }
  return <Stack gap="xl">
    {current.error && <Alert color="yellow" title="Trends unavailable">{current.error}<Button size="xs" variant="light" ml="sm" onClick={() => setRetry(value => value + 1)}>Retry trends</Button></Alert>}
    {!sections.length && <Text c="dimmed">No measurements were reported in the latest snapshot. Missing values are not treated as zero.</Text>}
    {/* Each section folds; per-container cards start folded when there are many, so the page stays scannable. */}
    {sections.map(section => <details key={section.id} className="vg-section" open={!(section.id === 'containers' && section.groups.length > 3)}>
      <summary><Title order={3} component="span" id={`monitoring-${section.id}`}>{section.title}</Title><Text size="sm" c="dimmed" component="span">{section.id === 'containers' ? `${section.groups.length} containers · ` : ''}{section.description}</Text></summary>
      <div className="monitoring-groups">{section.groups.map(group => <div className={`${group.title ? 'monitoring-resource-group' : ''} ${group.cards.length > 1 ? 'monitoring-group-wide' : ''}`} key={group.id}>
        {group.title && <Text fw={600} size="sm" mb="sm" className="monitoring-group-label">{group.title}</Text>}
        <SimpleGrid cols={{ base: 1, sm: Math.min(2, group.cards.length), xl: Math.min(3, group.cards.length) }} spacing="md">{group.cards.map(card => <ResourceCard key={card.id} card={card} history={current.history} loading={current.loading} failed={!!current.error} rateUnit={rateUnit} logicalCpus={device.latest?.host?.logicalCpus} />)}</SimpleGrid>
      </div>)}</div>
    </details>)}
    {current.history && <Text size="xs" c="dimmed">Trends: {new Date(current.history.windowStart).toLocaleString()} – {new Date(Math.min(new Date(current.history.windowEnd).getTime(), Date.now())).toLocaleString()}, {current.history.resolutionSeconds >= 3600 ? `${current.history.resolutionSeconds / 3600}-hour` : `${current.history.resolutionSeconds / 60}-minute`} averages. Hover for values. Gaps indicate missing measurements.</Text>}
  </Stack>
}
