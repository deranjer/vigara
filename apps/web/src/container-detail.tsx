import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { LineChart } from '@mantine/charts'
import { Alert, Badge, Button, Card, Checkbox, Code, Group, Loader, Select, SimpleGrid, Stack, Switch, Table, Tabs, Text, TextInput, Title } from '@mantine/core'
import { ActionBar } from './container-actions'
import { appendBounded, filterLogLines, formatBytes, formatRate, logsToText, portSummary, stateColors, type DockerPort, type HostEntry, type LogLine, type StatsSample } from './container-utils'
import { route as routeOf, type Route } from './route'
import { useStream } from './use-stream'

type InspectResult = {
  id: string; name: string; image: string; imageId?: string; created?: string
  state: { status: string; running: boolean; paused: boolean; restarting: boolean; oomKilled: boolean; exitCode: number; error?: string; startedAt?: string; finishedAt?: string; health?: string; pid?: number }
  command?: string[]; entrypoint?: string[]; workingDir?: string; user?: string; hostname?: string; tty: boolean
  env: { name: string; value?: string }[]; envRevealed: boolean; labels?: Record<string, string>
  mounts?: { type: string; name?: string; source?: string; destination: string; readWrite: boolean }[]
  ports?: DockerPort[]; networks?: { name: string; ipAddress?: string; gateway?: string; macAddress?: string; aliases?: string[] }[]
  restartPolicy?: string; networkMode?: string; privileged: boolean; readonlyRoot: boolean; memoryLimit?: number; nanoCpus?: number
  capAdd?: string[]; capDrop?: string[]; logDriver?: string; restartCount: number
}

type Props = { hostId: string; reference: string; tab: string; hosts: HostEntry[] | null; role: string; onNavigate: (route: Route) => void; refreshFleet: () => void }

const logLimit = 5000
const sampleLimit = 120

async function fetchInspect(hostId: string, reference: string, revealEnv: boolean): Promise<InspectResult> {
  const path = `/api/containers/${encodeURIComponent(hostId)}/${encodeURIComponent(reference)}/inspect${revealEnv ? '?reveal=env' : ''}`
  const response = await fetch(path, { credentials: 'same-origin' })
  if (!response.ok) throw new Error((await response.text()).trim().slice(0, 200) || 'The container could not be read.')
  return response.json() as Promise<InspectResult>
}

const when = (value?: string) => (value ? new Date(value).toLocaleString() : '–')
const field = (label: string, value: ReactNode) => <div key={label}><Text size="xs" c="dimmed" fw={600} tt="uppercase">{label}</Text><Text size="sm" style={{ wordBreak: 'break-word' }}>{value === undefined || value === null || value === '' ? '–' : value}</Text></div>

export function ContainerDetail({ hostId, reference, tab, hosts, role, onNavigate, refreshFleet }: Props) {
  const [inspect, setInspect] = useState<InspectResult | null>(null)
  const [error, setError] = useState('')
  const [revealing, setRevealing] = useState(false)
  const host = hosts?.find((entry) => entry.agentId === hostId)
  const hostName = host?.name ?? hostId.slice(0, 8)

  const load = useCallback(async (revealEnv: boolean) => {
    try { setInspect(await fetchInspect(hostId, reference, revealEnv)); setError('') } catch (cause) { setError(cause instanceof Error ? cause.message : 'The container could not be read.') }
  }, [hostId, reference])

  useEffect(() => {
    const timer = window.setTimeout(() => { void load(false) }, 0)
    return () => window.clearTimeout(timer)
  }, [load])

  const goTab = (value: string | null) => onNavigate(routeOf('containers', hostId, reference, value ?? 'overview'))
  const title = inspect?.name || reference
  const running = inspect ? inspect.state.running : null

  async function reveal(show: boolean) {
    setRevealing(true)
    await load(show)
    setRevealing(false)
  }

  return <>
    <Group mb="sm"><Button variant="default" size="xs" onClick={() => onNavigate(routeOf('containers'))}>← All containers</Button></Group>
    <Text size="xs" fw={700} c="dimmed" tt="uppercase">{hostName}</Text>
    <Group gap="sm" mt={4} align="center"><Title order={1}>{title}</Title>{inspect && <Badge size="lg" color={stateColors[inspect.state.status] ?? 'gray'} variant="light">{inspect.state.status}</Badge>}
      {inspect?.state.health && <Badge color={inspect.state.health === 'healthy' ? 'green' : 'orange'} variant="outline">{inspect.state.health}</Badge>}</Group>
    {inspect && <Text c="dimmed" mt={6} style={{ wordBreak: 'break-all' }}>{inspect.image}</Text>}
    {inspect && <ActionBar hostId={hostId} reference={reference} name={title} state={inspect.state.status} project={inspect.labels?.['com.docker.compose.project']} role={role} hostTier={host?.dockerTier}
      onChanged={() => { void load(false); refreshFleet() }}
      onRemoved={() => { refreshFleet(); onNavigate(routeOf('containers')) }}
      onRecreated={(newName) => { refreshFleet(); if (newName !== reference) onNavigate(routeOf('containers', hostId, newName, tab)); else void load(false) }} />}
    {error && <Alert color="red" mt="md" title="This container could not be loaded">{error} <Button variant="subtle" size="xs" onClick={() => void load(false)}>Try again</Button></Alert>}
    {!inspect && !error && <Group mt="lg"><Loader size="sm" /><Text c="dimmed">Loading…</Text></Group>}

    {inspect && <Tabs value={tab} onChange={goTab} mt="lg" keepMounted={false}>
      <Tabs.List><Tabs.Tab value="overview">Overview</Tabs.Tab><Tabs.Tab value="logs">Logs</Tabs.Tab><Tabs.Tab value="stats">Stats</Tabs.Tab><Tabs.Tab value="inspect">Inspect</Tabs.Tab></Tabs.List>
      <Tabs.Panel value="overview" pt="md"><Overview inspect={inspect} admin={role === 'admin'} revealing={revealing} onReveal={reveal} /></Tabs.Panel>
      <Tabs.Panel value="logs" pt="md"><LogsPanel hostId={hostId} reference={reference} name={title} /></Tabs.Panel>
      <Tabs.Panel value="stats" pt="md"><StatsPanel hostId={hostId} reference={reference} running={running} /></Tabs.Panel>
      <Tabs.Panel value="inspect" pt="md"><Card withBorder radius="md" padding="md"><Text size="sm" c="dimmed" mb="xs">Environment values are {inspect.envRevealed ? 'shown' : 'hidden unless revealed on the Overview tab'}.</Text><Code block style={{ maxHeight: 560, overflow: 'auto' }}>{JSON.stringify(inspect, null, 2)}</Code></Card></Tabs.Panel>
    </Tabs>}
  </>
}

function Overview({ inspect, admin, revealing, onReveal }: { inspect: InspectResult; admin: boolean; revealing: boolean; onReveal: (show: boolean) => Promise<void> }) {
  const s = inspect.state
  const cpuLimit = inspect.nanoCpus ? `${(inspect.nanoCpus / 1e9).toFixed(2)} cores` : 'Unlimited'
  return <Stack gap="md">
    <SimpleGrid cols={{ base: 1, md: 3 }}>
      <Card withBorder radius="md" padding="md"><Title order={5} mb="sm">State</Title><Stack gap="xs">
        {field('Status', s.status)}{field('Started', when(s.startedAt))}{field('Finished', when(s.finishedAt))}{field('Exit code', s.running ? '–' : s.exitCode)}
        {field('Restarts', inspect.restartCount)}{s.oomKilled && field('Out of memory', 'The container was killed for exceeding its memory')}{s.error && field('Error', s.error)}</Stack></Card>
      <Card withBorder radius="md" padding="md"><Title order={5} mb="sm">Configuration</Title><Stack gap="xs">
        {field('Command', inspect.command?.join(' '))}{field('Entrypoint', inspect.entrypoint?.join(' '))}{field('Working directory', inspect.workingDir)}{field('User', inspect.user)}
        {field('Restart policy', inspect.restartPolicy)}{field('Network mode', inspect.networkMode)}{field('Created', when(inspect.created))}</Stack></Card>
      <Card withBorder radius="md" padding="md"><Title order={5} mb="sm">Resources and safety</Title><Stack gap="xs">
        {field('Memory limit', inspect.memoryLimit ? formatBytes(inspect.memoryLimit) : 'Unlimited')}{field('CPU limit', cpuLimit)}
        {field('Privileged', inspect.privileged ? <Badge color="red" variant="light">Yes — full host access</Badge> : 'No')}{field('Read-only root', inspect.readonlyRoot ? 'Yes' : 'No')}
        {field('Added capabilities', inspect.capAdd?.join(', '))}{field('Dropped capabilities', inspect.capDrop?.join(', '))}{field('Log driver', inspect.logDriver)}</Stack></Card>
    </SimpleGrid>

    <Card withBorder radius="md" padding="md"><Title order={5} mb="sm">Ports</Title>
      {inspect.ports?.length ? <Table verticalSpacing="xs"><Table.Thead><Table.Tr><Table.Th>Container port</Table.Th><Table.Th>Published</Table.Th><Table.Th>Bound to</Table.Th></Table.Tr></Table.Thead>
        <Table.Tbody>{inspect.ports.map((port, index) => <Table.Tr key={index}><Table.Td>{port.privatePort}/{port.type}</Table.Td><Table.Td>{port.publicPort ? port.publicPort : 'Not published'}</Table.Td><Table.Td>{port.ip || '–'}</Table.Td></Table.Tr>)}</Table.Tbody></Table>
        : <Text size="sm" c="dimmed">No ports.</Text>}</Card>

    <Card withBorder radius="md" padding="md"><Title order={5} mb="sm">Mounts</Title>
      {inspect.mounts?.length ? <Table.ScrollContainer minWidth={520}><Table verticalSpacing="xs"><Table.Thead><Table.Tr><Table.Th>Type</Table.Th><Table.Th>Source</Table.Th><Table.Th>Destination</Table.Th><Table.Th>Access</Table.Th></Table.Tr></Table.Thead>
        <Table.Tbody>{inspect.mounts.map((mount, index) => <Table.Tr key={index}><Table.Td>{mount.type}</Table.Td><Table.Td style={{ wordBreak: 'break-all' }}>{mount.name || mount.source || '–'}</Table.Td><Table.Td style={{ wordBreak: 'break-all' }}>{mount.destination}</Table.Td><Table.Td>{mount.readWrite ? 'Read-write' : 'Read-only'}</Table.Td></Table.Tr>)}</Table.Tbody></Table></Table.ScrollContainer>
        : <Text size="sm" c="dimmed">No mounts.</Text>}</Card>

    <Card withBorder radius="md" padding="md"><Title order={5} mb="sm">Networks</Title>
      {inspect.networks?.length ? <Table.ScrollContainer minWidth={520}><Table verticalSpacing="xs"><Table.Thead><Table.Tr><Table.Th>Network</Table.Th><Table.Th>IP address</Table.Th><Table.Th>Gateway</Table.Th><Table.Th>Aliases</Table.Th></Table.Tr></Table.Thead>
        <Table.Tbody>{inspect.networks.map((network) => <Table.Tr key={network.name}><Table.Td>{network.name}</Table.Td><Table.Td>{network.ipAddress || '–'}</Table.Td><Table.Td>{network.gateway || '–'}</Table.Td><Table.Td>{network.aliases?.join(', ') || '–'}</Table.Td></Table.Tr>)}</Table.Tbody></Table></Table.ScrollContainer>
        : <Text size="sm" c="dimmed">Not attached to a network.</Text>}</Card>

    <Card withBorder radius="md" padding="md">
      <Group justify="space-between" mb="sm"><Title order={5}>Environment</Title>
        {admin ? (inspect.envRevealed ? <Button size="xs" variant="default" loading={revealing} onClick={() => void onReveal(false)}>Hide values</Button>
          : <Button size="xs" variant="light" color="orange" loading={revealing} onClick={() => void onReveal(true)}>Reveal values</Button>) : null}</Group>
      {inspect.envRevealed ? <Alert color="orange" mb="sm" title="Values are visible">Revealing environment values is recorded in the audit log. Hide them again when you are done.</Alert>
        : <Text size="sm" c="dimmed" mb="sm">{admin ? 'Values are hidden because they often hold passwords and keys. Revealing them is recorded in the audit log.' : 'Values are hidden. Only administrators can reveal them.'}</Text>}
      {inspect.env.length === 0 ? <Text size="sm" c="dimmed">No environment variables.</Text>
        : <Table.ScrollContainer minWidth={420}><Table verticalSpacing="xs"><Table.Thead><Table.Tr><Table.Th>Name</Table.Th><Table.Th>Value</Table.Th></Table.Tr></Table.Thead>
          <Table.Tbody>{inspect.env.map((item) => <Table.Tr key={item.name}><Table.Td><Code>{item.name}</Code></Table.Td><Table.Td style={{ wordBreak: 'break-all' }}>{inspect.envRevealed ? (item.value === '' ? <Text size="sm" c="dimmed">(empty)</Text> : <Code>{item.value}</Code>) : <Text size="sm" c="dimmed">••••••••</Text>}</Table.Td></Table.Tr>)}</Table.Tbody></Table></Table.ScrollContainer>}
    </Card>

    {inspect.labels && Object.keys(inspect.labels).length > 0 && <Card withBorder radius="md" padding="md"><Title order={5} mb="sm">Labels</Title>
      <Table.ScrollContainer minWidth={420}><Table verticalSpacing="xs"><Table.Tbody>{Object.entries(inspect.labels).sort(([a], [b]) => a.localeCompare(b)).map(([key, value]) => <Table.Tr key={key}><Table.Td style={{ wordBreak: 'break-all' }}><Code>{key}</Code></Table.Td><Table.Td style={{ wordBreak: 'break-all' }}>{value}</Table.Td></Table.Tr>)}</Table.Tbody></Table></Table.ScrollContainer></Card>}
    {inspect.ports?.length ? <Text size="xs" c="dimmed">Published: {portSummary(inspect.ports, 6) || 'none'}</Text> : null}
  </Stack>
}

const statusBadge: Record<string, { color: string; label: string }> = {
  idle: { color: 'gray', label: 'Idle' }, connecting: { color: 'blue', label: 'Connecting…' }, live: { color: 'green', label: 'Live' }, ended: { color: 'gray', label: 'Ended' }, failed: { color: 'red', label: 'Disconnected' },
}

function LogsPanel({ hostId, reference, name }: { hostId: string; reference: string; name: string }) {
  const [tail, setTail] = useState<string | null>('200')
  const [follow, setFollow] = useState(true)
  const [timestamps, setTimestamps] = useState(false)
  const [wrap, setWrap] = useState(true)
  const [search, setSearch] = useState('')
  const [lines, setLines] = useState<LogLine[]>([])
  const [stick, setStick] = useState(true)
  const viewport = useRef<HTMLDivElement>(null)

  const path = useMemo(() => `/api/containers/${encodeURIComponent(hostId)}/${encodeURIComponent(reference)}/logs?tail=${tail ?? '200'}&follow=${follow}&timestamps=${timestamps}`, [hostId, reference, tail, follow, timestamps])
  // A new set of options is a new stream, so the old lines no longer apply.
  const [seenPath, setSeenPath] = useState(path)
  if (seenPath !== path) { setSeenPath(path); setLines([]) }

  const stream = useStream<LogLine>(path, (records) => setLines((current) => appendBounded(current, records, logLimit)))
  const visible = useMemo(() => filterLogLines(lines, search), [lines, search])

  useEffect(() => {
    const element = viewport.current
    if (element && stick) element.scrollTop = element.scrollHeight
  }, [visible, stick])

  function download() {
    const url = URL.createObjectURL(new Blob([logsToText(visible)], { type: 'text/plain' }))
    const link = document.createElement('a')
    link.href = url; link.download = `${name || 'container'}.log`; link.click()
    window.setTimeout(() => URL.revokeObjectURL(url), 30_000)
  }

  const badge = statusBadge[stream.status] ?? statusBadge.idle
  return <Stack gap="sm">
    <Group gap="sm" align="flex-end">
      <Select label="Lines" value={tail} onChange={setTail} allowDeselect={false} w={110} data={['100', '200', '500', '1000', '5000']} />
      <TextInput label="Filter" placeholder="Show only lines containing…" value={search} onChange={(event) => setSearch(event.currentTarget.value)} style={{ flex: '1 1 200px' }} />
      <Switch label="Follow" checked={follow} onChange={(event) => setFollow(event.currentTarget.checked)} mb={6} />
      <Switch label="Timestamps" checked={timestamps} onChange={(event) => setTimestamps(event.currentTarget.checked)} mb={6} />
      <Switch label="Wrap" checked={wrap} onChange={(event) => setWrap(event.currentTarget.checked)} mb={6} />
    </Group>
    <Group justify="space-between">
      <Group gap="xs"><Badge color={badge.color} variant="light">{badge.label}</Badge><Text size="xs" c="dimmed">{visible.length}{search ? ` of ${lines.length}` : ''} lines{lines.length >= logLimit ? ` (latest ${logLimit})` : ''}</Text></Group>
      <Group gap="xs">
        {!stick && <Button size="xs" variant="light" onClick={() => setStick(true)}>Jump to latest</Button>}
        <Button size="xs" variant="default" onClick={() => setLines([])}>Clear</Button>
        <Button size="xs" variant="default" disabled={visible.length === 0} onClick={download}>Download</Button>
        {(stream.status === 'failed' || stream.status === 'ended') && <Button size="xs" variant="light" onClick={() => { setLines([]); stream.reconnect() }}>Reload</Button>}
      </Group>
    </Group>
    {stream.status === 'failed' && <Alert color="red" title="The log stream stopped">{stream.message}</Alert>}
    {stream.status === 'ended' && stream.message && <Alert color="yellow" title="The log stream ended">{stream.message}</Alert>}
    <div ref={viewport} role="log" aria-label={`Logs for ${name}`} tabIndex={0}
      onScroll={(event) => { const el = event.currentTarget; setStick(el.scrollHeight - el.scrollTop - el.clientHeight < 24) }}
      style={{ height: 480, overflow: 'auto', background: 'var(--mantine-color-dark-8)', color: 'var(--mantine-color-gray-3)', borderRadius: 8, padding: 12, fontFamily: 'var(--mantine-font-family-monospace)', fontSize: 12.5, lineHeight: 1.5 }}>
      {visible.length === 0 ? <Text size="sm" c="dimmed">{stream.status === 'connecting' ? 'Connecting…' : lines.length === 0 ? 'No log lines yet.' : 'No lines match the filter.'}</Text>
        : visible.map((line, index) => <div key={index} style={{ whiteSpace: wrap ? 'pre-wrap' : 'pre', wordBreak: wrap ? 'break-word' : 'normal', color: line.s === 'e' ? 'var(--mantine-color-red-4)' : undefined }}>
          {line.t && <span style={{ color: 'var(--mantine-color-dimmed)' }}>{line.t} </span>}{line.m}{line.x && <span style={{ color: 'var(--mantine-color-yellow-5)' }}> …[line cut]</span>}</div>)}
    </div>
    <Checkbox size="xs" checked={stick} onChange={(event) => setStick(event.currentTarget.checked)} label="Keep scrolled to the newest line" />
  </Stack>
}

function StatsPanel({ hostId, reference, running }: { hostId: string; reference: string; running: boolean | null }) {
  const [samples, setSamples] = useState<StatsSample[]>([])
  const path = running ? `/api/containers/${encodeURIComponent(hostId)}/${encodeURIComponent(reference)}/stats` : null
  const stream = useStream<StatsSample>(path, (records) => setSamples((current) => appendBounded(current, records, sampleLimit)))
  const latest = samples[samples.length - 1]
  const rows = useMemo(() => samples.map((sample) => ({ time: new Date(sample.t).toLocaleTimeString(), cpu: Number(sample.cpu.toFixed(2)), memory: Number(sample.memPct.toFixed(2)) })), [samples])

  if (running === false) return <Alert color="gray" title="This container is not running">Live resource use is only available while a container is running.</Alert>
  const badge = statusBadge[stream.status] ?? statusBadge.idle
  return <Stack gap="md">
    <Group gap="xs"><Badge color={badge.color} variant="light">{badge.label}</Badge><Text size="xs" c="dimmed">Updates about once a second; the last {sampleLimit} readings are shown.</Text></Group>
    {stream.status === 'failed' && <Alert color="red" title="The statistics stream stopped">{stream.message}</Alert>}
    {stream.status === 'ended' && stream.message && <Alert color="yellow" title="The statistics stream ended">{stream.message}</Alert>}
    <SimpleGrid cols={{ base: 2, md: 5 }}>
      <Card withBorder radius="md" padding="sm"><Text size="xs" c="dimmed" fw={600} tt="uppercase">CPU</Text><Text fw={700} size="lg">{latest ? `${latest.cpu.toFixed(1)}%` : '–'}</Text><Text size="xs" c="dimmed">of one core{latest ? ` (${latest.cpuCores} available)` : ''}</Text></Card>
      <Card withBorder radius="md" padding="sm"><Text size="xs" c="dimmed" fw={600} tt="uppercase">Memory</Text><Text fw={700} size="lg">{latest ? formatBytes(latest.memUsed) : '–'}</Text><Text size="xs" c="dimmed">{latest ? `${latest.memPct.toFixed(1)}% of ${latest.memLimit ? formatBytes(latest.memLimit) : 'unlimited'}` : ''}</Text></Card>
      <Card withBorder radius="md" padding="sm"><Text size="xs" c="dimmed" fw={600} tt="uppercase">Network</Text><Text fw={700} size="sm">↓ {formatRate(latest?.netRxBps)}</Text><Text fw={700} size="sm">↑ {formatRate(latest?.netTxBps)}</Text></Card>
      <Card withBorder radius="md" padding="sm"><Text size="xs" c="dimmed" fw={600} tt="uppercase">Disk I/O (total)</Text><Text fw={700} size="sm">Read {latest ? formatBytes(latest.blkRead) : '–'}</Text><Text fw={700} size="sm">Write {latest ? formatBytes(latest.blkWrite) : '–'}</Text></Card>
      <Card withBorder radius="md" padding="sm"><Text size="xs" c="dimmed" fw={600} tt="uppercase">Processes</Text><Text fw={700} size="lg">{latest ? latest.pids : '–'}</Text></Card>
    </SimpleGrid>
    {rows.length > 1 && <SimpleGrid cols={{ base: 1, md: 2 }}>
      <Card withBorder radius="md" padding="md"><Title order={6} mb="xs">CPU (% of one core)</Title><LineChart h={180} data={rows} dataKey="time" series={[{ name: 'cpu', color: 'blue.6', label: 'CPU %' }]} withDots={false} curveType="linear" yAxisProps={{ domain: [0, 'auto'] }} /></Card>
      <Card withBorder radius="md" padding="md"><Title order={6} mb="xs">Memory (% of limit)</Title><LineChart h={180} data={rows} dataKey="time" series={[{ name: 'memory', color: 'teal.6', label: 'Memory %' }]} withDots={false} curveType="linear" yAxisProps={{ domain: [0, 100] }} /></Card>
    </SimpleGrid>}
  </Stack>
}
