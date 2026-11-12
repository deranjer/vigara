import { useEffect, useMemo, useState } from 'react'
import { Button, Group, Loader, SegmentedControl, Table, Text } from '@mantine/core'
import { LineChart } from '@mantine/charts'
import type { HostEntry } from './container-utils'
import { driveUsage } from './device-info-utils'
import { getJson, type Device } from './device-types'
import { compareValues, containersByStack, gigabytes, osName, shortUptime, type DeviceTab } from './device-utils'
import { formatMetric } from './metric-utils'
import { topContainers, worstDisk } from './overview-utils'
import { route, type Route } from './route'
import type { TrendResponse } from './trend-utils'
import { MetricBar, Panel, Sparkline, Stat } from './ui'

type Range = '6h' | '24h' | '7d'

const latest = (device: Device, name: string) => device.latest?.metrics?.find((m) => m.name === name && !m.labels?.container_id)?.value ?? null
const latestSum = (device: Device, names: string[]) => {
  const values = device.latest?.metrics?.filter((m) => names.includes(m.name) && !m.labels?.container_id).map((m) => m.value) ?? []
  return values.length ? values.reduce((a, b) => a + b, 0) : null
}

function useTrend(deviceId: string, range: Range): TrendResponse | null {
  const [trend, setTrend] = useState<TrendResponse | null>(null)
  useEffect(() => {
    let active = true
    setTrend(null)
    const load = () => void getJson<TrendResponse>(`/api/inventory/devices/${encodeURIComponent(deviceId)}/trends?range=${range}`).then((value) => { if (active) setTrend(value) }).catch(() => undefined)
    load()
    const timer = window.setInterval(load, 120_000)
    return () => { active = false; window.clearInterval(timer) }
  }, [deviceId, range])
  return trend
}

/** A device at a glance: four headline figures, CPU and memory over time, its containers, disks and identity. */
export function DeviceOverview({ device, host, onNavigate, onOpenTab }: { device: Device; host: HostEntry | null; onNavigate: (route: Route) => void; onOpenTab: (tab: DeviceTab) => void }) {
  const [range, setRange] = useState<Range>('24h')
  const trend = useTrend(device.id, range)
  const lines = useMemo(() => trend ? { cpu: compareValues(trend, 'cpu'), memory: compareValues(trend, 'memory'), disk: compareValues(trend, 'disk'), network: compareValues(trend, 'network') } : null, [trend])
  const chart = useMemo(() => {
    if (!trend || !lines) return []
    const start = Date.parse(trend.windowStart)
    return lines.cpu.map((cpu, i) => ({ timestamp: start + i * trend.stepSeconds * 1000, cpu, memory: lines.memory[i] ?? null }))
  }, [trend, lines])
  const host_ = device.latest?.host
  const cpu = latest(device, 'cpu_used_percent')
  const memory = latest(device, 'memory_used_percent')
  const disk = worstDisk(device)
  const network = latestSum(device, ['network_receive_bytes_per_second', 'network_send_bytes_per_second'])
  const memoryUsed = memory !== null && host_?.totalMemoryBytes ? memory / 100 * host_.totalMemoryBytes : null
  const top = topContainers([device], 6)
  const drives = driveUsage(device.latest?.metrics)
  const stacks = containersByStack(host?.containers ?? [])
  const networkPeak = lines ? Math.max(1, ...lines.network.filter((v): v is number => v !== null)) : 1

  const about: [string, string][] = [
    ['OS', osName(host_, device.platform)],
    ['Machine', [host_?.hardwareInventory?.system?.manufacturer, host_?.hardwareInventory?.system?.model].filter(Boolean).join(' ') || (host_?.hardwareInventory?.environment?.kind === 'virtual-machine' ? `Virtual machine${host_.hardwareInventory.environment.hypervisor ? ` · ${host_.hardwareInventory.environment.hypervisor}` : ''}` : '—')],
    ['Architecture', host_?.architecture || device.architecture || '—'],
    ['Uptime', shortUptime(host_?.uptimeSeconds) ?? '—'],
    ['Agent', host_?.agentVersion || device.agentVersion || (device.agentId ? 'version not reported' : 'not installed')],
  ]

  return <div className="vg-device-overview">
    <div className="vg-stat-grid">
      <StatCard label="CPU" value={cpu === null ? '—' : cpu.toFixed(1)} unit={cpu === null ? undefined : '%'} detail={host_?.logicalCpus ? `${host_.logicalCpus} vCPU` : undefined} values={lines?.cpu} />
      <StatCard label="Memory" value={memory === null ? '—' : memory.toFixed(0)} unit={memory === null ? undefined : '%'} detail={memoryUsed !== null ? `${(memoryUsed / 2 ** 30).toFixed(1)} of ${gigabytes(host_!.totalMemoryBytes!)}` : undefined} tone={memory !== null && memory >= 90 ? 'crit' : memory !== null && memory >= 80 ? 'warn' : undefined} values={lines?.memory} />
      <StatCard label={`Disk ${disk?.mount ?? ''}`.trim()} value={disk ? disk.percent.toFixed(0) : '—'} unit={disk ? '%' : undefined} detail={disk ? 'fullest filesystem' : 'not reported'} tone={disk && disk.percent >= 90 ? 'crit' : disk && disk.percent >= 85 ? 'warn' : undefined} values={lines?.disk} />
      <StatCard label="Network" value={network === null ? '—' : formatMetric(network, 'By/s', 'MB/s')} detail="receive + send" values={lines?.network} max={networkPeak} />
    </div>

    <div className="vg-split">
      <div className="vg-col">
        <Panel title={`CPU and memory · ${range}`} actions={<>
          <SegmentedControl size="xs" value={range} onChange={(v) => setRange(v as Range)} data={['6h', '24h', '7d']} aria-label="Chart range" />
          <Button size="xs" variant="subtle" onClick={() => onOpenTab('metrics')}>Open in Metrics</Button>
        </>}>
          {!trend ? <Group h={200} justify="center"><Loader size="sm" /></Group>
            : chart.every((row) => row.cpu === null && row.memory === null) ? <Text c="dimmed" size="sm">No measurements in this range.</Text>
            : <LineChart h={200} data={chart} dataKey="timestamp" connectNulls={false} withDots={false} strokeWidth={1.5} curveType="linear" withLegend legendProps={{ verticalAlign: 'top', height: 28 }}
              series={[{ name: 'cpu', label: 'CPU', color: 'blue.6' }, { name: 'memory', label: 'Memory', color: 'teal.6' }]}
              valueFormatter={(v) => `${v.toFixed(1)}%`} yAxisProps={{ domain: [0, 100], width: 48, tickFormatter: (v: number) => `${v}%` }}
              xAxisProps={{ type: 'number', domain: ['dataMin', 'dataMax'], tickFormatter: (v: number) => range === '7d' ? new Date(v).toLocaleDateString([], { weekday: 'short' }) : new Date(v).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) }}
              tooltipProps={{ labelFormatter: (v) => new Date(Number(v)).toLocaleString() }} />}
        </Panel>

        <Panel title="Containers by stack" count={host?.containers.length} actions={<Button size="xs" variant="subtle" onClick={() => onOpenTab('containers')}>All containers</Button>} flush>
          {!device.agentId ? <Text c="dimmed" size="sm" p="md">No enrolled agent.</Text>
            : !host ? <Text c="dimmed" size="sm" p="md">Loading…</Text>
            : host.state !== 'ok' ? <Text c="dimmed" size="sm" p="md">{host.state === 'offline' ? 'Device offline.' : 'Docker is not available on this device.'}</Text>
            : !stacks.length ? <Text c="dimmed" size="sm" p="md">No containers.</Text>
            : <Table className="vg-table" verticalSpacing={8}><Table.Tbody>{stacks.map((group) => {
              const running = group.rows.filter((c) => c.state === 'running').length
              const bad = group.rows.filter((c) => c.state === 'restarting' || c.status.includes('(unhealthy)'))
              return <Table.Tr key={group.project || 'standalone'}>
                <Table.Td><span className="vg-cell-name"><span className="vg-status-dot" data-tone={bad.length ? 'warn' : running === group.rows.length ? 'ok' : 'idle'} aria-hidden="true" /><span><b>{group.project || 'Standalone'}</b><small>{group.rows.map((c) => c.name).join(', ')}</small></span></span></Table.Td>
                <Table.Td ta="right"><Text size="sm" c={bad.length ? 'orange' : 'dimmed'} className="vg-num">{bad.length ? `${bad.length} unhealthy · ` : ''}{running}/{group.rows.length} running</Text></Table.Td>
              </Table.Tr>
            })}</Table.Tbody></Table>}
        </Panel>
      </div>

      <div className="vg-col">
        <Panel title="Top containers by memory" flush>
          {!top.length ? <Text c="dimmed" size="sm" p="md">No container memory reported.</Text> : <Table className="vg-table" verticalSpacing={7}><Table.Tbody>{top.map((c) => {
            const share = top[0].memoryBytes ? c.memoryBytes / top[0].memoryBytes * 100 : 0
            return <Table.Tr key={c.name} className={device.agentId ? 'device-row' : undefined} onClick={device.agentId ? () => onNavigate(route('containers', device.agentId!, c.name)) : undefined}>
              <Table.Td><b className="vg-ellipsis">{c.name}</b></Table.Td>
              <Table.Td w="38%"><span className="vg-bar" style={{ width: '100%' }}><i data-tone="info" style={{ width: `${share}%` }} /></span></Table.Td>
              <Table.Td ta="right" className="vg-num">{formatMetric(c.memoryBytes, 'By')}</Table.Td>
            </Table.Tr>
          })}</Table.Tbody></Table>}
        </Panel>

        <Panel title="Filesystems" count={drives.length || undefined}>
          {!drives.length ? <Text c="dimmed" size="sm">No filesystems reported.</Text> : <div className="vg-fs">{drives.map((drive) => {
            const percent = drive.used === null ? null : drive.used / drive.total * 100
            return <div key={drive.key} className="vg-fs-row">
              <span className="vg-fs-mount" title={drive.filesystem || undefined}>{drive.mount}</span>
              <MetricBar value={percent} width={90} warnAt={85} label={`${drive.mount} used`} />
              <Text size="xs" c="dimmed" className="vg-num">{drive.free === null ? '' : `${formatMetric(drive.free, 'By')} free`}</Text>
            </div>
          })}</div>}
        </Panel>

        <Panel title="About" actions={<Button size="xs" variant="subtle" onClick={() => onOpenTab('settings')}>Details</Button>}>
          <dl className="vg-kv">
            {about.map(([key, value]) => <div key={key}><dt>{key}</dt><dd>{value}</dd></div>)}
            <div><dt>Backups</dt><dd><a href="#" onClick={(e) => { e.preventDefault(); onOpenTab('backups') }}>View backups</a></dd></div>
            <div><dt>Capabilities</dt><dd className="vg-chips">{device.capabilities.length ? device.capabilities.map((c) => <span key={c} className="vg-pill">{c}</span>) : '—'}</dd></div>
          </dl>
        </Panel>
      </div>
    </div>
  </div>
}

function StatCard({ label, value, unit, detail, tone, values, max = 100 }: { label: string; value: string; unit?: string; detail?: string; tone?: 'warn' | 'crit'; values?: (number | null)[]; max?: number }) {
  const shown = values?.length ? values : null
  return <div className="vg-panel vg-stat-card" data-tone={tone}>
    <Stat label={label} value={value} unit={unit} detail={detail} />
    {shown ? <Sparkline values={shown} max={max} width={200} height={28} label={`${label} over the chart range`} /> : <span className="vg-spark-empty" style={{ height: 28 }} />}
  </div>
}
