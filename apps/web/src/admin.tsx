import { useEffect, useState } from 'react'
import { Alert, Badge, Button, Card, Group, NumberInput, Stack, Switch, Table, Tabs, Text, Title } from '@mantine/core'

type Task = {
  id: string; name: string; description: string; available: boolean; enabled: boolean
  intervalSeconds: number; runOnStartup: boolean; status: string
  lastStartedAt: string | null; lastFinishedAt: string | null; nextRunAt: string | null; lastError: string
  settings: { purgeOld?: boolean; keepVersions?: number }
}

async function api<T>(path = '', method = 'GET', body?: unknown): Promise<T> {
  const response = await fetch(`/api/admin/scheduled-tasks${path}`, {
    method, credentials: 'same-origin', headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (!response.ok) throw new Error((await response.text()).slice(0, 300) || 'Scheduled task request failed.')
  return response.json() as Promise<T>
}

const when = (value: string | null) => value ? new Date(value).toLocaleString() : '—'
const interval = (seconds: number) => seconds >= 3600 ? `${seconds / 3600} hours` : seconds >= 60 ? `${seconds / 60} minutes` : `${seconds} seconds`

export function Admin({ role }: { role: string }) {
  if (role !== 'admin') return <Alert title="Administrator access required">Sign in as an administrator to manage scheduled tasks.</Alert>
  return <section><Stack gap="lg">
    <div><Title order={1}>Administration</Title><Text c="dimmed">Server maintenance and scheduling.</Text></div>
    <Tabs defaultValue="scheduled-tasks"><Tabs.List><Tabs.Tab value="scheduled-tasks">Scheduled tasks</Tabs.Tab></Tabs.List>
      <Tabs.Panel value="scheduled-tasks" pt="lg"><ScheduledTasks /></Tabs.Panel>
    </Tabs>
  </Stack></section>
}

function ScheduledTasks() {
  const [tasks, setTasks] = useState<Task[]>([])
  const [error, setError] = useState('')
  const [busy, setBusy] = useState('')
  const [loaded, setLoaded] = useState(false)
  useEffect(() => {
    let active = true
    let timer: number | undefined
    const load = async () => {
      try { const result = await api<Task[]>(); if (active) { setTasks(result); setLoaded(true) } }
      catch (cause) { if (active) setError(cause instanceof Error ? cause.message : 'Could not load scheduled tasks.') }
      finally { if (active) timer = window.setTimeout(() => void load(), 5000) }
    }
    void load()
    return () => { active = false; window.clearTimeout(timer) }
  }, [])

  async function run(task: Task) {
    setBusy(task.id); setError('')
    try { await api(`/${task.id}/run`, 'POST', {}); setTasks(await api<Task[]>()) }
    catch (cause) { setError(cause instanceof Error ? cause.message : 'Could not start task.') }
    finally { setBusy('') }
  }

  async function configure(task: Task, enabled: boolean, settings: Task['settings']) {
    setBusy(task.id); setError('')
    try { await api(`/${task.id}`, 'PATCH', { enabled, settings }); setTasks(await api<Task[]>()) }
    catch (cause) { setError(cause instanceof Error ? cause.message : 'Could not save task settings.'); throw cause }
    finally { setBusy('') }
  }

  return <Stack gap="lg">
    <Text>Every enabled, configured task runs on server startup, then on its interval. Failed tasks retry within one minute. Running tasks cannot overlap.</Text>
    {error && <Alert color="red" role="alert" withCloseButton onClose={() => setError('')}>{error}</Alert>}
    {!loaded && !error && <Text c="dimmed">Loading scheduled tasks…</Text>}
    <Table.ScrollContainer minWidth={1050}><Table striped highlightOnHover>
      <Table.Thead><Table.Tr><Table.Th>Task</Table.Th><Table.Th>Interval</Table.Th><Table.Th>Last started</Table.Th><Table.Th>Last finished</Table.Th><Table.Th>Next run</Table.Th><Table.Th>Status</Table.Th><Table.Th>Actions</Table.Th></Table.Tr></Table.Thead>
      <Table.Tbody>{tasks.map((task) => <Table.Tr key={task.id}>
        <Table.Td><Text fw={600}>{task.name}</Text><Text size="xs" c="dimmed">{task.description}</Text>{task.lastError && <Text size="xs" c="red">{task.lastError}</Text>}</Table.Td>
        <Table.Td>{interval(task.intervalSeconds)}</Table.Td>
        <Table.Td>{when(task.lastStartedAt)}</Table.Td><Table.Td>{when(task.lastFinishedAt)}</Table.Td><Table.Td>{when(task.nextRunAt)}</Table.Td>
        <Table.Td><Badge color={!task.available ? 'gray' : task.status === 'failed' ? 'red' : task.status === 'running' ? 'blue' : task.status === 'succeeded' ? 'green' : 'gray'}>{!task.available ? 'Not configured' : task.status}</Badge></Table.Td>
        <Table.Td><Stack gap="xs"><Button size="xs" loading={busy === task.id} disabled={!task.available || task.status === 'running'} onClick={() => void run(task)}>Run now</Button>
          <Switch label="Scheduled" checked={task.enabled} disabled={!task.available || busy === task.id} onChange={(event) => { void configure(task, event.currentTarget.checked, task.settings).catch(() => {}) }} /></Stack></Table.Td>
      </Table.Tr>)}</Table.Tbody>
    </Table></Table.ScrollContainer>
    {tasks.find((task) => task.id === 'agent-bundles') && <BundleRetention task={tasks.find((task) => task.id === 'agent-bundles')!} busy={busy === 'agent-bundles'} save={configure} />}
    <Text size="xs" c="dimmed">Times use your browser’s timezone. Backup policy schedules are configured separately; the scheduling task queues policies that are due. Notification delivery and deployment queues are continuous workers, rather than scheduled maintenance tasks.</Text>
  </Stack>
}

function BundleRetention({ task, busy, save }: { task: Task; busy: boolean; save: (task: Task, enabled: boolean, settings: Task['settings']) => Promise<void> }) {
  const [purgeOld, setPurgeOld] = useState(task.settings.purgeOld ?? true)
  const [keepVersions, setKeepVersions] = useState<string | number>(task.settings.keepVersions ?? 2)
  const [saved, setSaved] = useState(false)
  async function submit() {
    setSaved(false)
    try { await save(task, task.enabled, { purgeOld, keepVersions: Number(keepVersions) }); setSaved(true) } catch { /* The page displays the request error. */ }
  }
  return <Card withBorder padding="lg"><Stack gap="md">
    <Title order={3}>Agent bundle retention</Title>
    <Text size="sm">Scan GitHub every 24 hours and on startup. Automatic mode uses the newest complete bundle compatible with this server’s protocol and Restic version; an explicit release pin stays pinned.</Text>
    {!task.available && <Alert color="yellow">Automatic agent downloads are not configured. Enable VIGIRA_AGENT_RELEASE_TAG and a writable release cache on the API server.</Alert>}
    <Switch label="Purge old bundle versions" description="When disabled, downloaded versions remain cached." checked={purgeOld} onChange={(event) => { setPurgeOld(event.currentTarget.checked); setSaved(false) }} />
    <NumberInput label="Bundle versions to keep" description="Default: 2. Applies to the complete set of platform binaries for each release." min={1} max={20} allowDecimal={false} value={keepVersions} onChange={(value) => { setKeepVersions(value); setSaved(false) }} />
    <Group><Button loading={busy} disabled={!Number.isInteger(Number(keepVersions)) || Number(keepVersions) < 1 || Number(keepVersions) > 20} onClick={() => void submit()}>Save retention settings</Button>{saved && <Text size="sm" c="green" role="status">Saved. Applies on the next scan, or choose Run now.</Text>}</Group>
  </Stack></Card>
}
