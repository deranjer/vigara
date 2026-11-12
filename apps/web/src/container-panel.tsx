import { useCallback, useEffect, useState } from 'react'
import { ActionIcon, Alert, Button, Code, Group, Loader, Tabs, Text } from '@mantine/core'
import { IconArrowsMaximize, IconTerminal2, IconX } from '@tabler/icons-react'
import { ActionBar } from './container-actions'
import { ContainerOverview, fetchInspect, LogsPanel, StatsPanel, type InspectResult } from './container-detail'
import { imageRef, rowTone, stateText, type Usage } from './container-list-utils'
import { formatBytes, type FleetRow, type HostEntry } from './container-utils'
import { canBrowseFiles } from './mount-file-utils'
import { MountFiles } from './mount-files'
import { route as routeOf, type Route } from './route'
import { StatusText } from './status'

type Props = { hostId: string; reference: string; row: FleetRow | null; hosts: HostEntry[] | null; usage: Usage | null; role: string; onNavigate: (route: Route) => void; onClose: () => void; refreshFleet: () => void }

/**
 * A container beside the list: logs first, the main actions, and a link to the full page.
 * The list stays mounted underneath, so closing the panel returns to the same scroll position.
 */
export function ContainerPanel({ hostId, reference, row, hosts, usage, role, onNavigate, onClose, refreshFleet }: Props) {
  const [inspect, setInspect] = useState<InspectResult | null>(null)
  const [error, setError] = useState('')
  const [tab, setTab] = useState<string | null>('logs')
  const [revealing, setRevealing] = useState(false)
  const [browseMount, setBrowseMount] = useState<string | null>(null)
  const host = hosts?.find((entry) => entry.agentId === hostId)
  const load = useCallback(async (revealEnv: boolean) => {
    try { setInspect(await fetchInspect(hostId, reference, revealEnv)); setError('') } catch (cause) { setError(cause instanceof Error ? cause.message : 'The container could not be read.') }
  }, [hostId, reference])
  useEffect(() => { void load(false) }, [load])
  useEffect(() => {
    // Closed Mantine modals leave an empty root behind, so look for open content: Escape belongs to an open dialog or menu first.
    const onKey = (event: KeyboardEvent) => { if (event.key === 'Escape' && !document.querySelector('.mantine-Modal-content, .mantine-Menu-dropdown')) onClose() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  const name = inspect?.name || row?.name || reference
  const state = inspect?.state.status ?? row?.state ?? 'unknown'
  const project = inspect?.labels?.['com.docker.compose.project'] ?? row?.project
  const full = (tabName: string) => onNavigate(routeOf('containers', hostId, reference, tabName))
  // The last telemetry sample outlives a stop, so only show usage for a running container.
  const live = state === 'running' ? usage : null
  const statusRow = row ??(inspect ? { state, status: inspect.state.health === 'unhealthy' ? '(unhealthy)' : '' } : null)

  return <aside className="vg-sidepanel" aria-label={`Container ${name}`}>
    <header className="vg-sidepanel-h">
      <div className="vg-sidepanel-title">
        <h2>{name}</h2>
        <Text size="xs" c="dimmed">{[project, host?.name ?? row?.hostName].filter(Boolean).join(' · ')}</Text>
      </div>
      <ActionIcon variant="subtle" onClick={onClose} aria-label="Close panel"><IconX size={18} /></ActionIcon>
    </header>
    <div className="vg-sidepanel-actions">
      <ActionBar hostId={hostId} reference={reference} name={name} state={state} project={project} role={role} hostTier={host?.dockerTier}
        onChanged={() => { void load(false); refreshFleet() }}
        onRemoved={() => { refreshFleet(); onClose() }}
        onRecreated={(newName) => { refreshFleet(); if (newName !== reference) onNavigate(routeOf('containers', hostId, newName)); else void load(false) }}
        extra={<>
          <Button size="xs" variant="default" leftSection={<IconTerminal2 size={14} />} onClick={() => full('terminal')}>Shell</Button>
          <Button size="xs" variant="subtle" leftSection={<IconArrowsMaximize size={14} />} onClick={() => full(tab ?? 'logs')}>Open full page</Button>
        </>} />
      <Group gap="lg" mt={8} className="vg-sidepanel-facts">
        {statusRow && <StatusText tone={rowTone(statusRow)}>{stateText(statusRow)}</StatusText>}
        <span><Text span size="xs" c="dimmed">CPU </Text><b className="vg-num">{live?.cpu == null ? '—' : `${live.cpu.toFixed(1)}%`}</b></span>
        <span><Text span size="xs" c="dimmed">Memory </Text><b className="vg-num">{live?.memoryBytes == null ? '—' : formatBytes(live.memoryBytes)}</b></span>
        <Code className="vg-ellipsis" title={inspect?.image ?? row?.image}>{imageRef(inspect?.image ?? row?.image ?? '')}</Code>
      </Group>
    </div>
    {error && <Alert color="red" m="md" title="This container could not be loaded">{error} <Button variant="subtle" size="xs" onClick={() => void load(false)}>Try again</Button></Alert>}
    <Tabs value={tab} onChange={setTab} keepMounted={false} className="vg-sidepanel-tabs">
      <Tabs.List px="md"><Tabs.Tab value="logs">Logs</Tabs.Tab><Tabs.Tab value="overview">Overview</Tabs.Tab><Tabs.Tab value="stats">Stats</Tabs.Tab><Tabs.Tab value="inspect">Inspect</Tabs.Tab></Tabs.List>
      <div className="vg-sidepanel-body">
        <Tabs.Panel value="logs"><LogsPanel key={`${hostId}/${reference}`} hostId={hostId} reference={reference} name={name} height={440} compact /></Tabs.Panel>
        <Tabs.Panel value="overview">{inspect ? <ContainerOverview hostId={hostId} inspect={inspect} admin={role === 'admin'} revealing={revealing} onReveal={async (show) => { setRevealing(true); await load(show); setRevealing(false) }} onBrowse={canBrowseFiles(role, host?.dockerTier) ? setBrowseMount : undefined} /> : !error && <Loader size="sm" />}</Tabs.Panel>
        <Tabs.Panel value="stats"><StatsPanel hostId={hostId} reference={reference} running={inspect ? inspect.state.running : null} /></Tabs.Panel>
        <Tabs.Panel value="inspect">{inspect ? <Code block className="vg-inspect">{JSON.stringify(inspect, null, 2)}</Code> : !error && <Loader size="sm" />}</Tabs.Panel>
      </div>
    </Tabs>
    {browseMount !== null && <MountFiles hostId={hostId} target={{ kind: 'mount', id: reference, mount: browseMount }} label={browseMount} onClose={() => setBrowseMount(null)} />}
  </aside>
}
