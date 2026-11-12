import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { useAtom } from 'jotai'
import { useMediaQuery } from '@mantine/hooks'
import { Alert, Button, Card, Checkbox, Group, Loader, Menu, Modal, SegmentedControl, SimpleGrid, Stack, Table, Text, TextInput, Title, UnstyledButton } from '@mantine/core'
import { IconDots, IconGitBranch, IconPlus, IconRefresh, IconSearch } from '@tabler/icons-react'
import { relativeTime } from './overview-utils'
import { deviceScopeAtom } from './scope'
import { runStackActions } from './stack-actions'
import { NewStack, type ApprovedTarget } from './stack-management'
import { bulkStacks, excludedFromBulk, filterStacks, isReverseProxy, matchesStackFilter, stackActionBlocker, stackActionTargets, stackFilterCounts, stackLabels, stackRuntimeBadge, stackStatus, stacksWithUpdates, type StackAction, type StackFilter, type StackHost, type StackLabel, type StackSummary } from './stack-list-utils'
import { StackProfiles } from './stack-profiles'
import { GitSettings } from './git-settings'
import { StackImport } from './stack-import'
import { StackLoading } from './stack-loading'
import { StatusText, toneOf } from './status'

async function request<T>(path: string, method = 'GET', body?: unknown): Promise<T> {
  const response = await fetch(path, { method, credentials: 'same-origin', headers: body === undefined ? undefined : { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) })
  if (!response.ok) throw new Error((await response.text()).trim().slice(0, 500) || 'The stack request failed.')
  return response.json() as Promise<T>
}
type Listing = { configured: boolean; branch?: string; stacks: StackSummary[]; targets?: ApprovedTarget[] }
type Outcome = { name: string; ok: boolean; message: string }
type Confirm = { action: 'start' | 'stop'; stacks: StackSummary[]; single: boolean }
type View = 'table' | 'cards'

const filterNames: Record<StackFilter, string> = { all: 'All', attention: 'Needs attention', draft: 'Drafts', update: 'Updates', drift: 'Drift', failed: 'Failed' }
const inventoryProblem = 'Live container counts are unavailable. Saved stack information is still shown.'

function readView(): View {
  try { return localStorage.getItem('vigara.stack-view') === 'cards' ? 'cards' : 'table' } catch { return 'table' }
}
/** The stack's device by name; a removed device has no name left, so say so instead of showing its ID. */
function deviceLabel(stack: StackSummary) {
  if (!stack.agentId) return 'Unassigned'
  return stack.deviceName || 'Unknown device'
}
function deployedAt(stack: StackSummary) {
  return stack.deploymentAt && !stack.deploymentAt.startsWith('0001') ? stack.deploymentAt : null
}
/** Why Pull & recreate cannot run from the list, or null when it can. */
function pullBlocker(stack: StackSummary) {
  if (!stack.commit) return 'Save the stack first.'
  if (!stack.assigned) return 'Assign the stack to a device first.'
  if (stack.draft) return 'Save or discard the draft first.'
  if (['queued', 'running'].includes(stack.deploymentStatus ?? '')) return 'Wait for the deployment to finish.'
  if (stack.deploymentStatus === 'unknown') return 'Check the last deployment first.'
  return null
}

function Labels({ labels, stack }: { labels: StackLabel[]; stack: StackSummary }) {
  if (!labels.length) return <span className="vg-dim">{stackStatus(stack) === 'Deployed' ? 'Deployed' : stackStatus(stack)}</span>
  return <span className="vg-chips">{labels.map((label) => <span key={label.key} className="vg-pill" data-tone={label.tone} title={label.detail}>{label.text}</span>)}</span>
}

function Services({ stack, hosts }: { stack: StackSummary; hosts: StackHost[] }) {
  const badge = stackRuntimeBadge(stack, hosts)
  if (badge.label === 'Unavailable') return <StatusText tone="idle">Unavailable</StatusText>
  if (badge.label === 'Not running') return <StatusText tone="idle">Not running</StatusText>
  return <StatusText tone={toneOf(badge.color)} title={badge.label}>{badge.count} running</StatusText>
}

/** The list of Compose stacks: what differs about each one stands out, the rest stays quiet. */
export function StackList({ onOpen, onPull, role }: { onOpen: (id: string) => void; onPull: (id: string) => void; role: string }) {
  const [listing, setListing] = useState<Listing | null>(null)
  const [hosts, setHosts] = useState<StackHost[]>([])
  const [updates, setUpdates] = useState<Set<string>>(() => new Set())
  const [inventoryError, setInventoryError] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const [refresh, setRefresh] = useState(0)
  const [query, setQuery] = useState('')
  const [filter, setFilter] = useState<StackFilter>('all')
  const [view, setView] = useState<View>(readView)
  // Phones get the cards: a table this wide only scrolls sideways there.
  const phone = useMediaQuery('(max-width: 47.99em)') ?? false
  const layout: View = phone ? 'cards' : view
  const [scope, setScope] = useAtom(deviceScopeAtom)
  const actionLock = useRef(false)
  const [busy, setBusy] = useState<string | null>(null)
  const [outcomes, setOutcomes] = useState<Outcome[]>([])
  const [confirm, setConfirm] = useState<Confirm | null>(null)
  const [creating, setCreating] = useState(false)
  const [importing, setImporting] = useState(false)
  const [settings, setSettings] = useState(() => new URLSearchParams(window.location.search).get('github_app') === 'connected')
  const [profiles, setProfiles] = useState(false)

  useEffect(() => {
    let active = true
    setLoading(true)
    void request<Listing>('/api/stacks').then((value) => { if (active) { setListing(value); setError('') } }).catch((cause: Error) => { if (active) setError(cause.message) }).finally(() => { if (active) setLoading(false) })
    void request<{ hosts: StackHost[] }>('/api/containers').then((value) => { if (active) { setHosts(value.hosts); setInventoryError('') } }).catch(() => { if (active) { setHosts([]); setInventoryError(inventoryProblem) } })
    // Image updates only add a label, so a failure here leaves the list as it is.
    void request<{ items: Parameters<typeof stacksWithUpdates>[0] }>('/api/image-updates').then((value) => { if (active) setUpdates(stacksWithUpdates(value.items ?? [])) }).catch(() => undefined)
    return () => { active = false }
  }, [refresh])
  useEffect(() => {
    let active = true
    const timer = window.setInterval(() => {
      if (actionLock.current) return
      void request<{ hosts: StackHost[] }>('/api/containers').then((value) => {
        if (active && !actionLock.current) { setHosts(value.hosts); setInventoryError('') }
      }).catch(() => { if (active && !actionLock.current) { setHosts([]); setInventoryError(inventoryProblem) } })
    }, 10_000)
    return () => { active = false; window.clearInterval(timer) }
  }, [])

  const all = listing?.stacks ?? []
  const scoped = useMemo(() => filterStacks(all, query, scope ?? '', 'name'), [all, query, scope])
  const labelled = useMemo(() => scoped.map((stack) => ({ stack, labels: stackLabels(stack, hosts, updates) })), [scoped, hosts, updates])
  const counts = useMemo(() => stackFilterCounts(labelled.map((item) => item.labels)), [labelled])
  const rows = labelled.filter((item) => matchesStackFilter(item.labels, filter))
  const devices = new Map(all.filter((stack) => stack.agentId).map((stack) => [stack.agentId, deviceLabel(stack)]))
  const showDevice = !scope && devices.size > 1
  const scopeName = scope ? devices.get(scope) ?? hosts.find((host) => host.agentId === scope)?.name ?? 'one device' : null
  const onlyDevice = devices.size === 1 ? [...devices.values()][0] : null

  async function run(action: StackAction, ids: string[]) {
    if (actionLock.current) return
    actionLock.current = true
    setBusy(ids.length === 1 ? `${ids[0]}:${action}` : `all:${action}`)
    setOutcomes([])
    try {
      // Refresh before choosing targets so stale inventory cannot select a recreated container.
      const [fresh, inventory] = await Promise.all([request<Listing>('/api/stacks'), request<{ hosts: StackHost[] }>('/api/containers')])
      setListing(fresh); setHosts(inventory.hosts); setInventoryError('')
      setOutcomes(await runStackActions(fresh.stacks, inventory.hosts, role, action, ids))
    } catch (cause) { setOutcomes([{ name: 'Stack action', ok: false, message: cause instanceof Error ? cause.message : 'The stack action failed.' }]) }
    finally { actionLock.current = false; setBusy(null); setRefresh((value) => value + 1) }
  }
  const allFor = (action: 'start' | 'stop') => setConfirm({ action, stacks: bulkStacks(all, hosts, true), single: false })

  function rowActions(stack: StackSummary): ReactNode {
    const blocker = stackActionBlocker(stack, hosts, role)
    const pull = pullBlocker(stack)
    const can = (action: StackAction) => !busy && !blocker && stackActionTargets(stack, hosts, action).length > 0
    return <Group gap={4} wrap="nowrap" className="vg-row-actions" onClick={(event) => event.stopPropagation()}>
      <Button size="compact-xs" variant="default" disabled={!!pull} title={pull ?? 'Download newer images for the configured tags and recreate every service'} onClick={() => onPull(stack.id)}>Pull &amp; recreate</Button>
      <Menu position="bottom-end" withinPortal>
        <Menu.Target><Button size="compact-xs" variant="default" px={6} aria-label={`More actions for ${stack.name || stack.id}`} loading={!!busy?.startsWith(`${stack.id}:`)}><IconDots size={14} /></Button></Menu.Target>
        <Menu.Dropdown>
          <Menu.Item disabled={!can('restart')} onClick={() => void run('restart', [stack.id])}>Restart</Menu.Item>
          <Menu.Item disabled={!can('stop')} onClick={() => setConfirm({ action: 'stop', stacks: [stack], single: true })}>Stop…</Menu.Item>
          <Menu.Item disabled={!can('start')} onClick={() => void run('start', [stack.id])}>Start</Menu.Item>
          {blocker && <Menu.Label>{blocker}</Menu.Label>}
          <Menu.Divider />
          <Menu.Item onClick={() => onOpen(stack.id)}>Open</Menu.Item>
        </Menu.Dropdown>
      </Menu>
    </Group>
  }

  const configured = !!listing?.configured
  return <div className="vg-page vg-stacks">
    <Group justify="space-between" align="flex-end" mb="md" wrap="wrap">
      <div>
        <Title order={1}>Stacks</Title>
        <Text size="sm" c="dimmed" mt={4} className="vg-meta-row">
          {listing?.branch && <span className="vg-nowrap"><IconGitBranch size={13} aria-hidden="true" /> <span className="vg-mono">{listing.branch}</span></span>}
          {listing && <span>{scoped.length === all.length ? `${all.length} stack${all.length === 1 ? '' : 's'}` : `${scoped.length} of ${all.length} stacks`}</span>}
          {scopeName ? <span>on {scopeName} · <a href="#" onClick={(event) => { event.preventDefault(); setScope(null) }}>show all devices</a></span> : onlyDevice && <span>on {onlyDevice}</span>}
          <a href="#" onClick={(event) => { event.preventDefault(); setSettings(true) }}>Repository settings</a>
        </Text>
      </div>
      <Group gap="xs">
        <Button variant="default" size="xs" disabled={!configured || loading} onClick={() => setImporting(true)}>Import existing</Button>
        <Button size="xs" leftSection={<IconPlus size={14} />} disabled={!configured || loading} onClick={() => setCreating(true)}>New stack</Button>
        <Menu position="bottom-end" withinPortal>
          <Menu.Target><Button variant="default" size="xs" px={8} aria-label="More stack actions"><IconDots size={16} /></Button></Menu.Target>
          <Menu.Dropdown>
            <Menu.Item leftSection={<IconRefresh size={14} />} onClick={() => setRefresh((value) => value + 1)}>Refresh</Menu.Item>
            <Menu.Item disabled={!configured} onClick={() => setProfiles(true)}>Profiles and assignments</Menu.Item>
            <Menu.Item onClick={() => setSettings(true)}>Git settings</Menu.Item>
            <Menu.Divider />
            <Menu.Item disabled={!!busy || !bulkStacks(all, hosts, true).length} onClick={() => allFor('start')}>Start all stacks…</Menu.Item>
            <Menu.Item color="red" disabled={!!busy || !bulkStacks(all, hosts, true).length} onClick={() => allFor('stop')}>Stop all stacks…</Menu.Item>
          </Menu.Dropdown>
        </Menu>
      </Group>
    </Group>

    {busy && <Text role="status" size="sm" mb="sm">Changing stack containers…</Text>}
    {!!outcomes.length && <Alert mb="md" color={outcomes.some((outcome) => !outcome.ok) ? 'red' : 'green'} title="Stack action results" withCloseButton onClose={() => setOutcomes([])}><Stack gap="xs">{outcomes.map((outcome, index) => <Text key={index} size="sm"><Text span fw={600}>{outcome.name}:</Text> {outcome.message}</Text>)}</Stack></Alert>}
    {error && <Alert mb="md" color="red" title="Could not load stacks">{error}</Alert>}
    {inventoryError && <Alert mb="md" color="yellow">{inventoryError}</Alert>}
    {!listing && loading && <StackLoading />}
    {listing && !configured && <Card withBorder padding="lg"><Stack><Title order={3}>Connect your stack repository</Title><Text>Choose a Git repository and branch to keep a history of your Compose files.</Text><Group><Button onClick={() => setSettings(true)}>Open Git settings</Button></Group></Stack></Card>}

    {configured && <>
      {!all.length && <Alert mb="md" color="blue" title="Already running Vigara, Caddy, or other Compose projects?"><Text size="sm">Import them from your enrolled host to add their configuration to Git without restarting containers.</Text><Button mt="sm" variant="light" onClick={() => setImporting(true)}>Find existing stacks</Button></Alert>}
      <div className="vg-toolbar vg-filterbar">
        <TextInput size="xs" leftSection={<IconSearch size={14} />} placeholder="Filter stacks" value={query} onChange={(event) => setQuery(event.currentTarget.value)} className="vg-filter" aria-label="Filter stacks" />
        <SegmentedControl size="xs" className="vg-scroll-x" value={filter} onChange={(value) => setFilter(value as StackFilter)} aria-label="Show" data={(Object.keys(filterNames) as StackFilter[]).filter((key) => key === 'all' || key === 'attention' || counts[key] > 0 || key === filter).map((key) => ({ value: key, label: `${filterNames[key]} ${counts[key]}` }))} />
        <span className="vg-grow" />
        {!phone && <SegmentedControl size="xs" value={view} onChange={(value) => { setView(value as View); try { localStorage.setItem('vigara.stack-view', value) } catch { /* The layout preference is optional. */ } }} data={[{ value: 'table', label: 'Table' }, { value: 'cards', label: 'Cards' }]} aria-label="Layout" />}
      </div>

      {!rows.length ? <div className="device-empty"><Text c="dimmed" size="sm">{all.length ? 'No stacks match these filters.' : 'Create your first stack to start editing and deploying from Vigara.'}</Text></div>
        : layout === 'cards' ? <SimpleGrid cols={{ base: 1, sm: 2, xl: 3 }}>{rows.map(({ stack, labels }) => <Card key={stack.id} withBorder padding="md" className="vg-stack-card">
          <UnstyledButton className="vg-row-link" onClick={() => onOpen(stack.id)}><Text fw={600}>{stack.name || stack.id}</Text></UnstyledButton>
          {stack.projectName !== stack.id && <Text size="xs" c="dimmed">project {stack.projectName}</Text>}
          <div className="vg-stack-card-status"><Services stack={stack} hosts={hosts} /><Labels labels={labels} stack={stack} /></div>
          <Text size="xs" c="dimmed">{[showDevice && deviceLabel(stack), deployedAt(stack) ? `deployed ${relativeTime(deployedAt(stack))}` : 'never deployed', stack.commit && stack.commit.slice(0, 7)].filter(Boolean).join(' · ')}</Text>
          <div className="vg-stack-card-actions">{rowActions(stack)}</div>
        </Card>)}</SimpleGrid>
        : <div className="vg-panel"><div className="table-scroll"><Table className="vg-table vg-stack-table" highlightOnHover verticalSpacing={7}>
          <Table.Thead><Table.Tr><Table.Th>Stack</Table.Th><Table.Th>Services</Table.Th><Table.Th>Status</Table.Th>{showDevice && <Table.Th>Device</Table.Th>}<Table.Th>Last deploy</Table.Th><Table.Th>Commit</Table.Th><Table.Th aria-label="Actions" /></Table.Tr></Table.Thead>
          <Table.Tbody>{rows.map(({ stack, labels }) => <Table.Tr key={stack.id} className="vg-click-row" onClick={() => onOpen(stack.id)}>
            <Table.Td><UnstyledButton className="vg-row-link" onClick={(event) => { event.stopPropagation(); onOpen(stack.id) }}><b>{stack.name || stack.id}</b></UnstyledButton>{stack.projectName !== stack.id && <Text size="xs" c="dimmed">project {stack.projectName}</Text>}</Table.Td>
            <Table.Td className="vg-nowrap"><Services stack={stack} hosts={hosts} /></Table.Td>
            <Table.Td><Labels labels={labels} stack={stack} /></Table.Td>
            {showDevice && <Table.Td className="vg-dim vg-nowrap">{deviceLabel(stack)}</Table.Td>}
            <Table.Td className="vg-dim vg-nowrap" title={deployedAt(stack) ? new Date(deployedAt(stack) as string).toLocaleString() : undefined}>{deployedAt(stack) ? relativeTime(deployedAt(stack)) : '—'}</Table.Td>
            <Table.Td>{stack.commit ? <span className="vg-mono vg-dim">{stack.commit.slice(0, 7)}</span> : <span className="vg-dim">—</span>}</Table.Td>
            <Table.Td ta="right">{rowActions(stack)}</Table.Td>
          </Table.Tr>)}</Table.Tbody>
        </Table></div></div>}
    </>}

    <BulkConfirm confirm={confirm} hosts={hosts} onCancel={() => setConfirm(null)} onConfirm={(ids) => { const action = confirm?.action; setConfirm(null); if (action) void run(action, ids) }} />
    <Modal opened={creating} onClose={() => setCreating(false)} title="New Compose stack" size="lg" centered>{creating && <NewStack approved={listing?.targets?.filter((target) => !all.some((stack) => stack.id === target.id)) ?? []} onOpen={onOpen} />}</Modal>
    <Modal opened={settings} onClose={() => setSettings(false)} title="Git settings" size="lg" centered>{settings && <GitSettings onSaved={() => { setSettings(false); setRefresh((value) => value + 1); if (!listing?.configured) setImporting(true) }} />}</Modal>
    <Modal opened={importing} onClose={() => {}} withCloseButton={false} closeOnEscape={false} closeOnClickOutside={false} title="Import existing stack" size="xl" centered>{importing && <StackImport onClose={() => { setImporting(false); setRefresh((value) => value + 1) }} onImported={(id) => { setImporting(false); setRefresh((value) => value + 1); onOpen(id) }} />}</Modal>
    <Modal opened={profiles} onClose={() => {}} withCloseButton={false} closeOnEscape={false} closeOnClickOutside={false} title="Server profiles and assignments" size="xl" centered>{profiles && <StackProfiles stacks={all} onClose={() => setProfiles(false)} />}</Modal>
    {loading && listing && <Loader size="xs" className="vg-corner-loader" aria-label="Refreshing" />}
  </div>
}

/**
 * Confirms Start all / Stop all, or stopping one stack, by listing every stack it touches.
 * Vigara's own stacks stay running unless the box is cleared; reverse proxies get a warning.
 */
function BulkConfirm({ confirm, hosts, onCancel, onConfirm }: { confirm: Confirm | null; hosts: StackHost[]; onCancel: () => void; onConfirm: (ids: string[]) => void }) {
  const [keepVigara, setKeepVigara] = useState(true)
  useEffect(() => { setKeepVigara(true) }, [confirm])
  if (!confirm) return <Modal opened={false} onClose={onCancel}>{null}</Modal>
  const { action, single } = confirm
  const protectedStacks = single ? [] : confirm.stacks.filter(excludedFromBulk)
  const affected = single || !keepVigara ? confirm.stacks : confirm.stacks.filter((stack) => !excludedFromBulk(stack))
  const verb = action === 'stop' ? 'Stop' : 'Start'
  const noun = `${affected.length} stack${affected.length === 1 ? '' : 's'}`
  const devices = [...new Set(affected.map((stack) => deviceLabel(stack)))]
  const where = devices.length === 1 ? ` on ${devices[0]}` : ''
  const proxies = affected.filter(isReverseProxy)
  return <Modal opened onClose={onCancel} title={single ? `${verb} ${affected[0]?.name || affected[0]?.id}?` : `${verb} ${noun}?`} centered size="md">
    <Stack gap="sm">
      <Text size="sm" c="dimmed">{action === 'stop'
        ? `Every running container in ${single ? 'this stack' : 'these stacks'} stops${where}. Nothing is removed, and you can start ${single ? 'it' : 'them'} again later.`
        : `Existing containers start${where}. Missing containers are not created; deploy a stack to create them.`}</Text>
      {single && action === 'stop' && affected[0] && <Text size="sm">{stackActionTargets(affected[0], hosts, 'stop').length} container{stackActionTargets(affected[0], hosts, 'stop').length === 1 ? '' : 's'} will stop.</Text>}
      {!single && <div className="vg-panel vg-confirm-list"><Table className="vg-table" verticalSpacing={4}><Table.Tbody>{affected.map((stack) => <Table.Tr key={stack.id}>
        <Table.Td><b>{stack.name || stack.id}</b></Table.Td>
        <Table.Td className="vg-dim vg-nowrap">{devices.length > 1 ? deviceLabel(stack) : ''}</Table.Td>
        <Table.Td ta="right">{action === 'stop' && isReverseProxy(stack) && <span className="vg-pill" data-tone="warn">Reverse proxy</span>}</Table.Td>
      </Table.Tr>)}</Table.Tbody></Table></div>}
      {action === 'stop' && proxies.length > 0 && <Alert color="yellow" p="xs">{proxies.map((stack) => stack.name || stack.id).join(', ')} {proxies.length === 1 ? 'looks like a reverse proxy' : 'look like reverse proxies'}. Sites behind {proxies.length === 1 ? 'it' : 'them'} go offline while stopped.</Alert>}
      {protectedStacks.length > 0 && <Checkbox checked={keepVigara} onChange={(event) => setKeepVigara(event.currentTarget.checked)} label={<>Keep Vigara’s own stack{protectedStacks.length === 1 ? '' : 's'} running ({protectedStacks.map((stack) => stack.name || stack.id).join(', ')})</>} />}
      <Group justify="flex-end" gap="xs" mt="xs">
        <Button variant="default" onClick={onCancel}>Cancel</Button>
        <Button color={action === 'stop' ? 'red' : undefined} disabled={!affected.length} onClick={() => onConfirm(affected.map((stack) => stack.id))}>{verb} {noun}</Button>
      </Group>
    </Stack>
  </Modal>
}
