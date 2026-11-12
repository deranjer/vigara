import { useEffect, useState } from 'react'
import { Alert, Group, Loader, Stack, Table, Text } from '@mantine/core'
import { getJson } from './device-types'
import { formatMetric } from './metric-utils'
import { StatusText } from './status'
import { Panel } from './ui'

type BackupProgress = { observedAt: string; phase: string; filesDone?: number; filesTotal?: number; bytesDone?: number; bytesTotal?: number; percentDone?: number }
type BackupJob = { id: string; profileId: string; scheduledFor: string; status: string; executionStatus: string; outcome?: string; snapshotIds?: string[]; errorCode?: string; errorMessage?: string; progress?: BackupProgress }
type BackupPolicy = { deviceId: string; id: string; name: string; profileId: string }

const timeLabel = (value?: string | null) => value ? new Date(value).toLocaleString() : 'Never'
const executionColor = (status: string) => ['running', 'succeeded'].includes(status) ? 'green' : ['stale', 'unknown'].includes(status) ? 'yellow' : ['failed', 'partial', 'interrupted', 'canceled', 'timed_out'].includes(status) ? 'red' : 'gray'

/** Recent runs of every backup policy, or of one device's policies. Refreshes every 15 seconds. */
export function BackupHistory({ refresh = 0, deviceId }: { refresh?: number; deviceId?: string }) {
  const [rows, setRows] = useState<{ policy: BackupPolicy; jobs: BackupJob[] }[]>([])
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  useEffect(() => {
    let active = true
    const load = async () => {
      try {
        const policies: BackupPolicy[] = []
        for (let offset = 0; ; offset += 100) {
          const response = await fetch(`/api/backups/policies?limit=100&offset=${offset}`, { credentials: 'same-origin' })
          if (!response.ok) throw new Error('Could not load backup policies.')
          policies.push(...await response.json() as BackupPolicy[])
          if (response.headers.get('X-Has-More') !== 'true') break
        }
        const result = await Promise.all(policies.filter((policy) => !deviceId || policy.deviceId === deviceId).map(async (policy) => ({ policy, jobs: await getJson<BackupJob[]>(`/api/backups/policies/${encodeURIComponent(policy.id)}/history?limit=20`) })))
        if (active) { setRows(result); setError('') }
      } catch (cause) { if (active) setError(cause instanceof Error ? cause.message : 'Could not load backup history.') }
      finally { if (active) setLoading(false) }
    }
    void load()
    const timer = window.setInterval(() => { void load() }, 15_000)
    return () => { active = false; window.clearInterval(timer) }
  }, [refresh, deviceId])
  return <Panel title="Backup history" actions={<Text size="xs" c="dimmed">refreshes every 15 s</Text>}>
    {error ? <Alert color="red" title="Backup history could not be loaded">{error}</Alert>
      : loading ? <Group><Loader size="xs" /><Text size="sm" c="dimmed">Loading backup history…</Text></Group>
      : rows.length === 0 ? <Text c="dimmed" size="sm">{deviceId ? 'No backup policies are configured for this device.' : 'No backup policies are configured.'}</Text>
      : <Stack gap="md">{rows.map(({ policy, jobs }) => <div key={policy.id}>
        <Group justify="space-between" mb="xs"><Text fw={600}>{policy.name}</Text><Text size="xs" c="dimmed">Profile {policy.profileId}</Text></Group>
        {jobs.length === 0 ? <Text size="sm" c="dimmed">No scheduled runs yet.</Text> : <Table.ScrollContainer minWidth={650}><Table className="vg-table">
          <Table.Thead><Table.Tr><Table.Th>Scheduled</Table.Th><Table.Th>Execution</Table.Th><Table.Th>Outcome</Table.Th><Table.Th>Progress</Table.Th><Table.Th>Snapshot</Table.Th><Table.Th>Details</Table.Th></Table.Tr></Table.Thead>
          <Table.Tbody>{jobs.map((job) => <Table.Tr key={job.id}>
            <Table.Td>{timeLabel(job.scheduledFor)}</Table.Td>
            <Table.Td><StatusText color={executionColor(job.executionStatus)}>{job.executionStatus}</StatusText></Table.Td>
            <Table.Td>{job.outcome || job.status}</Table.Td>
            <Table.Td>{job.progress ? <Text size="sm">{job.progress.phase}{job.progress.percentDone ? ` · ${job.progress.percentDone.toFixed(1)}%` : ''}{job.progress.filesTotal ? ` · ${job.progress.filesDone ?? 0}/${job.progress.filesTotal} files` : ''}{job.progress.bytesTotal ? ` · ${formatMetric(job.progress.bytesDone ?? 0, 'By')}/${formatMetric(job.progress.bytesTotal, 'By')}` : ''}</Text> : '—'}</Table.Td>
            <Table.Td>{job.snapshotIds?.length ? job.snapshotIds.map((id) => <Text key={id} size="xs" ff="monospace">{id}</Text>) : '—'}</Table.Td>
            <Table.Td>{job.errorMessage ? <Text size="xs" c="red">{job.errorMessage}{job.errorCode ? ` (${job.errorCode})` : ''}</Text> : job.errorCode || '—'}</Table.Td>
          </Table.Tr>)}</Table.Tbody>
        </Table></Table.ScrollContainer>}
      </div>)}</Stack>}
  </Panel>
}
