import { useEffect, useMemo, useState } from 'react'
import { ActionIcon, Button, Group, Menu, Stack, Table, Tabs, Text, Title, UnstyledButton } from '@mantine/core'
import { IconChevronDown, IconDots, IconMinus, IconTerminal2, IconX } from '@tabler/icons-react'
import { BackupHistory } from './backup-history'
import { DeviceBackups } from './backup-config'
import type { ContainerInfo, HostEntry } from './container-utils'
import { DeviceMetrics } from './device-metrics'
import { DeviceOverview } from './device-overview'
import { DeviceInfo, DeviceSettings } from './device-panels'
import { getJson, statusLabel, type Device } from './device-types'
import { agentHealth, containersByStack, deviceTab, osName, shortUptime, type DeviceTab } from './device-utils'
import { deviceTone } from './overview-utils'
import { route, type Route } from './route'
import { StatusText, type StatusTone } from './status'
import { TelemetryDeliveryPanel } from './telemetry-delivery'
import { TerminalPanel } from './terminal-panel'
import { Panel } from './ui'

type Availability = { expectedAwake: boolean; maintenanceActive: boolean }
type Props = { device: Device; tab: string | null; role: string; onNavigate: (route: Route) => void; onRefresh: () => void; onDeleted: () => void; onReinstall: () => void }

/** This device's live Docker containers from GET /api/containers, refreshed every 30 seconds. */
function useHost(agentId: string | null | undefined): HostEntry | null {
  const [host, setHost] = useState<HostEntry | null>(null)
  useEffect(() => {
    setHost(null)
    if (!agentId) return
    let active = true
    const load = () => void getJson<{ hosts: HostEntry[] }>('/api/containers').then((value) => { if (active) setHost(value.hosts.find((h) => h.agentId === agentId) ?? null) }).catch(() => undefined)
    load()
    const timer = window.setInterval(load, 30_000)
    return () => { active = false; window.clearInterval(timer) }
  }, [agentId])
  return host
}

export function DevicePage({ device, tab: rawTab, role, onNavigate, onRefresh, onDeleted, onReinstall }: Props) {
  const tab = deviceTab(rawTab)
  const go = (next: DeviceTab) => onNavigate(route('devices', device.id, next === 'overview' ? null : next))
  const [availability, setAvailability] = useState<Availability | null>(null)
  const [terminal, setTerminal] = useState<'closed' | 'open' | 'minimized'>('closed')
  const health = agentHealth(device)
  const [deliveryOpen, setDeliveryOpen] = useState(health.problem)
  const host = useHost(device.agentId)
  const host_ = device.latest?.host

  // Problems open the delivery details by themselves; they stay as the user leaves them after that.
  useEffect(() => { if (health.problem) setDeliveryOpen(true) }, [health.problem])
  useEffect(() => {
    setAvailability(null)
    let active = true
    void getJson<Availability>(`/api/devices/${encodeURIComponent(device.id)}/availability`).then((value) => { if (active) setAvailability(value) }).catch(() => undefined)
    return () => { active = false }
  }, [device.id])

  const meta = [
    osName(host_, device.platform),
    host_?.cpuModel?.replace(/\(R\)|\(TM\)|CPU|with Radeon Graphics|@.*$/g, '').replace(/\s+/g, ' ').trim(),
    host_?.logicalCpus ? `${host_.logicalCpus} vCPU` : '',
    host_?.totalMemoryBytes ? `${Math.round(host_.totalMemoryBytes / 2 ** 30)} GB` : '',
    shortUptime(host_?.uptimeSeconds) ? `up ${shortUptime(host_?.uptimeSeconds)}` : '',
  ].filter(Boolean)
  const containerCount = host?.containers.length ?? device.latest?.dockerContainers?.length
  const canShell = !!device.agentId && (role === 'operator' || role === 'admin')

  return <div className="vg-page vg-device">
    <header className="vg-device-head">
      <div className="vg-device-title">
        <Group gap="sm" align="center">
          <Title order={1}>{device.name}</Title>
          <StatusText tone={deviceTone(device.status)}>{statusLabel[device.status]}</StatusText>
          {availability?.maintenanceActive && <StatusText tone="info">Maintenance</StatusText>}
          {availability && !availability.expectedAwake && <StatusText tone="idle">Expected offline</StatusText>}
        </Group>
        <Text size="sm" c="dimmed" mt={4}>{meta.join(' · ') || 'No hardware reported yet'}</Text>
      </div>
      <Group gap="xs" wrap="nowrap">
        <UnstyledButton className="vg-chip" data-tone={health.tone} onClick={() => setDeliveryOpen((v) => !v)} aria-expanded={deliveryOpen} aria-label={`${health.label}, ${health.detail}. Show upload details`}>
          <span className="vg-status-dot" data-tone={health.tone} aria-hidden="true" />
          <span><b>{health.label}</b> · {health.detail}</span>
          <IconChevronDown size={14} className="vg-chip-caret" aria-hidden="true" />
        </UnstyledButton>
        {canShell && <Button size="xs" variant={terminal === 'closed' ? 'default' : 'light'} leftSection={<IconTerminal2 size={15} />} onClick={() => setTerminal(terminal === 'open' ? 'minimized' : 'open')}>Terminal</Button>}
        <Menu position="bottom-end" withinPortal>
          <Menu.Target><ActionIcon variant="default" size={30} aria-label="More actions"><IconDots size={16} /></ActionIcon></Menu.Target>
          <Menu.Dropdown>
            <Menu.Item onClick={onRefresh}>Refresh now</Menu.Item>
            {device.agentId && <Menu.Item onClick={onReinstall}>Reinstall agent…</Menu.Item>}
            <Menu.Item onClick={() => go('settings')}>Device settings</Menu.Item>
            <Menu.Divider />
            <Menu.Item color="red" onClick={() => go('settings')}>Delete device…</Menu.Item>
          </Menu.Dropdown>
        </Menu>
      </Group>
    </header>
    {deliveryOpen && <TelemetryDeliveryPanel device={device} />}

    <Tabs value={tab} onChange={(value) => { if (value) go(value as DeviceTab) }} keepMounted={false} className="vg-page-tabs">
      <Tabs.List mb="lg">
        <Tabs.Tab value="overview">Overview</Tabs.Tab>
        <Tabs.Tab value="metrics">Metrics</Tabs.Tab>
        <Tabs.Tab value="containers" rightSection={containerCount ? <span className="vg-pill">{containerCount}</span> : undefined}>Containers</Tabs.Tab>
        <Tabs.Tab value="backups">Backups</Tabs.Tab>
        <Tabs.Tab value="settings">Settings</Tabs.Tab>
      </Tabs.List>
      <Tabs.Panel value="overview"><DeviceOverview device={device} host={host} onNavigate={onNavigate} onOpenTab={go} /></Tabs.Panel>
      <Tabs.Panel value="metrics"><DeviceMetrics device={device} /></Tabs.Panel>
      <Tabs.Panel value="containers"><DeviceContainers device={device} host={host} onNavigate={onNavigate} /></Tabs.Panel>
      <Tabs.Panel value="backups"><Stack gap="lg"><DeviceBackups key={`setup-${device.id}`} deviceId={device.id} /><BackupHistory key={device.id} deviceId={device.id} /></Stack></Tabs.Panel>
      <Tabs.Panel value="settings"><Stack gap="lg"><DeviceInfo device={device} onReinstall={onReinstall} /><DeviceSettings key={device.id} device={device} onDeleted={onDeleted} onSaved={onRefresh} /></Stack></Tabs.Panel>
    </Tabs>

    {terminal !== 'closed' && canShell && <section className="vg-dock" data-minimized={terminal === 'minimized'} aria-label={`Terminal on ${device.name}`}>
      <header>
        <IconTerminal2 size={15} aria-hidden="true" /><b>Terminal</b><Text size="xs" c="dimmed">{device.name} · stays open while you switch tabs</Text>
        <span className="vg-dock-actions">
          <ActionIcon variant="subtle" size="sm" onClick={() => setTerminal(terminal === 'open' ? 'minimized' : 'open')} aria-label={terminal === 'open' ? 'Minimize terminal' : 'Restore terminal'}>{terminal === 'open' ? <IconMinus size={15} /> : <IconChevronDown size={15} style={{ transform: 'rotate(180deg)' }} />}</ActionIcon>
          <ActionIcon variant="subtle" size="sm" onClick={() => setTerminal('closed')} aria-label="Close terminal"><IconX size={15} /></ActionIcon>
        </span>
      </header>
      {/* Minimizing hides the panel without unmounting it, so the shell stays connected. */}
      <div className="vg-dock-body" hidden={terminal === 'minimized'}>
        <TerminalPanel key={device.id} compact height={300} hostId={device.agentId ?? ''} reference={device.agentId ?? ''} name={device.name} running={null} role={role} device={{ platform: device.platform, capabilities: device.capabilities }} />
      </div>
    </section>}
  </div>
}

const containerTone = (c: ContainerInfo): StatusTone => c.state === 'restarting' || c.status.includes('(unhealthy)') ? 'warn' : c.state === 'running' ? 'ok' : 'idle'

function DeviceContainers({ device, host, onNavigate }: { device: Device; host: HostEntry | null; onNavigate: (route: Route) => void }) {
  const groups = useMemo(() => containersByStack(host?.containers ?? []), [host])
  if (!device.agentId) return <Text c="dimmed" size="sm">This device has no enrolled agent.</Text>
  if (!host) return <Text c="dimmed" size="sm">Loading containers…</Text>
  if (host.state !== 'ok') return <Text c="dimmed" size="sm">{host.message || (host.state === 'offline' ? 'The device is offline, so its containers cannot be listed.' : 'Docker is not available on this device.')}</Text>
  if (!host.containers.length) return <Text c="dimmed" size="sm">No containers on this device.</Text>
  return <Stack gap="lg">
    {groups.map((group) => <Panel key={group.project || 'standalone'} title={group.project || 'Standalone containers'} count={group.rows.length} flush
      actions={group.project ? <Button size="xs" variant="subtle" onClick={() => onNavigate(route('stacks'))}>Stacks</Button> : undefined}>
      <div className="table-scroll"><Table className="vg-table" highlightOnHover verticalSpacing={8}>
        <Table.Thead><Table.Tr><Table.Th>Container</Table.Th><Table.Th>Image</Table.Th><Table.Th>Status</Table.Th><Table.Th>Ports</Table.Th></Table.Tr></Table.Thead>
        <Table.Tbody>{group.rows.map((c) => <Table.Tr key={c.id} className="device-row" tabIndex={0} onClick={() => onNavigate(route('containers', host.agentId, c.name))} onKeyDown={(e) => { if (e.key === 'Enter') onNavigate(route('containers', host.agentId, c.name)) }} aria-label={`Open container ${c.name}`}>
          <Table.Td><span className="vg-cell-name"><span className="vg-status-dot" data-tone={containerTone(c)} aria-hidden="true" /><span><b>{c.name}</b>{c.service && <small>{c.service}</small>}</span></span></Table.Td>
          <Table.Td><Text size="sm" ff="monospace" className="vg-ellipsis" title={c.image}>{c.image}</Text></Table.Td>
          <Table.Td><Text size="sm" c={containerTone(c) === 'warn' ? 'orange' : undefined}>{c.status || c.state}</Text></Table.Td>
          <Table.Td><Text size="xs" c="dimmed" className="vg-num">{c.ports?.filter((p) => p.publicPort).map((p) => `${p.publicPort}→${p.privatePort}`).join(', ') || '—'}</Text></Table.Td>
        </Table.Tr>)}</Table.Tbody>
      </Table></div>
    </Panel>)}
    <Text size="xs" c="dimmed">Memory and CPU per container are on the Metrics tab.</Text>
  </Stack>
}
