import { useCallback, useEffect, useMemo, useState } from 'react'
import { Alert, Anchor, Badge, Button, Card, Checkbox, Code, Group, List, Loader, Modal, Select, Stack, Table, Text, TextInput, Title } from '@mantine/core'
import { canManage, drainRecords, manageBlocker, type HostEntry } from './container-utils'
import { route as routeOf, type Route, type Section } from './route'
import {
  confirmWord, filterBy, imageName, isInUse, plurals, pruneConfirmWord, pruneDescription, pullRefProblem, removeBlocker, resourcesPath, shortId, sizeLabel, sortRows, summarizePrune, usageLabel,
  type ImageRow, type InspectResult, type NetworkRow, type PruneResult, type ResourceKind, type UsedBy, type VolumeRow,
} from './resource-utils'

type Row = ImageRow | NetworkRow | VolumeRow
type Props = { kind: ResourceKind; route: Route; role: string; onNavigate: (route: Route) => void }

const sections: Record<ResourceKind, Section> = { image: 'images', network: 'networks', volume: 'volumes' }
const titles: Record<ResourceKind, { title: string; blurb: string }> = {
  image: { title: 'Images', blurb: 'Images stored on a host. Unused images take up disk space and can be removed.' },
  network: { title: 'Networks', blurb: 'Docker networks on a host and the containers attached to them.' },
  volume: { title: 'Volumes', blurb: 'Volumes hold container data. Removing one deletes that data for good.' },
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, { credentials: 'same-origin', ...init })
  if (!response.ok) throw new Error((await response.text()).trim().slice(0, 400) || 'The request failed.')
  return response.json() as Promise<T>
}

const post = <T,>(path: string, body: unknown) => request<T>(path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })

const keyOf = (kind: ResourceKind, row: Row): string => kind === 'image' ? (row as ImageRow).id : kind === 'network' ? (row as NetworkRow).name : (row as VolumeRow).name
const nameOf = (kind: ResourceKind, row: Row): string => kind === 'image' ? imageName(row as ImageRow) : keyOf(kind, row)
// Images are removed by ID, so removing one tag name never deletes an image other tags still use.
const refOf = (kind: ResourceKind, row: Row): string => kind === 'image' ? (row as ImageRow).id : kind === 'network' ? (row as NetworkRow).id : (row as VolumeRow).name

export function Resources({ kind, route, role, onNavigate }: Props) {
  const [hosts, setHosts] = useState<HostEntry[] | null>(null)
  const [rows, setRows] = useState<Row[] | null>(null)
  const [error, setError] = useState('')
  const [search, setSearch] = useState('')
  const [detail, setDetail] = useState<Row | null>(null)
  const [removing, setRemoving] = useState<Row | null>(null)
  const [pruning, setPruning] = useState(false)
  const [pulling, setPulling] = useState(false)

  const usable = useMemo(() => (hosts ?? []).filter((host) => host.state === 'ok'), [hosts])
  const hostId = route.id && usable.some((host) => host.agentId === route.id) ? route.id : usable[0]?.agentId ?? null
  const host = usable.find((entry) => entry.agentId === hostId)
  const blocker = manageBlocker(role, host?.dockerTier)
  const mayManage = canManage(role, host?.dockerTier)

  useEffect(() => {
    let active = true
    const load = () => request<{ hosts: HostEntry[] }>('/api/containers').then((value) => { if (active) setHosts(value.hosts) }).catch((cause: unknown) => { if (active) setError(cause instanceof Error ? cause.message : 'Hosts could not be loaded.') })
    void load()
    return () => { active = false }
  }, [])

  const refresh = useCallback(async () => {
    if (!hostId) return
    try {
      const result = await request<Record<string, Row[] | null>>(resourcesPath(hostId, kind))
      setRows(result[plurals[kind]] ?? [])
      setError('')
    } catch (cause) { setError(cause instanceof Error ? cause.message : 'The list could not be loaded.') }
  }, [hostId, kind])

  useEffect(() => {
    const first = window.setTimeout(() => { setRows(null); void refresh() }, 0)
    const timer = window.setInterval(() => { void refresh() }, 15_000)
    return () => { window.clearTimeout(first); window.clearInterval(timer) }
  }, [refresh])

  const shown = useMemo(() => {
    const filtered = filterBy(rows ?? [], search, (row) => kind === 'image' ? [imageName(row as ImageRow), (row as ImageRow).id, ...((row as ImageRow).tags ?? [])] : [nameOf(kind, row), ((row as NetworkRow).driver ?? ''), ((row as VolumeRow).mountpoint ?? '')])
    return kind === 'image' ? sortRows(filtered, (row) => (row as ImageRow).created, true) : sortRows(filtered, (row) => nameOf(kind, row))
  }, [rows, search, kind])
  const unusedCount = (rows ?? []).filter((row) => !isInUse((row as { usedBy?: UsedBy[] }).usedBy) && !(kind === 'network' && (row as NetworkRow).builtin)).length
  const info = titles[kind]

  return <>
    <Text size="xs" fw={700} c="dimmed" tt="uppercase">Home infrastructure</Text>
    <Title order={1} mt={4}>{info.title}</Title>
    <Text c="dimmed" mt={6}>{info.blurb}</Text>
    {error && <Alert color="red" mt="md" title={`${info.title} could not be loaded`}>{error}</Alert>}

    <Card withBorder radius="md" padding="lg" mt="lg">
      {hosts === null ? <Group><Loader size="sm" /><Text c="dimmed">Loading hosts…</Text></Group>
        : usable.length === 0 ? <Text c="dimmed">No connected host has Docker enabled. Enable it on a host's agent to see its {info.title.toLowerCase()}.</Text>
        : <>
          <Group gap="sm" mb="md" align="flex-end">
            <Select label="Host" value={hostId} onChange={(value) => onNavigate(routeOf(sections[kind], value))} allowDeselect={false} data={usable.map((entry) => ({ value: entry.agentId, label: entry.name }))} w={240} />
            <TextInput label="Search" placeholder={`Search ${info.title.toLowerCase()}`} value={search} onChange={(event) => setSearch(event.currentTarget.value)} style={{ flex: '1 1 200px' }} />
            <Button variant="default" onClick={() => void refresh()}>Refresh</Button>
            {mayManage && kind === 'image' && <Button onClick={() => setPulling(true)}>Pull image…</Button>}
            {mayManage && <Button color="orange" variant="light" onClick={() => setPruning(true)}>Prune unused…</Button>}
          </Group>
          {blocker && <Text size="xs" c="dimmed" mb="sm">{blocker}</Text>}
          {rows === null && !error ? <Group><Loader size="sm" /><Text c="dimmed">Loading…</Text></Group>
            : shown.length === 0 ? <Text c="dimmed">{(rows ?? []).length === 0 ? `No ${info.title.toLowerCase()} on this host.` : 'Nothing matches that search.'}</Text>
            : <Table.ScrollContainer minWidth={640}><Table highlightOnHover verticalSpacing="xs">
              <Table.Thead><Table.Tr>{kind === 'image' && <><Table.Th>Image</Table.Th><Table.Th>ID</Table.Th><Table.Th>Size</Table.Th><Table.Th>Created</Table.Th></>}
                {kind === 'network' && <><Table.Th>Name</Table.Th><Table.Th>Driver</Table.Th><Table.Th>Subnet</Table.Th><Table.Th>Scope</Table.Th></>}
                {kind === 'volume' && <><Table.Th>Name</Table.Th><Table.Th>Driver</Table.Th><Table.Th>Size</Table.Th><Table.Th>Created</Table.Th></>}
                <Table.Th>Used by</Table.Th></Table.Tr></Table.Thead>
              <Table.Tbody>{shown.map((row) => {
                const usedBy = (row as { usedBy?: UsedBy[] }).usedBy
                return <Table.Tr key={keyOf(kind, row)} className="device-row" tabIndex={0} aria-label={`Open ${nameOf(kind, row)}`} onClick={() => setDetail(row)} onKeyDown={(event) => { if (event.target === event.currentTarget && (event.key === 'Enter' || event.key === ' ')) { event.preventDefault(); setDetail(row) } }}>
                  {kind === 'image' && <><Table.Td><Text fw={600} size="sm" style={{ wordBreak: 'break-all' }}>{imageName(row as ImageRow)}</Text>{((row as ImageRow).tags?.length ?? 0) > 1 && <Text size="xs" c="dimmed">+{(row as ImageRow).tags!.length - 1} more tag{(row as ImageRow).tags!.length > 2 ? 's' : ''}</Text>}</Table.Td>
                    <Table.Td><Code>{shortId((row as ImageRow).id)}</Code></Table.Td><Table.Td>{sizeLabel((row as ImageRow).size)}</Table.Td><Table.Td>{new Date((row as ImageRow).created * 1000).toLocaleDateString()}</Table.Td></>}
                  {kind === 'network' && <><Table.Td><Group gap={6}><Text fw={600} size="sm">{(row as NetworkRow).name}</Text>{(row as NetworkRow).builtin && <Badge size="xs" variant="light" color="gray">built-in</Badge>}{(row as NetworkRow).internal && <Badge size="xs" variant="light" color="orange">internal</Badge>}</Group></Table.Td>
                    <Table.Td>{(row as NetworkRow).driver}</Table.Td><Table.Td>{(row as NetworkRow).subnets?.join(', ') || '–'}</Table.Td><Table.Td>{(row as NetworkRow).scope}</Table.Td></>}
                  {kind === 'volume' && <><Table.Td><Group gap={6}><Text fw={600} size="sm" style={{ wordBreak: 'break-all' }}>{(row as VolumeRow).anonymous ? shortId((row as VolumeRow).name) : (row as VolumeRow).name}</Text>{(row as VolumeRow).anonymous && <Badge size="xs" variant="light" color="gray">anonymous</Badge>}</Group></Table.Td>
                    <Table.Td>{(row as VolumeRow).driver}</Table.Td><Table.Td>{sizeLabel((row as VolumeRow).size)}</Table.Td><Table.Td>{(row as VolumeRow).created ? new Date((row as VolumeRow).created!).toLocaleDateString() : '–'}</Table.Td></>}
                  <Table.Td>{isInUse(usedBy) ? <Text size="sm">{usageLabel(usedBy)}</Text> : <Badge variant="light" color="yellow" size="sm">Unused</Badge>}</Table.Td>
                </Table.Tr>
              })}</Table.Tbody></Table></Table.ScrollContainer>}
          {rows !== null && <Text size="xs" c="dimmed" mt="sm">{shown.length} of {rows.length} {info.title.toLowerCase()}{unusedCount > 0 ? ` · ${unusedCount} unused` : ''}</Text>}
        </>}
    </Card>

    {detail && hostId && <DetailDialog kind={kind} hostId={hostId} row={detail} mayManage={mayManage} onClose={() => setDetail(null)} onRemove={(row) => { setDetail(null); setRemoving(row) }} onOpenContainer={(name) => onNavigate(routeOf('containers', hostId, name))} />}
    {removing && hostId && <RemoveDialog kind={kind} hostId={hostId} row={removing} onClose={() => setRemoving(null)} onDone={() => { setRemoving(null); void refresh() }} />}
    {pruning && hostId && <PruneDialog kind={kind} hostId={hostId} onClose={() => setPruning(false)} onDone={() => void refresh()} />}
    {pulling && hostId && <PullDialog hostId={hostId} onClose={() => setPulling(false)} onDone={() => void refresh()} />}
  </>
}

function DetailDialog({ kind, hostId, row, mayManage, onClose, onRemove, onOpenContainer }: { kind: ResourceKind; hostId: string; row: Row; mayManage: boolean; onClose: () => void; onRemove: (row: Row) => void; onOpenContainer: (name: string) => void }) {
  const [inspect, setInspect] = useState<InspectResult | null>(null)
  const [error, setError] = useState('')
  useEffect(() => {
    let active = true
    request<InspectResult>(`${resourcesPath(hostId, kind)}/inspect?ref=${encodeURIComponent(refOf(kind, row))}`).then((value) => { if (active) setInspect(value) }).catch((cause: unknown) => { if (active) setError(cause instanceof Error ? cause.message : 'Details could not be loaded.') })
    return () => { active = false }
  }, [hostId, kind, row])
  const usedBy = (inspect?.image ?? inspect?.network ?? inspect?.volume ?? (row as { usedBy?: UsedBy[] }))?.usedBy
  const blocker = removeBlocker(kind, row as { usedBy?: UsedBy[]; builtin?: boolean })
  const field = (label: string, value: React.ReactNode) => value === undefined || value === null || value === '' ? null : <div key={label}><Text size="xs" c="dimmed" fw={600} tt="uppercase">{label}</Text><Text size="sm" style={{ wordBreak: 'break-word' }}>{value}</Text></div>
  const image = inspect?.image
  const network = inspect?.network
  const volume = inspect?.volume
  return <Modal opened onClose={onClose} title={nameOf(kind, row)} size="lg" centered>
    <Stack gap="sm">
      {error && <Alert color="red">{error}</Alert>}
      {!inspect && !error && <Group><Loader size="sm" /><Text c="dimmed">Loading…</Text></Group>}
      {image && <Stack gap="xs">{field('ID', <Code>{image.id}</Code>)}{field('Tags', image.tags?.join(', '))}{field('Size', sizeLabel(image.size))}{field('Created', new Date(image.created * 1000).toLocaleString())}
        {field('Platform', [image.os, image.architecture].filter(Boolean).join(' / '))}{field('Layers', image.layers)}{field('Runs as', image.user)}{field('Working directory', image.workingDir)}{field('Entrypoint', image.entrypoint?.join(' '))}{field('Command', image.command?.join(' '))}
        {field('Exposed ports', image.exposedPorts?.join(', '))}{field('Environment variables', image.envNames?.join(', '))}{field('Digests', image.digests?.join(', '))}</Stack>}
      {network && <Stack gap="xs">{field('ID', <Code>{network.id}</Code>)}{field('Driver', network.driver)}{field('Scope', network.scope)}{field('Subnets', network.subnets?.join(', '))}{field('Gateways', network.gateways?.join(', '))}
        {field('Options', network.internal ? 'Internal (no outside access)' : undefined)}
        {network.members && network.members.length > 0 && <Table verticalSpacing="xs"><Table.Thead><Table.Tr><Table.Th>Container</Table.Th><Table.Th>IPv4</Table.Th><Table.Th>MAC</Table.Th></Table.Tr></Table.Thead><Table.Tbody>{network.members.map((member) => <Table.Tr key={member.id}><Table.Td>{member.name}</Table.Td><Table.Td>{member.ipv4 || '–'}</Table.Td><Table.Td>{member.mac || '–'}</Table.Td></Table.Tr>)}</Table.Tbody></Table>}</Stack>}
      {volume && <Stack gap="xs">{field('Name', <Code>{volume.name}</Code>)}{field('Driver', volume.driver)}{field('Mount point on the host', <Code>{volume.mountpoint}</Code>)}{field('Size', sizeLabel(volume.size))}{field('Created', volume.created ? new Date(volume.created).toLocaleString() : undefined)}
        {volume.anonymous && field('Kind', 'Anonymous: created for a container without a name')}</Stack>}
      <div><Text size="xs" c="dimmed" fw={600} tt="uppercase">Used by</Text>
        {isInUse(usedBy) ? <Group gap="xs" mt={4}>{usedBy!.map((user) => <Anchor key={user.id} size="sm" component="button" onClick={() => onOpenContainer(user.name)}>{user.name}</Anchor>)}</Group> : <Text size="sm" c="dimmed">No container uses this.</Text>}</div>
      {mayManage && <Group justify="space-between" mt="xs"><Text size="xs" c="dimmed">{blocker ?? ''}</Text><Button color="red" variant="light" disabled={blocker !== null} onClick={() => onRemove(row)}>Remove…</Button></Group>}
    </Stack>
  </Modal>
}

function RemoveDialog({ kind, hostId, row, onClose, onDone }: { kind: ResourceKind; hostId: string; row: Row; onClose: () => void; onDone: () => void }) {
  const name = nameOf(kind, row)
  const word = confirmWord(kind, keyOf(kind, row))
  const [typed, setTyped] = useState('')
  const [force, setForce] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [done, setDone] = useState<string | null>(null)
  async function run() {
    setBusy(true); setError('')
    try {
      const result = await post<{ untagged?: string[]; deleted?: string[] }>(`${resourcesPath(hostId, kind)}/remove`, { ref: refOf(kind, row), force: kind === 'image' ? force : false })
      setDone(kind === 'image' ? `Removed ${[...(result.untagged ?? []), ...(result.deleted ?? [])].length} item${[...(result.untagged ?? []), ...(result.deleted ?? [])].length === 1 ? '' : 's'}.` : `${name} was removed.`)
      onDone()
    } catch (cause) { setError(cause instanceof Error ? cause.message : 'The removal failed.') } finally { setBusy(false) }
  }
  return <Modal opened onClose={busy ? () => undefined : onClose} title={`Remove ${name}?`} centered>
    <Stack>
      {done ? <Alert color="green">{done}</Alert> : <>
        {kind === 'volume' && <Alert color="red" title="This deletes data">Everything stored in this volume is lost permanently. There is no undo, and Vigara does not back volumes up.</Alert>}
        {kind === 'image' && <Text size="sm">The image is deleted from this host. It can be pulled again later if it is still in a registry. Layers shared with other images stay.</Text>}
        {kind === 'network' && <Text size="sm">The network is deleted. Containers can no longer reach each other over it.</Text>}
        {kind === 'image' && (row as ImageRow).tags && (row as ImageRow).tags!.length > 1 && <Checkbox checked={force} onChange={(event) => setForce(event.currentTarget.checked)} label="Remove it even though it has several tags" description={`Tags: ${(row as ImageRow).tags!.join(', ')}`} />}
        {word && <TextInput label={<>Type <Code>{word}</Code> to confirm</>} value={typed} onChange={(event) => setTyped(event.currentTarget.value)} autoComplete="off" />}
        {error && <Alert color="red" title="That did not work">{error}</Alert>}</>}
      <Group justify="flex-end"><Button variant="default" disabled={busy} onClick={onClose}>{done ? 'Close' : 'Cancel'}</Button>{!done && <Button color="red" loading={busy} disabled={word !== null && typed !== word} onClick={() => void run()}>Remove</Button>}</Group>
    </Stack>
  </Modal>
}

function PruneDialog({ kind, hostId, onClose, onDone }: { kind: ResourceKind; hostId: string; onClose: () => void; onDone: () => void }) {
  const [all, setAll] = useState(false)
  const [preview, setPreview] = useState<PruneResult | null>(null)
  const [outcome, setOutcome] = useState<PruneResult | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [typed, setTyped] = useState('')
  const path = `${resourcesPath(hostId, kind)}/prune`

  useEffect(() => {
    let active = true
    const timer = window.setTimeout(() => {
      setPreview(null); setError(''); setTyped('')
      post<PruneResult>(path, { all, dryRun: true }).then((value) => { if (active) setPreview(value) }).catch((cause: unknown) => { if (active) setError(cause instanceof Error ? cause.message : 'The preview failed.') })
    }, 0)
    return () => { active = false; window.clearTimeout(timer) }
  }, [path, all])

  const candidates = preview?.candidates ?? []
  const word = pruneConfirmWord(kind, candidates.length)
  async function run() {
    setBusy(true); setError('')
    try { setOutcome(await post<PruneResult>(path, { all, dryRun: false })); onDone() } catch (cause) { setError(cause instanceof Error ? cause.message : 'The prune failed.') } finally { setBusy(false) }
  }
  return <Modal opened onClose={busy ? () => undefined : onClose} title={`Prune unused ${plurals[kind]}`} centered size="lg">
    <Stack>
      {outcome ? <Alert color="green" title="Done">Removed {outcome.removed?.length ?? 0} item{(outcome.removed?.length ?? 0) === 1 ? '' : 's'}{outcome.spaceReclaimed ? `, freeing ${sizeLabel(outcome.spaceReclaimed)}` : ''}.</Alert> : <>
        <Text size="sm">This removes {pruneDescription(kind, all)}.</Text>
        {kind !== 'network' && <Checkbox checked={all} onChange={(event) => setAll(event.currentTarget.checked)} label={kind === 'image' ? 'Also remove unused tagged images' : 'Also remove unused named volumes'} />}
        {kind === 'volume' && <Alert color="red" title="Volumes hold data">Deleted volumes cannot be recovered. Check the list below carefully, especially with named volumes included.</Alert>}
        {!preview && !error && <Group><Loader size="sm" /><Text c="dimmed">Working out what would be removed…</Text></Group>}
        {preview && <>
          <Text size="sm" fw={600}>{summarizePrune(candidates)}</Text>
          {candidates.length > 0 && <Table.ScrollContainer minWidth={420} mah={260}><Table verticalSpacing={4}><Table.Tbody>{candidates.map((item) => <Table.Tr key={item.id}><Table.Td><Text size="sm" style={{ wordBreak: 'break-all' }}>{item.name || shortId(item.id)}</Text></Table.Td><Table.Td ta="right"><Text size="sm" c="dimmed">{item.size ? sizeLabel(item.size) : ''}</Text></Table.Td></Table.Tr>)}</Table.Tbody></Table></Table.ScrollContainer>}
          {word && candidates.length > 0 && <TextInput label={<>Type <Code>{word}</Code> to confirm</>} value={typed} onChange={(event) => setTyped(event.currentTarget.value)} autoComplete="off" />}</>}
        {error && <Alert color="red" title="That did not work">{error}</Alert>}</>}
      <Group justify="flex-end"><Button variant="default" disabled={busy} onClick={onClose}>{outcome ? 'Close' : 'Cancel'}</Button>
        {!outcome && <Button color="red" loading={busy} disabled={!preview || candidates.length === 0 || (word !== null && typed !== word)} onClick={() => void run()}>Prune</Button>}</Group>
    </Stack>
  </Modal>
}

type PullStep = { step: string; message?: string; layer?: string; current?: number; total?: number }

function PullDialog({ hostId, onClose, onDone }: { hostId: string; onClose: () => void; onDone: () => void }) {
  const [ref, setRef] = useState('')
  const [phase, setPhase] = useState<'ready' | 'running' | 'done' | 'failed'>('ready')
  const [steps, setSteps] = useState<PullStep[]>([])
  const [error, setError] = useState('')
  const problem = ref.trim() ? pullRefProblem(ref) : null
  const layer = [...steps].reverse().find((step) => step.step === 'pull_layer')

  async function start() {
    setPhase('running'); setSteps([]); setError('')
    try {
      const response = await fetch(`${resourcesPath(hostId, 'image')}/pull`, { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ ref: ref.trim() }) })
      if (!response.ok) throw new Error((await response.text()).trim().slice(0, 400) || 'The pull failed.')
      if (!response.body) throw new Error('This browser cannot show progress for a pull.')
      const reader = response.body.getReader()
      const decoder = new TextDecoder()
      let buffer = ''
      let finished = false
      for (;;) {
        const { value, done } = await reader.read()
        if (value) buffer += decoder.decode(value, { stream: !done })
        const drained = drainRecords<PullStep & { code?: string }>(buffer)
        buffer = drained.rest
        for (const record of drained.records) {
          if (record.step === 'error') throw new Error(record.message || 'The pull failed.')
          if (record.step === 'done') finished = true
          setSteps((current) => [...current, record])
        }
        if (done) break
      }
      if (!finished) throw new Error('The host stopped reporting before the pull finished.')
      setPhase('done'); onDone()
    } catch (cause) { setError(cause instanceof Error ? cause.message : 'The pull failed.'); setPhase('failed') }
  }
  const running = phase === 'running'
  return <Modal opened onClose={running ? () => undefined : onClose} title="Pull an image" centered>
    <Stack>
      {phase === 'ready' || phase === 'failed' ? <>
        <TextInput label="Image" placeholder="nginx:alpine or ghcr.io/owner/app:tag" value={ref} onChange={(event) => setRef(event.currentTarget.value)} error={problem ?? undefined} data-autofocus />
        <Text size="xs" c="dimmed">Pulls from public registries only for now; images that need a login cannot be pulled here.</Text>
        {phase === 'failed' && <Alert color="red" title="The pull did not complete">{error}</Alert>}
        <Group justify="flex-end"><Button variant="default" onClick={onClose}>Cancel</Button><Button disabled={!ref.trim() || problem !== null} onClick={() => void start()}>Pull</Button></Group></>
        : <>
          <List size="sm" spacing={4} center>{steps.filter((step) => step.step === 'pull' && step.message).slice(-4).map((step, index) => <List.Item key={index}>{step.message}</List.Item>)}</List>
          {running && layer && <Text size="xs" c="dimmed">{layer.message} {layer.total ? `${Math.min(100, Math.round(((layer.current ?? 0) / layer.total) * 100))}%` : ''}</Text>}
          {running && <Group><Loader size="sm" /><Text size="sm">Pulling…</Text></Group>}
          {phase === 'done' && <Alert color="green">Pulled {ref.trim()}.</Alert>}
          <Group justify="flex-end">{!running && <Button onClick={onClose}>Close</Button>}{running && <Text size="xs" c="dimmed">Keep this window open until it finishes.</Text>}</Group></>}
    </Stack>
  </Modal>
}
