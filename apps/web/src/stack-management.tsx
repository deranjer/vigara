import { useEffect, useMemo, useRef, useState } from 'react'
import { Alert, Badge, Button, Card, Group, Loader, Modal, Pagination, SegmentedControl, Select, SimpleGrid, Stack, Table, Text, TextInput, Title } from '@mantine/core'
import { excludedFromBulk, filterStacks, isDeployedStack, stackActionBlocker, stackActionTargets, stackRuntimeBadge, stackStatus, type StackAction, type StackHost, type StackSummary } from './stack-list-utils'
import { runStackActions } from './stack-actions'
import { StackProfiles } from './stack-profiles'
import { GitSettings } from './git-settings'
import { StackImport } from './stack-import'

async function request<T>(path: string, method = 'GET', body?: unknown): Promise<T> {
  const response = await fetch(path, { method, credentials: 'same-origin', headers: body === undefined ? undefined : { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) })
  if (!response.ok) throw new Error((await response.text()).trim().slice(0, 500) || 'The stack request failed.')
  return response.json() as Promise<T>
}
type ApprovedTarget = { id: string; projectName: string; agentId: string; deviceName: string }
type Listing = { configured: boolean; branch?: string; stacks: StackSummary[]; targets?: ApprovedTarget[] }
type Device = { agentId: string; name: string; managedRoot: string; message?: string }
const short = (commit?: string) => commit ? commit.slice(0, 8) : 'Not saved'
function statusColor(status: string) { return status === 'Deployed' ? 'green' : status === 'Deployment failed' || status === 'Needs review' ? 'red' : status === 'Deploying' ? 'blue' : 'gray' }

export function StackList({ onOpen, role }: { onOpen: (id: string) => void; role: string }) {
  const [listing, setListing] = useState<Listing | null>(null)
  const actionLock = useRef(false)
  const [busy, setBusy] = useState<string | null>(null)
  const [outcomes, setOutcomes] = useState<{ name: string; ok: boolean; message: string }[]>([])
  const [hosts, setHosts] = useState<StackHost[]>([])
  const [inventoryError, setInventoryError] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const [refresh, setRefresh] = useState(0)
  const [query, setQuery] = useState('')
  const [device, setDevice] = useState<string | null>(null)
  const [sort, setSort] = useState<string | null>('name')
  const [page, setPage] = useState(1)
  const [view, setView] = useState(() => { try { return localStorage.getItem('vigara.stack-view') === 'table' ? 'table' : 'cards' } catch { return 'cards' } })
  const [creating, setCreating] = useState(false)
  const [importing, setImporting] = useState(false)
  const [settings, setSettings] = useState(() => new URLSearchParams(window.location.search).get('github_app') === 'connected')
  const [profiles, setProfiles] = useState(false)
  useEffect(() => {
    let active = true
    setLoading(true)
    void request<Listing>('/api/stacks').then((value) => { if (active) { setListing(value); setError('') } }).catch((cause: Error) => { if (active) setError(cause.message) }).finally(() => { if (active) setLoading(false) })
    void request<{ hosts: StackHost[] }>('/api/containers').then((value) => { if (active) { setHosts(value.hosts); setInventoryError('') } }).catch(() => { if (active) { setHosts([]); setInventoryError('Live container counts are unavailable. Saved stack information is still shown.') } })
    return () => { active = false }
  }, [refresh])
  const filtered = useMemo(() => filterStacks(listing?.stacks ?? [], query, device ?? '', sort ?? 'name'), [listing, query, device, sort])
  const pages = Math.max(1, Math.ceil(filtered.length / 20))
  const currentPage = Math.min(page, pages)
  const visible = filtered.slice((currentPage - 1) * 20, currentPage * 20)
  const devices = [...new Map((listing?.stacks ?? []).filter((stack) => stack.agentId).map((stack) => [stack.agentId, { value: stack.agentId, label: stack.deviceName || stack.agentId }])).values()].sort((a, b) => a.label.localeCompare(b.label))
  useEffect(() => {
    let active = true
    const timer = window.setInterval(() => {
      if (actionLock.current) return
      void request<{ hosts: StackHost[] }>('/api/containers').then((value) => {
        if (active && !actionLock.current) { setHosts(value.hosts); setInventoryError('') }
      }).catch(() => {
        if (active && !actionLock.current) { setHosts([]); setInventoryError('Live container counts are unavailable. Saved stack information is still shown.') }
      })
    }, 10_000)
    return () => { active = false; window.clearInterval(timer) }
  }, [])
  function runtimeBadge(stack: StackSummary) {
    const badge = stackRuntimeBadge(stack, hosts)
    return <Badge color={badge.color} variant="light">{badge.label}</Badge>
  }
  async function changeStacks(action: StackAction, selected?: StackSummary) {
    if (actionLock.current) return
    actionLock.current = true
    setBusy((selected?.id ?? 'all') + ':' + action)
    setOutcomes([])
    try {
      // Refresh before choosing targets so stale inventory cannot select a recreated container.
      const [freshListing, inventory] = await Promise.all([request<Listing>('/api/stacks'), request<{ hosts: StackHost[] }>('/api/containers')])
      setListing(freshListing); setHosts(inventory.hosts); setInventoryError('')
      const results = await runStackActions(freshListing.stacks, inventory.hosts, role, action, selected?.id)
      setOutcomes(results)
    } catch (cause) { setOutcomes([{ name: 'Stack action', ok: false, message: cause instanceof Error ? cause.message : 'The stack action failed.' }]) }
    finally { actionLock.current = false; setBusy(null); setRefresh((value) => value + 1) }
  }
  function controls(stack: StackSummary) {
    const blocker = stackActionBlocker(stack, hosts, role)
    return <Group gap="xs"><Button size="xs" variant="light" color="green" title={blocker ?? 'Start existing stack containers'} aria-label={'Start ' + (stack.name || stack.id)} loading={busy === stack.id + ':start'} disabled={!!busy || loading || !!blocker || !stackActionTargets(stack, hosts, 'start').length} onClick={() => void changeStacks('start', stack)}>Start</Button><Button size="xs" variant="light" color="red" title={blocker ?? 'Stop stack containers'} aria-label={'Stop ' + (stack.name || stack.id)} loading={busy === stack.id + ':stop'} disabled={!!busy || loading || !!blocker || !stackActionTargets(stack, hosts, 'stop').length} onClick={() => void changeStacks('stop', stack)}>Stop</Button><Button size="xs" variant="default" onClick={() => onOpen(stack.id)}>Open stack</Button></Group>
  }
  const bulkStacks = (listing?.stacks ?? []).filter((stack) => isDeployedStack(stack, hosts) && !excludedFromBulk(stack))
  const empty = listing?.stacks.length ? 'No stacks match your search and device filter.' : 'Create your first stack to start editing and deploying from Vigara.'
  return <section className="devices-page"><Stack gap="lg">
    <Group justify="space-between" align="start"><div><Text size="sm" c="dimmed" tt="uppercase">Docker Stacks</Text><Title order={1}>Compose stacks</Title><Text c="dimmed" mt="xs">Your applications, devices and deployment status in one place.</Text></div><Group><Button variant="default" onClick={() => setSettings(true)}>Git settings</Button><Button variant="default" loading={loading} onClick={() => setRefresh((value) => value + 1)}>Refresh</Button><Button variant="default" disabled={!listing?.configured || loading} onClick={() => setImporting(true)}>Import existing stack</Button><Button disabled={!listing?.configured || loading} onClick={() => setCreating(true)}>New stack</Button></Group></Group>
    <Group><Button variant="default" disabled={!listing?.configured || loading} onClick={() => setProfiles(true)}>Profiles and assignments</Button><Button color="green" variant="light" loading={busy === 'all:start'} disabled={!!busy || loading || !bulkStacks.length} onClick={() => void changeStacks('start')}>Start all stacks</Button><Button color="red" variant="light" loading={busy === 'all:stop'} disabled={!!busy || loading || !bulkStacks.length} onClick={() => void changeStacks('stop')}>Stop all stacks</Button><Text size="xs" c="dimmed">All deployed stacks across devices, including outside these filters. Excludes vigara and vigara-caddy.</Text></Group>
    {busy && <Text role="status" size="sm">Changing stack containers…</Text>}
    {!!outcomes.length && <Alert color={outcomes.some((outcome) => !outcome.ok) ? 'red' : 'green'} title="Stack action results" withCloseButton onClose={() => setOutcomes([])}><Stack gap="xs">{outcomes.map((outcome, index) => <Text key={index} size="sm"><Text span fw={600}>{outcome.name}:</Text> {outcome.message}</Text>)}</Stack></Alert>}
    {error && <Alert color="red" title="Could not load stacks">{error}</Alert>}
    {inventoryError && <Alert color="yellow">{inventoryError}</Alert>}
    {!listing && loading && <Loader />}
    {listing && !listing.configured && <Card withBorder padding="lg"><Stack><Title order={3}>Connect your stack repository</Title><Text>Choose a Git repository and branch to keep a history of your Compose files.</Text><Group><Button onClick={() => setSettings(true)}>Open Git settings</Button></Group></Stack></Card>}
    {listing?.configured && <>
      {!listing.stacks.length && <Alert color="blue" title="Already running Vigara, Caddy, or other Compose projects?"><Text size="sm">Import them from your enrolled host to add their configuration to Git without restarting containers.</Text><Button mt="sm" variant="light" onClick={() => setImporting(true)}>Find existing stacks</Button></Alert>}
      <Group justify="space-between"><Group><Badge variant="light">Branch: {listing.branch}</Badge><Text size="sm" c="dimmed">Save commits to Git. Deploy applies a saved revision.</Text></Group><Text size="sm" c="dimmed">{listing.stacks.length} stacks</Text></Group>
      <Group align="end"><TextInput className="stack-search" label="Search stacks" placeholder="Name, project, device or status" value={query} onChange={(event) => { setQuery(event.currentTarget.value); setPage(1) }} /><Select label="Device" placeholder="All devices" clearable searchable data={devices} value={device} onChange={(value) => { setDevice(value); setPage(1) }} /><Select label="Sort by" data={[{ value: 'name', label: 'Stack name' }, { value: 'device', label: 'Device' }, { value: 'status', label: 'Deployment status' }]} value={sort} onChange={setSort} /><SegmentedControl aria-label="Stack layout" value={view} data={[{ value: 'cards', label: 'Cards' }, { value: 'table', label: 'Table' }]} onChange={(value) => { setView(value); try { localStorage.setItem('vigara.stack-view', value) } catch { /* View preference is optional. */ } }} />{(query || device) && <Button variant="subtle" onClick={() => { setQuery(''); setDevice(null); setPage(1) }}>Clear filters</Button>}</Group>
      {!filtered.length ? <Card withBorder padding="lg"><Text c="dimmed">{empty}</Text></Card> : view === 'cards' ? <SimpleGrid cols={{ base: 1, sm: 2, xl: 3 }}>{visible.map((stack) => {
        const status = stackStatus(stack)
        return <Card key={stack.id} withBorder radius="md" padding="lg" className="stack-summary-card"><Stack gap="sm"><Group justify="space-between" align="start" wrap="nowrap"><div className="stack-card-name"><Text fw={600}>{stack.name || stack.id}</Text><Text size="xs" c="dimmed">{stack.id}</Text></div><Badge color={statusColor(status)}>{status}</Badge></Group><Text size="sm">Device: <Text span fw={500}>{stack.deviceName || (stack.assigned ? stack.agentId : 'Unassigned')}</Text></Text><SimpleGrid cols={2}><div><Text size="xs" c="dimmed">Containers</Text>{runtimeBadge(stack)}</div><div><Text size="xs" c="dimmed">Saved services</Text><Text fw={500} size="sm">{stack.commit ? stack.serviceCount : 'Not saved'}</Text></div></SimpleGrid><Text size="sm" c="dimmed">Project: {stack.projectName}</Text><Text size="xs" c="dimmed">Saved {short(stack.commit)} · Last attempt {short(stack.deploymentCommit)}</Text>{stack.deploymentStatus && <Text size="xs" c="dimmed">Last attempt: {stack.deploymentStatus}{stack.deploymentAt && !stack.deploymentAt.startsWith('0001') ? ' · ' + new Date(stack.deploymentAt).toLocaleString() : ''}</Text>}{stack.draft && <Text size="xs" c="orange">You have a saved draft</Text>}<div style={{ marginTop: 'auto' }}>{controls(stack)}</div></Stack></Card>
      })}</SimpleGrid> : <Card withBorder padding={0} className="inventory-card stack-table"><Table><Table.Thead><Table.Tr><Table.Th>Stack</Table.Th><Table.Th>Device</Table.Th><Table.Th>Containers</Table.Th><Table.Th>Deployment</Table.Th><Table.Th>Saved revision</Table.Th><Table.Th><Text size="sm">Actions</Text></Table.Th></Table.Tr></Table.Thead><Table.Tbody>{visible.map((stack) => <Table.Tr key={stack.id}><Table.Td><Text fw={600} size="sm">{stack.name || stack.id}</Text><Text size="xs" c="dimmed">{stack.projectName}</Text>{stack.draft && <Text size="xs" c="orange">Saved draft</Text>}</Table.Td><Table.Td>{stack.deviceName || stack.agentId || 'Unassigned'}</Table.Td><Table.Td>{runtimeBadge(stack)}<Text size="xs" c="dimmed">{stack.serviceCount} saved services</Text></Table.Td><Table.Td><Badge color={statusColor(stackStatus(stack))}>{stackStatus(stack)}</Badge><Text size="xs" c="dimmed" mt={4}>{short(stack.deploymentCommit)}</Text></Table.Td><Table.Td>{short(stack.commit)}</Table.Td><Table.Td>{controls(stack)}</Table.Td></Table.Tr>)}</Table.Tbody></Table></Card>}
      <Group justify="space-between"><Text size="sm" c="dimmed">{filtered.length ? (currentPage - 1) * 20 + 1 : 0}–{Math.min(currentPage * 20, filtered.length)} of {filtered.length}{filtered.length !== listing.stacks.length ? ' matching stacks' : ' stacks'}</Text>{pages > 1 && <Pagination total={pages} value={currentPage} onChange={setPage} />}</Group><Text size="xs" c="dimmed">Container counts refresh every 10 seconds. Start/stop changes existing containers; deploy to create missing ones. Offline devices show Unavailable. Deployment status reflects Vigara’s last recorded attempt.</Text>
    </>}
    <Modal opened={creating} onClose={() => setCreating(false)} title="New Compose stack" size="lg" centered>{creating && <NewStack approved={listing?.targets?.filter((target) => !listing.stacks.some((stack) => stack.id === target.id)) ?? []} onOpen={onOpen} />}</Modal>
    <Modal opened={settings} onClose={() => setSettings(false)} title="Git settings" size="lg" centered>{settings && <GitSettings onSaved={() => { setSettings(false); setRefresh((value) => value + 1); if (!listing?.configured) setImporting(true) }} />}</Modal>
    <Modal opened={importing} onClose={() => {}} withCloseButton={false} closeOnEscape={false} closeOnClickOutside={false} title="Import existing stack" size="xl" centered>{importing && <StackImport onClose={() => { setImporting(false); setRefresh((value) => value + 1) }} onImported={(id) => { setImporting(false); setRefresh((value) => value + 1); onOpen(id) }} />}</Modal>
    <Modal opened={profiles} onClose={() => {}} withCloseButton={false} closeOnEscape={false} closeOnClickOutside={false} title="Server profiles and assignments" size="xl" centered>{profiles && <StackProfiles stacks={listing?.stacks ?? []} onClose={() => setProfiles(false)} />}</Modal>
  </Stack></section>
}

function NewStack({ approved, onOpen }: { approved: ApprovedTarget[]; onOpen: (id: string) => void }) {
  const [devices, setDevices] = useState<Device[] | null>(null)
  const [name, setName] = useState('')
  const [id, setId] = useState('')
  const [project, setProject] = useState('')
  const [agent, setAgent] = useState<string | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [approvedId, setApprovedId] = useState<string | null>(null)
  useEffect(() => { let active = true; void request<{ devices: Device[] }>('/api/stacks/setup').then((value) => { if (active) setDevices(value.devices) }).catch((cause: Error) => { if (active) setError(cause.message) }); return () => { active = false } }, [])
  const selected = devices?.find((device) => device.agentId === agent)
  const valid = /^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(id) && id.length <= 63 && /^[a-z0-9][a-z0-9_-]{0,62}$/.test(project) && !!name.trim() && !!selected?.managedRoot
  return <Stack>{error && <Alert color="red">{error}</Alert>}<Text size="sm" c="dimmed">Create a draft, choose its device, then edit Compose before saving or deploying. Creating a stack starts no containers.</Text><TextInput label="Stack name" placeholder="Home media" maxLength={120} value={name} disabled={busy} onChange={(event) => { const next = event.currentTarget.value; const slug = next.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '').slice(0, 63); const previousSlug = name.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '').slice(0, 63); if (!id || id === previousSlug) setId(slug); if (!project || project === previousSlug) setProject(slug); setName(next) }} /><Group grow><TextInput label="Stack ID" description="Lowercase letters, numbers and hyphens" value={id} disabled={busy} onChange={(event) => setId(event.currentTarget.value)} /><TextInput label="Compose project name" description="Must be unused on the selected device" value={project} disabled={busy} onChange={(event) => setProject(event.currentTarget.value)} /></Group>{!devices && !error && <Loader size="sm" />}<Select label="Device" placeholder="Choose a device" searchable value={agent} disabled={busy} onChange={setAgent} data={(devices ?? []).map((device) => ({ value: device.agentId, label: device.name + (device.managedRoot ? '' : ' — unavailable'), disabled: !device.managedRoot }))} />{devices?.filter((device) => device.message).map((device) => <Text size="xs" c="dimmed" key={device.agentId}>{device.name}: {device.message}</Text>)}<TextInput label="Device folder" description="Vigara uses a new folder inside this device’s managed stack directory." readOnly value={selected?.managedRoot ? selected.managedRoot.replace(/\/$/, '') + '/' + id : ''} /><Button loading={busy} disabled={!valid} onClick={() => { setBusy(true); setError(''); void request<{ id: string }>('/api/stacks', 'POST', { id, name, projectName: project, agentId: agent }).then((result) => onOpen(result.id)).catch((cause: Error) => setError(cause.message)).finally(() => setBusy(false)) }}>Create draft</Button>{!!approved.length && <><Text size="sm" fw={500} mt="sm">Or use an existing approved target</Text><Select label="Approved target" value={approvedId} onChange={setApprovedId} data={approved.map((target) => ({ value: target.id, label: target.id + ' · ' + (target.deviceName || target.agentId) }))} /><Button variant="default" disabled={!approvedId || busy} onClick={() => { if (approvedId) onOpen(approvedId) }}>Open approved target</Button></>}</Stack>
}
