import { useEffect, useMemo, useState } from 'react'
import { Alert, Badge, Button, Card, Group, Loader, Modal, Pagination, SegmentedControl, Select, SimpleGrid, Stack, Table, Text, TextInput, Title } from '@mantine/core'
import { filterStacks, stackContainers, stackStatus, type StackHost, type StackSummary } from './stack-list-utils'
import { StackProfiles } from './stack-profiles'

async function request<T>(path: string, method = 'GET', body?: unknown): Promise<T> {
  const response = await fetch(path, { method, credentials: 'same-origin', headers: body === undefined ? undefined : { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) })
  if (!response.ok) throw new Error((await response.text()).trim().slice(0, 500) || 'The stack request failed.')
  return response.json() as Promise<T>
}
type ApprovedTarget = { id: string; projectName: string; agentId: string; deviceName: string }
type Listing = { configured: boolean; branch?: string; stacks: StackSummary[]; targets?: ApprovedTarget[] }
type Device = { agentId: string; name: string; managedRoot: string; message?: string }
type GitSettings = { remote: string; branch: string; version: number; authentication: string; credentialHelp: string }
const short = (commit?: string) => commit ? commit.slice(0, 8) : 'Not saved'
function statusColor(status: string) { return status === 'Deployed' ? 'green' : status === 'Deployment failed' || status === 'Needs review' ? 'red' : status === 'Deploying' ? 'blue' : 'gray' }

export function StackList({ onOpen }: { onOpen: (id: string) => void }) {
  const [listing, setListing] = useState<Listing | null>(null)
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
  const [settings, setSettings] = useState(false)
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
  function counts(stack: StackSummary) {
    const count = stackContainers(stack, hosts)
    return count.known ? (count.partial ? 'At least ' : '') + count.running + ' / ' + count.total + ' running' : 'Unavailable'
  }
  const empty = listing?.stacks.length ? 'No stacks match your search and device filter.' : 'Create your first stack to start editing and deploying from Vigara.'
  return <section className="devices-page"><Stack gap="lg">
    <Group justify="space-between" align="start"><div><Text size="sm" c="dimmed" tt="uppercase">Docker Stacks</Text><Title order={1}>Compose stacks</Title><Text c="dimmed" mt="xs">Your applications, devices and deployment status in one place.</Text></div><Group><Button variant="default" onClick={() => setSettings(true)}>Git settings</Button><Button variant="default" loading={loading} onClick={() => setRefresh((value) => value + 1)}>Refresh</Button><Button disabled={!listing?.configured || loading} onClick={() => setCreating(true)}>New stack</Button></Group></Group>
    <Group><Button variant="default" disabled={!listing?.configured || loading} onClick={() => setProfiles(true)}>Profiles and assignments</Button></Group>
    {error && <Alert color="red" title="Could not load stacks">{error}</Alert>}
    {inventoryError && <Alert color="yellow">{inventoryError}</Alert>}
    {!listing && loading && <Loader />}
    {listing && !listing.configured && <Card withBorder padding="lg"><Stack><Title order={3}>Connect your stack repository</Title><Text>Choose a Git repository and branch to keep a history of your Compose files.</Text><Group><Button onClick={() => setSettings(true)}>Open Git settings</Button></Group></Stack></Card>}
    {listing?.configured && <>
      <Group justify="space-between"><Group><Badge variant="light">Branch: {listing.branch}</Badge><Text size="sm" c="dimmed">Save commits to Git. Deploy applies a saved revision.</Text></Group><Text size="sm" c="dimmed">{listing.stacks.length} stacks</Text></Group>
      <Group align="end"><TextInput className="stack-search" label="Search stacks" placeholder="Name, project, device or status" value={query} onChange={(event) => { setQuery(event.currentTarget.value); setPage(1) }} /><Select label="Device" placeholder="All devices" clearable searchable data={devices} value={device} onChange={(value) => { setDevice(value); setPage(1) }} /><Select label="Sort by" data={[{ value: 'name', label: 'Stack name' }, { value: 'device', label: 'Device' }, { value: 'status', label: 'Deployment status' }]} value={sort} onChange={setSort} /><SegmentedControl aria-label="Stack layout" value={view} data={[{ value: 'cards', label: 'Cards' }, { value: 'table', label: 'Table' }]} onChange={(value) => { setView(value); try { localStorage.setItem('vigara.stack-view', value) } catch { /* View preference is optional. */ } }} />{(query || device) && <Button variant="subtle" onClick={() => { setQuery(''); setDevice(null); setPage(1) }}>Clear filters</Button>}</Group>
      {!filtered.length ? <Card withBorder padding="lg"><Text c="dimmed">{empty}</Text></Card> : view === 'cards' ? <SimpleGrid cols={{ base: 1, sm: 2, xl: 3 }}>{visible.map((stack) => {
        const status = stackStatus(stack)
        return <Card key={stack.id} withBorder radius="md" padding="lg" className="stack-summary-card"><Stack gap="sm"><Group justify="space-between" align="start" wrap="nowrap"><div className="stack-card-name"><Text fw={600}>{stack.name || stack.id}</Text><Text size="xs" c="dimmed">{stack.id}</Text></div><Badge color={statusColor(status)}>{status}</Badge></Group><Text size="sm">Device: <Text span fw={500}>{stack.deviceName || (stack.assigned ? stack.agentId : 'Unassigned')}</Text></Text><SimpleGrid cols={2}><div><Text size="xs" c="dimmed">Containers</Text><Text fw={500} size="sm">{counts(stack)}</Text></div><div><Text size="xs" c="dimmed">Saved services</Text><Text fw={500} size="sm">{stack.commit ? stack.serviceCount : 'Not saved'}</Text></div></SimpleGrid><Text size="sm" c="dimmed">Project: {stack.projectName}</Text><Text size="xs" c="dimmed">Saved {short(stack.commit)} · Last attempt {short(stack.deploymentCommit)}</Text>{stack.deploymentStatus && <Text size="xs" c="dimmed">Last attempt: {stack.deploymentStatus}{stack.deploymentAt && !stack.deploymentAt.startsWith('0001') ? ' · ' + new Date(stack.deploymentAt).toLocaleString() : ''}</Text>}{stack.draft && <Text size="xs" c="orange">You have a saved draft</Text>}<Button variant="light" mt="auto" onClick={() => onOpen(stack.id)}>Open stack</Button></Stack></Card>
      })}</SimpleGrid> : <Card withBorder padding={0} className="inventory-card stack-table"><Table><Table.Thead><Table.Tr><Table.Th>Stack</Table.Th><Table.Th>Device</Table.Th><Table.Th>Containers</Table.Th><Table.Th>Deployment</Table.Th><Table.Th>Saved revision</Table.Th><Table.Th><Text size="sm">Open</Text></Table.Th></Table.Tr></Table.Thead><Table.Tbody>{visible.map((stack) => <Table.Tr key={stack.id}><Table.Td><Text fw={600} size="sm">{stack.name || stack.id}</Text><Text size="xs" c="dimmed">{stack.projectName}</Text>{stack.draft && <Text size="xs" c="orange">Saved draft</Text>}</Table.Td><Table.Td>{stack.deviceName || stack.agentId || 'Unassigned'}</Table.Td><Table.Td><Text size="sm">{counts(stack)}</Text><Text size="xs" c="dimmed">{stack.serviceCount} saved services</Text></Table.Td><Table.Td><Badge color={statusColor(stackStatus(stack))}>{stackStatus(stack)}</Badge><Text size="xs" c="dimmed" mt={4}>{short(stack.deploymentCommit)}</Text></Table.Td><Table.Td>{short(stack.commit)}</Table.Td><Table.Td><Button size="xs" variant="light" onClick={() => onOpen(stack.id)}>Open stack</Button></Table.Td></Table.Tr>)}</Table.Tbody></Table></Card>}
      <Group justify="space-between"><Text size="sm" c="dimmed">{filtered.length ? (currentPage - 1) * 20 + 1 : 0}–{Math.min(currentPage * 20, filtered.length)} of {filtered.length}{filtered.length !== listing.stacks.length ? ' matching stacks' : ' stacks'}</Text>{pages > 1 && <Pagination total={pages} value={currentPage} onChange={setPage} />}</Group><Text size="xs" c="dimmed">Container counts are live observations from Refresh. Offline devices show Unavailable. Deployment status reflects Vigara’s last recorded attempt.</Text>
    </>}
    <Modal opened={creating} onClose={() => setCreating(false)} title="New Compose stack" size="lg" centered>{creating && <NewStack approved={listing?.targets?.filter((target) => !listing.stacks.some((stack) => stack.id === target.id)) ?? []} onOpen={onOpen} />}</Modal>
    <Modal opened={settings} onClose={() => setSettings(false)} title="Git settings" size="lg" centered>{settings && <GitSettings onSaved={() => { setSettings(false); setRefresh((value) => value + 1) }} />}</Modal>
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

function GitSettings({ onSaved }: { onSaved: () => void }) {
  const [value, setValue] = useState<GitSettings | null>(null)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [busy, setBusy] = useState(false)
  useEffect(() => { let active = true; void request<GitSettings>('/api/stacks/settings').then((value) => { if (active) setValue(value) }).catch((cause: Error) => { if (active) setError(cause.message) }); return () => { active = false } }, [])
  async function submit(save: boolean) {
    if (!value) return
    setBusy(true); setError(''); setNotice('')
    try { const result = await request<{ message: string }>(save ? '/api/stacks/settings' : '/api/stacks/settings/test', save ? 'PUT' : 'POST', { remote: value.remote, branch: value.branch, version: value.version }); if (save) onSaved(); else setNotice(result.message) } catch (cause) { setError(cause instanceof Error ? cause.message : 'Could not update Git settings.') } finally { setBusy(false) }
  }
  return <Stack>{error && <Alert color="red">{error}</Alert>}{notice && <Alert color="green">{notice}</Alert>}{!value && !error && <Loader />}{value && <><Text size="sm">All stacks use this repository and branch. Each Save creates a commit and pushes it; deployment remains a separate action.</Text><TextInput label="Repository URL" placeholder="git@example.com:home/stacks.git" description="SSH or HTTPS. Keep passwords and tokens out of this field." value={value.remote} disabled={busy} onChange={(event) => { setValue({ ...value, remote: event.currentTarget.value }); setNotice('') }} /><TextInput label="Branch" value={value.branch} disabled={busy} onChange={(event) => { setValue({ ...value, branch: event.currentTarget.value }); setNotice('') }} /><Card withBorder padding="sm"><Text fw={500} size="sm">Authentication: {value.authentication}</Text><Text size="sm" c="dimmed" mt="xs">{value.credentialHelp}</Text></Card><Text size="sm" c="dimmed">The repository needs an existing branch and at least one commit. Changing repositories requires finishing or discarding stored drafts and resolving active deployments first. Existing stacks and repositories are not moved automatically.</Text><Group justify="end"><Button variant="default" loading={busy} disabled={!value.remote || !value.branch} onClick={() => void submit(false)}>Test connection</Button><Button loading={busy} disabled={!value.remote || !value.branch} onClick={() => void submit(true)}>Save settings</Button></Group></>}</Stack>
}
