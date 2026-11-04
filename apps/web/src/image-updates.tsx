import { useEffect, useState } from 'react'
import { Alert, Badge, Button, Card, Checkbox, Code, Group, Modal, MultiSelect, NumberInput, Select, Stack, Table, Tabs, Text, TextInput, Title } from '@mantine/core'
import { route as routeOf, type Route } from './route'
import { defaultImageUpdateFilters, selectImageUpdates, type ImageUpdateSort } from './image-update-list'

type Window = { days: number[]; start: string; minutes: number; timezone: string }
type Settings = { intervalMinutes: number; maintenance: Window; version: number }
type Result = { runningImage?: string; name: string; image: string; status: string; message?: string; current?: string; available?: string; platform?: string }
type Item = { trackingTag: string; agentId: string; device: string; containerId: string; stackId: string; result: Result; checkedAt: string; lastSuccessAt: string | null; lastSuccess: Result | null; ignored: boolean }
type Host = { agentId: string; device: string; checkedAt: string | null; nextCheck: string; message: string }
type Policy = { tracking?: Record<string, string>; stackId: string; enabled: boolean; maintenance: Window | null; excludedServices: string[]; services: string[]; configuration: string; pausedReason: string; version: number }
const defaultWindow: Window = { days: [0], start: '03:00', minutes: 120, timezone: 'UTC' }
const days = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday']
const when = (date: string | null) => date ? new Date(date).toLocaleString() : 'Never'
const description = (w: Window) => `${w.days.map(d => days[d]).join(', ')} at ${w.start}, ${w.minutes} minutes (${w.timezone})`
async function request<T>(url: string, method = 'GET', body?: unknown): Promise<T> {
  const response = await fetch(url, { method, credentials: 'same-origin', headers: body === undefined ? undefined : { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) })
  if (!response.ok) throw new Error((await response.text()).trim().slice(0, 400) || 'Image update request failed.')
  return response.json() as Promise<T>
}
function WindowFields({ value, onChange, disabled }: { value: Window; onChange: (value: Window) => void; disabled?: boolean }) {
  return <Stack gap="sm">
    <MultiSelect label="Maintenance days" data={days.map((label, i) => ({ value: String(i), label }))} value={value.days.map(String)} onChange={v => onChange({ ...value, days: v.map(Number) })} disabled={disabled} />
    <Group grow align="start"><TextInput label="Start time" type="time" value={value.start} onChange={e => onChange({ ...value, start: e.currentTarget.value })} disabled={disabled} /><NumberInput label="Duration (minutes)" min={15} max={1440} value={value.minutes} onChange={v => onChange({ ...value, minutes: Number(v) })} disabled={disabled} /></Group>
    <TextInput label="Timezone" description="Use a timezone such as America/New_York or UTC. Windows may cross midnight." value={value.timezone} onChange={e => onChange({ ...value, timezone: e.currentTarget.value })} disabled={disabled} />
  </Stack>
}

export function ImageUpdates({ role, onNavigate }: { role: string; onNavigate: (route: Route) => void }) {
  const [items, setItems] = useState<Item[]>([])
  const [activeTab, setActiveTab] = useState<string | null>('updates')
  const [filters, setFilters] = useState({ ...defaultImageUpdateFilters })
  const [trackingItem, setTrackingItem] = useState<Item | null>(null)
  const [trackingTag, setTrackingTag] = useState('')
  const [hosts, setHosts] = useState<Host[]>([])
  const [settings, setSettings] = useState<Settings | null>(null)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [busy, setBusy] = useState(false)
  const [loading, setLoading] = useState(true)
  const canManage = role === 'admin' || role === 'operator'
  useEffect(() => {
    let active = true
    const load = () => request<{ items: Item[]; hosts: Host[] }>('/api/image-updates').then(v => { if (active) { setItems(v.items); setHosts(v.hosts) } }).catch(e => { if (active) setError(String(e.message)) }).finally(() => { if (active) setLoading(false) })
    void load()
    void request<Settings>('/api/image-updates/settings').then(v => { if (active) setSettings(v) }).catch(e => { if (active) setError(String(e.message)) })
    const timer = window.setInterval(() => { void load() }, 15000)
    return () => { active = false; window.clearInterval(timer) }
  }, [])
  async function act(work: () => Promise<unknown>, message: string) {
    setBusy(true); setError(''); setNotice('')
    try { await work(); setNotice(message) } catch (e) { setError(e instanceof Error ? e.message : 'Request failed.') } finally { setBusy(false) }
  }
  const visibleItems = selectImageUpdates(items, filters)
  const devices = Array.from(new Map([...hosts.map(h => [h.agentId, h.device] as const), ...items.map(i => [i.agentId, i.device] as const)]).entries()).map(([value, label]) => ({ value, label })).sort((a, b) => a.label.localeCompare(b.label))
  const hostErrors = new Set(hosts.filter(h => h.message).map(h => h.agentId))
  return <Stack>
    <Group justify="space-between"><div><Title order={2}>Image updates</Title><Text c="dimmed">All containers are monitored. Automatic updates require an explicit policy on a Vigara-managed stack.</Text></div>{canManage && activeTab === 'updates' && <Button loading={busy} onClick={() => void act(() => request('/api/image-updates/check', 'POST', {}), 'Image checks queued. Results will refresh here.')}>Check all now</Button>}</Group>
    {error && <Alert color="red">{error}</Alert>}{notice && <Alert color="blue">{notice}</Alert>}

    <Tabs value={activeTab} onChange={setActiveTab} keepMounted={false}>
      <Tabs.List mb="lg"><Tabs.Tab value="updates">Image updates</Tabs.Tab><Tabs.Tab value="schedule">Global schedule</Tabs.Tab></Tabs.List>
      <Tabs.Panel value="updates"><Stack>
        <Card withBorder><Stack gap="sm">
          <TextInput label="Search containers and images" placeholder="Container, image, device, stack, or platform" value={filters.search} onChange={e => setFilters({ ...filters, search: e.currentTarget.value })} />
          <Group grow align="end">
            <Select label="Device" placeholder="All devices" clearable searchable data={devices} value={filters.device || null} onChange={device => setFilters({ ...filters, device: device ?? '' })} />
            <Select label="Status" placeholder="All statuses" clearable data={[{ value: 'available', label: 'Image changed' }, { value: 'current', label: 'Current' }, { value: 'pinned', label: 'Needs tracking tag' }, { value: 'unknown', label: 'Unable to check' }, { value: 'ignored', label: 'Ignored' }]} value={filters.status || null} onChange={status => setFilters({ ...filters, status: status ?? '' })} />
            <Select label="Management" placeholder="All containers" clearable data={[{ value: 'managed', label: 'Managed stacks' }, { value: 'external', label: 'External containers' }]} value={filters.scope || null} onChange={scope => setFilters({ ...filters, scope: scope ?? '' })} />
          </Group>
          <Group justify="space-between" align="end">
            <Group align="end"><Select label="Sort by" allowDeselect={false} data={[{ value: 'container', label: 'Container' }, { value: 'device', label: 'Device' }, { value: 'image', label: 'Image' }, { value: 'status', label: 'Status' }, { value: 'checked', label: 'Last checked' }]} value={filters.sort} onChange={sort => setFilters({ ...filters, sort: (sort ?? 'container') as ImageUpdateSort })} /><Button variant="light" onClick={() => setFilters({ ...filters, descending: !filters.descending })}>{filters.descending ? 'Descending ↓' : 'Ascending ↑'}</Button></Group>
            <Group><Text size="sm" c="dimmed">{visibleItems.length} of {items.length} containers</Text><Button variant="subtle" onClick={() => setFilters({ ...defaultImageUpdateFilters })}>Reset filters</Button></Group>
          </Group>
        </Stack></Card>
    <Card withBorder><Table.ScrollContainer minWidth={850}><Table><Table.Thead><Table.Tr><Table.Th>Container</Table.Th><Table.Th>Tracking</Table.Th><Table.Th>Status</Table.Th><Table.Th>Checked</Table.Th><Table.Th>Actions</Table.Th></Table.Tr></Table.Thead><Table.Tbody>
      {visibleItems.map(item => <Table.Tr key={`${item.agentId}/${item.containerId}`}><Table.Td><Button variant="subtle" size="compact-sm" onClick={() => onNavigate(routeOf('containers', item.agentId, item.containerId))}>{item.result.name || item.containerId.slice(0, 12)}</Button><Text size="xs" c="dimmed">{item.device}</Text></Table.Td><Table.Td><Code>{item.result.image}</Code>{item.result.runningImage && item.result.runningImage !== item.result.image && <Text size="xs" c="dimmed">Running: <Code>{item.result.runningImage}</Code></Text>}<Text size="xs" c="dimmed">{item.result.platform}</Text></Table.Td><Table.Td><Badge color={item.ignored ? 'gray' : item.result.status === 'available' ? 'orange' : item.result.status === 'current' ? 'green' : 'gray'}>{item.ignored ? 'Ignored' : item.result.status === 'available' ? 'Image changed' : item.result.status === 'current' ? 'Current' : item.result.status === 'pinned' ? 'Needs tracking tag' : 'Unable to check'}</Badge>{hostErrors.has(item.agentId) && <Text size="xs" c="orange">Check incomplete; result may be stale</Text>}<Text size="xs">{item.result.message}</Text>{item.result.status === 'unknown' && item.lastSuccess && <Text size="xs" c="dimmed">Last success: {when(item.lastSuccessAt)} ({item.lastSuccess.status})</Text>}</Table.Td><Table.Td><Text size="xs">{when(item.checkedAt)}</Text></Table.Td><Table.Td><Group gap="xs">{canManage && <Button size="compact-xs" variant="light" disabled={busy} onClick={() => { setTrackingItem(item); setTrackingTag(item.trackingTag ?? ''); setError('') }}>Set monitoring tag</Button>}{item.stackId && role === 'admin' && <Button size="compact-xs" variant="light" onClick={() => onNavigate(routeOf('stacks', item.stackId))}>Stack policy</Button>}{canManage && <Button size="compact-xs" variant="subtle" disabled={busy} onClick={() => void act(async () => { await request(`/api/image-updates/containers/${encodeURIComponent(item.agentId)}/${encodeURIComponent(item.containerId)}`, 'PUT', { ignored: !item.ignored }); setItems(v => v.map(i => i === item ? { ...i, ignored: !i.ignored } : i)) }, item.ignored ? 'Monitoring resumed.' : 'Container ignored. Managed stack exclusions are configured separately.')}>{item.ignored ? 'Monitor' : 'Ignore'}</Button>}</Group></Table.Td></Table.Tr>)}
    </Table.Tbody></Table></Table.ScrollContainer>{!visibleItems.length && <Text c="dimmed" p="md">{items.length ? 'No containers match these filters.' : loading ? 'Loading image checks…' : 'No image checks yet. Connected Docker agents will be checked automatically.'}</Text>}</Card>
        {!!hosts.length && <details><summary>Device check details ({hosts.length})</summary><Stack mt="sm">
    {hosts.map(h => <Card key={h.agentId} withBorder><Group justify="space-between"><div><Text fw={600}>{h.device}</Text><Text size="sm" c="dimmed">Last attempt: {when(h.checkedAt)} · Next check: {when(h.nextCheck)}</Text>{h.message && <Text size="sm" c="orange">{h.message} Existing results may be stale.</Text>}</div>{canManage && <Button variant="light" disabled={busy} onClick={() => void act(() => request('/api/image-updates/check', 'POST', { agentId: h.agentId }), `Check queued for ${h.device}.`)}>Check now</Button>}</Group></Card>)}
        </Stack></details>}
      </Stack></Tabs.Panel>
      <Tabs.Panel value="schedule">
    {settings && <Card withBorder><Stack><Title order={3}>Global schedule</Title><Text size="sm">Checks run independently of maintenance windows. Automatic updates are disabled by default. Missed windows wait until the next window.</Text><NumberInput label="Check every (minutes)" min={15} max={10080} value={settings.intervalMinutes} disabled={role !== 'admin' || busy} onChange={v => setSettings({ ...settings, intervalMinutes: Number(v) })} /><WindowFields value={settings.maintenance} onChange={maintenance => setSettings({ ...settings, maintenance })} disabled={role !== 'admin' || busy} />{role === 'admin' && <Button loading={busy} onClick={() => void act(async () => { setSettings(await request<Settings>('/api/image-updates/settings', 'PUT', settings)) }, 'Global schedule saved.')}>Save global schedule</Button>}</Stack></Card>}
      {!settings && <Text c="dimmed">Loading global schedule…</Text>}
      </Tabs.Panel>
    </Tabs>
    <Modal opened={trackingItem !== null} onClose={() => { if (!busy) setTrackingItem(null) }} title="Monitor a tag for this container" centered><Stack>
      <Text size="sm">The running image stays pinned. This setting only checks whether the selected tag points to different image content; automatic updates still require the stack's explicit policy.</Text>
      <Code style={{ overflowWrap: 'anywhere' }}>{trackingItem?.result.runningImage || trackingItem?.result.image}</Code>
      <TextInput label="Monitoring tag" description="Enter only the tag, such as latest or pg17. Leave blank to use the saved stack reference or a tag retained in the running reference. A digest alone has no default tag." value={trackingTag} onChange={e => setTrackingTag(e.currentTarget.value)} disabled={busy} />
      <Text size="xs" c="dimmed">This override applies to this container. Managed stacks recover their configured tags automatically; recreated external containers may need a new override.</Text>
      {error && <Alert color="red">{error}</Alert>}
      <Button loading={busy} onClick={() => { const item = trackingItem; if (item) void act(async () => { await request(`/api/image-updates/containers/${encodeURIComponent(item.agentId)}/${encodeURIComponent(item.containerId)}/tracking`, 'PUT', { tag: trackingTag }); setTrackingItem(null) }, 'Monitoring tag saved and a check queued. The deployed image is unchanged.') }}>Save and check</Button>
    </Stack></Modal>

  </Stack>
}

export function StackUpdatePolicy({ id }: { id: string }) {
  const [policy, setPolicy] = useState<Policy | null>(null)
  const [global, setGlobal] = useState<Settings | null>(null)
  const [acknowledged, setAcknowledged] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  useEffect(() => {
    let active = true
    void Promise.all([request<Policy>(`/api/stacks/${encodeURIComponent(id)}/update-policy`), request<Settings>('/api/image-updates/settings')]).then(([p, g]) => { if (active) { setPolicy(p); setGlobal(g) } }).catch(e => { if (active) setError(String(e.message)) })
    return () => { active = false }
  }, [id])
  async function save() {
    setBusy(true); setError(''); setNotice('')
    try { setPolicy(await request<Policy>(`/api/stacks/${encodeURIComponent(id)}/update-policy`, 'PUT', policy)); setAcknowledged(false); setNotice('Update policy saved. Eligible updates use the next available maintenance window, including the current window if open.') } catch (e) { setError(e instanceof Error ? e.message : 'Policy could not be saved.') } finally { setBusy(false) }
  }
  return <Card withBorder><Stack><Title order={3}>Automatic image updates</Title><Text size="sm">Disabled by default. Only this managed stack can be enrolled here. Updates follow each service’s configured tag; version tags are never advanced automatically.</Text>{error && <Alert color="red">{error}</Alert>}{notice && <Alert color="blue">{notice}</Alert>}{policy && <>
    {policy.pausedReason && <Alert color="orange" title="Automatic updates paused">{policy.pausedReason}</Alert>}
    <Checkbox label="Enable automatic updates for this stack" checked={policy.enabled} disabled={busy} onChange={e => { setPolicy({ ...policy, enabled: e.currentTarget.checked }); setAcknowledged(false) }} />
    <Checkbox label="Override the global maintenance window" checked={policy.maintenance !== null} disabled={busy} onChange={e => { setPolicy({ ...policy, maintenance: e.currentTarget.checked ? global?.maintenance ?? defaultWindow : null }); setAcknowledged(false) }} />
    {policy.maintenance && <WindowFields value={policy.maintenance} disabled={busy} onChange={maintenance => { setPolicy({ ...policy, maintenance }); setAcknowledged(false) }} />}
    <Stack gap="xs">{Object.entries(policy.tracking ?? {}).map(([service, ref]) => <Text size="sm" key={service}><Code>{service}</Code> tracks <Code>{ref}</Code>{ref.includes('@') ? ' (pinned)' : ''}</Text>)}</Stack>
    <Text size="sm" fw={600}>Effective window: {description(policy.maintenance ?? global?.maintenance ?? defaultWindow)}</Text>
    <MultiSelect label="Services excluded from automatic updates" description="Excluded and digest-pinned services retain their deployed images." data={policy.services} value={policy.excludedServices} disabled={busy} onChange={excludedServices => { setPolicy({ ...policy, excludedServices }); setAcknowledged(false) }} />
    {policy.enabled && <Checkbox label="I explicitly authorize automatic image deployments for this stack during the effective window, including any update already pending. Failed updates pause the policy; application data is not automatically rolled back." checked={acknowledged} onChange={e => setAcknowledged(e.currentTarget.checked)} disabled={busy} />}
    <Button loading={busy} disabled={policy.enabled && !acknowledged} onClick={() => void save()}>{policy.enabled ? 'Save explicit opt-in' : 'Save monitor-only policy'}</Button>
  </>}</Stack></Card>
}
