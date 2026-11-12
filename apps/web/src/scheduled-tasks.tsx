import { useEffect, useState } from 'react'
import { Alert, Button, Group, NumberInput, Stack, Switch, Table, Text } from '@mantine/core'
import { untilTime } from './backup-utils'
import { relativeTime } from './overview-utils'
import { StatusText } from './status'
import { Panel } from './ui'

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

const interval = (seconds: number) => seconds >= 3600 ? `${seconds / 3600} h` : seconds >= 60 ? `${seconds / 60} min` : `${seconds} s`
// Tasks run every few seconds, so "due now" is too coarse below a minute.
const next = (at: string) => { const s = Math.round((Date.parse(at) - Date.now()) / 1000); return s <= 0 ? 'due now' : s < 60 ? `in ${s} s` : untilTime(at) }
const exact = (value: string | null) => value ? new Date(value).toLocaleString() : undefined
const statusName: Record<string, string> = { succeeded: 'Succeeded', failed: 'Failed', running: 'Running', idle: 'Not run yet' }

/** Server maintenance tasks: when each last ran, when it runs next, and a way to run it now. */
export function ScheduledTasks() {
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

  const bundles = tasks.find((task) => task.id === 'agent-bundles')
  return <Stack gap="md">
    <Text c="dimmed" size="sm">Tasks run when the server starts and then on their interval. A failed task retries within a minute; a running task never overlaps itself.</Text>
    {error && <Alert color="red" role="alert" withCloseButton onClose={() => setError('')}>{error}</Alert>}
    <Panel flush>
      {!loaded && !error ? <Text c="dimmed" size="sm" p="md">Loading scheduled tasks…</Text> : <div className="table-scroll"><Table className="vg-table" verticalSpacing={8}>
        <Table.Thead><Table.Tr><Table.Th>Task</Table.Th><Table.Th>Every</Table.Th><Table.Th>Last run</Table.Th><Table.Th>Next</Table.Th><Table.Th>Status</Table.Th><Table.Th ta="right">Enabled</Table.Th><Table.Th aria-label="Actions" /></Table.Tr></Table.Thead>
        <Table.Tbody>{tasks.map((task) => <Table.Tr key={task.id}>
          <Table.Td style={{ minWidth: 240 }}><b>{task.name}</b><Text size="xs" c="dimmed">{task.description}</Text>{task.lastError && <Text size="xs" c="red">{task.lastError}</Text>}</Table.Td>
          <Table.Td className="vg-num vg-nowrap">{interval(task.intervalSeconds)}</Table.Td>
          <Table.Td className="vg-dim vg-nowrap" title={exact(task.lastStartedAt)}>{relativeTime(task.lastStartedAt)}</Table.Td>
          <Table.Td className="vg-dim vg-nowrap" title={exact(task.nextRunAt)}>{task.enabled && task.available && task.nextRunAt ? next(task.nextRunAt) : '—'}</Table.Td>
          <Table.Td className="vg-nowrap"><StatusText tone={!task.available ? 'idle' : task.status === 'failed' ? 'crit' : task.status === 'running' ? 'info' : task.status === 'succeeded' ? 'ok' : 'idle'}>{!task.available ? 'Not configured' : statusName[task.status] ?? task.status}</StatusText></Table.Td>
          <Table.Td ta="right"><Switch aria-label={`Run ${task.name} on schedule`} checked={task.enabled} disabled={!task.available || busy === task.id} onChange={(event) => { void configure(task, event.currentTarget.checked, task.settings).catch(() => {}) }} style={{ display: 'inline-block' }} /></Table.Td>
          <Table.Td ta="right"><Button size="xs" variant="subtle" loading={busy === task.id} disabled={!task.available || task.status === 'running'} onClick={() => void run(task)}>Run now</Button></Table.Td>
        </Table.Tr>)}</Table.Tbody>
      </Table></div>}
    </Panel>
    {bundles && <BundleRetention task={bundles} busy={busy === 'agent-bundles'} save={configure} />}
    <Text size="xs" c="dimmed">Backup policy schedules are set on each policy; the backup task only queues the ones that are due. Notification delivery and deployments run continuously rather than on a schedule.</Text>
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
  return <Panel title="Agent bundle retention"><Stack gap="sm">
    <Text size="sm" c="dimmed">Vigara checks GitHub for agent bundles every 24 hours and on startup, and uses the newest one that matches this server. A pinned release stays pinned.</Text>
    {!task.available && <Alert color="yellow">Automatic agent downloads are not configured. Set VIGIRA_AGENT_RELEASE_TAG and a writable release cache on the API server.</Alert>}
    <Group align="flex-end" gap="lg" wrap="wrap">
      <Switch label="Prune old bundle versions" checked={purgeOld} onChange={(event) => { setPurgeOld(event.currentTarget.checked); setSaved(false) }} />
      <NumberInput label="Versions to keep" w={140} min={1} max={20} allowDecimal={false} value={keepVersions} disabled={!purgeOld} onChange={(value) => { setKeepVersions(value); setSaved(false) }} />
      <Button variant="default" loading={busy} disabled={!Number.isInteger(Number(keepVersions)) || Number(keepVersions) < 1 || Number(keepVersions) > 20} onClick={() => void submit()}>Save</Button>
      {saved && <Text size="sm" c="green" role="status">Saved. Applies on the next scan, or choose Run now.</Text>}
    </Group>
  </Stack></Panel>
}
