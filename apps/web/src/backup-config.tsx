import { useEffect, useState } from 'react'
import { Alert, Badge, Button, Card, Checkbox, Group, Modal, MultiSelect, PasswordInput, Select, Stack, Table, Text, TextInput, Title } from '@mantine/core'

import { BackupPathInput } from './backup-path-input'
import { RepositoryPathInput } from './repository-path-input'
import { isAbsoluteBackupPath } from './backup-path-utils'
import { formatBytes } from './container-utils'

const weekdays = ['Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday', 'Sunday']
export type BackupConfig = {
  repository: string; serverRepository: string; sourcePaths: string[]; password?: string; initialize: boolean
  schedule: { weekdays: string[]; time: string; timezone: string }; enabled: boolean; expectedRevision: number
}
export const newBackupConfig = (): BackupConfig => ({ repository: '', serverRepository: '', sourcePaths: [], initialize: true, schedule: { weekdays, time: '02:00', timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC' }, enabled: true, expectedRevision: 0 })

export function BackupFields({ value, onChange, existing = false, disabled = false, onPathsPendingChange }: { value: BackupConfig; onChange: (value: BackupConfig) => void; existing?: boolean; disabled?: boolean; onPathsPendingChange?: (pending: boolean) => void }) {
  const update = (patch: Partial<BackupConfig>) => onChange({ ...value, ...patch })
  return <Stack gap="sm">
    <BackupPathInput paths={value.sourcePaths} onChange={(sourcePaths) => update({ sourcePaths })} disabled={disabled} onPendingChange={onPathsPendingChange} />
    <RepositoryPathInput value={value.repository} onChange={(repository) => update({ repository })} disabled={existing || disabled} />
    <TextInput label="Repository path on the server (optional)" description="Where the server can access the same repository. Required for browsing and downloads." value={value.serverRepository} onChange={(e) => update({ serverRepository: e.currentTarget.value })} disabled={disabled} />
    {!existing && <><Checkbox label="Create a new encrypted repository" checked={value.initialize} onChange={(e) => update({ initialize: e.currentTarget.checked, password: '' })} disabled={disabled} />
      {!value.initialize && <PasswordInput label="Existing repository password" description={value.expectedRevision > 0 ? 'Leave blank to keep the saved password.' : undefined} value={value.password ?? ''} onChange={(e) => update({ password: e.currentTarget.value })} disabled={disabled} required />}
      <Text size="xs" c="dimmed">New repositories receive a generated password. Vigara stores it encrypted and provisions it to the device. Export the recovery information after setup.</Text></>}
    <MultiSelect label="Backup days" data={weekdays} value={value.schedule.weekdays} onChange={(days) => update({ schedule: { ...value.schedule, weekdays: days } })} disabled={disabled} />
    <Group grow><TextInput type="time" label="Time" value={value.schedule.time} onChange={(e) => update({ schedule: { ...value.schedule, time: e.currentTarget.value } })} disabled={disabled} />
      <TextInput label="Timezone" value={value.schedule.timezone} onChange={(e) => update({ schedule: { ...value.schedule, timezone: e.currentTarget.value } })} disabled={disabled} /></Group>
    <Checkbox label="Enable scheduled backups" checked={value.enabled} onChange={(e) => update({ enabled: e.currentTarget.checked })} disabled={disabled} />
  </Stack>
}

export type ManagedView = { configured: boolean; initialized: boolean; available: boolean; revision: number; appliedRevision: number; errorCode?: string; reportedAt?: string; config?: BackupConfig }
type Snapshot = { id: string; time: string; hostname?: string; paths: string[] }
type Entry = { name: string; type: string; path: string; size?: number }
async function json<T>(url: string, init?: RequestInit): Promise<T> {
  const response = await fetch(url, { credentials: 'same-origin', ...init })
  if (!response.ok) throw new Error((await response.text()).trim().slice(0, 300) || 'Backup request failed.')
  return response.json() as Promise<T>
}
export const setupErrors: Record<string, string> = {
  source_unavailable: 'A selected folder cannot be read by the agent.', repository_unavailable: 'The repository cannot be opened or initialized. Check storage access and the repository password.',
  restic_unavailable: 'The bundled restic executable is missing. Install a fresh backup-enabled agent bundle.', invalid_configuration: 'The selected paths are invalid or include the backup repository.', state_unavailable: 'The agent could not save its private configuration.',
}

export function DeviceBackups({ deviceId }: { deviceId: string }) {
  const base = `/api/backups/devices/${encodeURIComponent(deviceId)}`
  const [view, setView] = useState<ManagedView | null>(null)
  const [error, setError] = useState('')
  const [draft, setDraft] = useState<BackupConfig | null>(null)
  const [busy, setBusy] = useState(false)
  const [pathsPending, setPathsPending] = useState(false)
  const [refresh, setRefresh] = useState(0)
  const [browsing, setBrowsing] = useState(false)
  useEffect(() => {
    let active = true
    const load = () => { void json<ManagedView>(`${base}/managed`).then((v) => { if (active) { setView(v); setError('') } }).catch((e: unknown) => { if (active) setError(e instanceof Error ? e.message : 'Could not load backups.') }) }
    load(); const timer = window.setInterval(load, 15000)
    return () => { active = false; window.clearInterval(timer) }
  }, [base, refresh])
  const save = async () => {
    if (!draft) return
    setBusy(true); setError('')
    try {
      const value = { ...draft, sourcePaths: draft.sourcePaths.map((p) => p.trim()).filter(Boolean) }
      const result = await json<ManagedView>(`${base}/managed`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(value) })
      setView(result); setDraft(null); setRefresh((n) => n + 1)
    } catch (e) { setError(e instanceof Error ? e.message : 'Could not save backups.') }
    finally { setBusy(false) }
  }
  const ready = !!view?.configured && view.revision === view.appliedRevision && !view.errorCode
  return <Card withBorder radius="md" padding="lg" mb="lg"><Stack>
    <Group justify="space-between"><Title order={3}>Backup setup</Title>{view?.configured && <Badge color={ready ? 'green' : view.errorCode ? 'red' : 'blue'}>{ready ? 'Ready' : view.errorCode ? 'Needs attention' : 'Changes pending'}</Badge>}</Group>
    {error && <Alert color="red">{error}</Alert>}
    {view && !view.available && <Alert color="yellow">The server needs a backup master key before managed backups can be configured.</Alert>}
    {view?.config ? <><Text size="sm">{view.config.sourcePaths.join(', ')} → {view.config.repository}</Text>
      <Text size="sm" c="dimmed">{view.config.enabled ? `${view.config.schedule.weekdays.join(', ')} at ${view.config.schedule.time} (${view.config.schedule.timezone})` : 'Scheduled backups paused'} · Desired revision {view.revision}, applied {view.appliedRevision}</Text>
      {view.errorCode && <Alert color="red">{setupErrors[view.errorCode] ?? 'The agent could not apply the backup configuration.'}</Alert>}
      {!ready && !view.errorCode && <Text size="sm">Waiting for the device to apply these settings. An existing installation may need a fresh backup-enabled agent bundle.</Text>}</> : <Text size="sm" c="dimmed">Choose folders and a schedule. The backup-enabled agent configures restic automatically.</Text>}
    <Group><Button disabled={!view?.available} onClick={() => { setError(''); setDraft(view?.config ? { ...view.config, expectedRevision: view.revision } : newBackupConfig()) }}>{view?.configured ? 'Edit backup settings' : 'Set up backups'}</Button>
      {view?.config?.serverRepository && <Button variant="light" onClick={() => setBrowsing((v) => !v)}>{browsing ? 'Close browser' : 'Browse snapshots'}</Button>}
      {view?.configured && <Button component="a" variant="default" href={`${base}/managed/recovery`}>Export recovery information</Button>}</Group>
    {browsing && <SnapshotBrowser key={base} base={base} />}
    <Modal opened={!!draft} onClose={() => { if (!busy) setDraft(null) }} title="Backup settings" size="lg">
      {draft && <Stack>{error && <Alert color="red">{error}</Alert>}<BackupFields value={draft} onChange={setDraft} existing={view?.initialized} disabled={busy} onPathsPendingChange={setPathsPending} /><Text size="xs" c="dimmed">Changes apply after the device connects. Backups already running must finish before settings can be changed.</Text><Button loading={busy} disabled={pathsPending || !draft.sourcePaths.length || !isAbsoluteBackupPath(draft.repository)} onClick={() => void save()}>Save backup settings</Button></Stack>}
    </Modal>
  </Stack></Card>
}

/** Read-only browsing of a device's snapshots; `snapshotId` opens that snapshot without the picker. */
export function SnapshotBrowser({ base, snapshotId }: { base: string; snapshotId?: string }) {
  const [snapshots, setSnapshots] = useState<Snapshot[]>([])
  const [snapshot, setSnapshot] = useState<string | null>(null)
  const [directory, setDirectory] = useState('/')
  const [entries, setEntries] = useState<Entry[]>([])
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  useEffect(() => {
    let active = true
    void json<Snapshot[]>(`${base}/snapshots`).then((v) => { if (active) { const sorted = [...v].sort((a, b) => b.time.localeCompare(a.time)); setSnapshots(sorted); setSnapshot(snapshotId && sorted.some((x) => x.id === snapshotId) ? snapshotId : sorted[0]?.id ?? null); setLoading(false) } }).catch((e: unknown) => { if (active) { setError(e instanceof Error ? e.message : 'Could not load snapshots.'); setLoading(false) } })
    return () => { active = false }
  }, [base, snapshotId])
  useEffect(() => {
    if (!snapshot) return
    let active = true
    setLoading(true); setEntries([]); setError('')
    void json<Entry[]>(`${base}/snapshots/${snapshot}/entries?path=${encodeURIComponent(directory)}`).then((v) => { if (active) setEntries(v) }).catch((e: unknown) => { if (active) setError(e instanceof Error ? e.message : 'Could not browse snapshot.') }).finally(() => { if (active) setLoading(false) })
    return () => { active = false }
  }, [base, snapshot, directory])
  const download = (path: string) => `${base}/snapshots/${snapshot}/download?path=${encodeURIComponent(path)}`
  return <Stack>
    {!snapshotId && <Title order={4}>Snapshots</Title>}{error && <Alert color="red">{error}</Alert>}
    {!snapshotId && <Select label="Snapshot" value={snapshot} onChange={(v) => { setSnapshot(v); setDirectory('/') }} data={snapshots.map((s) => ({ value: s.id, label: `${new Date(s.time).toLocaleString()} · ${s.hostname ?? ''} · ${s.id.slice(0, 12)}` }))} />}
    {snapshot && <><Group><Button size="xs" variant="default" disabled={directory === '/' || loading} onClick={() => setDirectory(directory.slice(0, directory.lastIndexOf('/')) || '/')}>Up</Button><Text size="sm">{directory}</Text><Button size="xs" component="a" href={download(directory)}>Download folder ZIP</Button></Group>
      <Table><Table.Thead><Table.Tr><Table.Th>Name</Table.Th><Table.Th>Size</Table.Th><Table.Th /></Table.Tr></Table.Thead><Table.Tbody>{entries.map((entry) => <Table.Tr key={entry.path}><Table.Td>{entry.type === 'dir' ? <Button variant="subtle" size="xs" onClick={() => setDirectory(entry.path)}>{entry.name}/</Button> : entry.name}</Table.Td><Table.Td className="vg-num">{entry.type === 'file' ? formatBytes(entry.size ?? 0) : '—'}</Table.Td><Table.Td>{['file', 'dir'].includes(entry.type) && <Button component="a" href={download(entry.path)} variant="light" size="xs">Download{entry.type === 'dir' ? ' ZIP' : ''}</Button>}</Table.Td></Table.Tr>)}</Table.Tbody></Table></>}
    <Text size="xs" c="dimmed">{loading ? 'Loading…' : !snapshots.length ? 'No snapshots found.' : !entries.length ? 'No entries in this folder.' : 'Downloads are limited to 2 GiB each.'}</Text>
  </Stack>
}
