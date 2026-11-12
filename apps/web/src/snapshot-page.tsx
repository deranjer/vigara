import { lazy, Suspense, useEffect, useState } from 'react'
import { Alert, Button, Group, Loader, Select, Stack, Table, Text, Title, UnstyledButton } from '@mantine/core'
import { IconFile, IconFolder } from '@tabler/icons-react'
import { looksBinary, pathSteps, previewLimit } from './backup-utils'
import { formatBytes } from './container-utils'
import { route, type Route } from './route'
import { Panel } from './ui'

const TextViewer = lazy(() => import('./mount-text-editor'))

type Snapshot = { id: string; time: string; hostname?: string; paths: string[] }
type Entry = { name: string; type: string; path: string; size?: number }
type DeviceRef = { id: string; name: string }
type Preview = { entry: Entry; state: 'loading' } | { entry: Entry; state: 'text'; text: string } | { entry: Entry; state: 'binary' | 'large' | 'error'; message?: string }

async function read<T>(url: string): Promise<T> {
  const response = await fetch(url, { credentials: 'same-origin' })
  if (!response.ok) throw new Error((await response.text()).trim().slice(0, 300) || `Request failed (${response.status}).`)
  return await response.json() as T
}

/** Reads at most `limit` bytes of a download, stopping the transfer once it has enough. */
async function readHead(url: string, limit: number, signal: AbortSignal): Promise<{ bytes: Uint8Array; truncated: boolean }> {
  const response = await fetch(url, { credentials: 'same-origin', signal })
  if (!response.ok || !response.body) throw new Error((await response.text()).trim().slice(0, 300) || 'The file could not be read.')
  const reader = response.body.getReader()
  const chunks: Uint8Array[] = []
  let size = 0
  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    chunks.push(value); size += value.length
    if (size > limit) { void reader.cancel(); break }
  }
  const bytes = new Uint8Array(Math.min(size, limit))
  let offset = 0
  for (const chunk of chunks) { const part = chunk.subarray(0, bytes.length - offset); bytes.set(part, offset); offset += part.length; if (offset >= bytes.length) break }
  return { bytes, truncated: size > limit }
}

const fmtTime = (at: string) => new Date(at).toLocaleString(undefined, { day: 'numeric', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit' })

/**
 * One snapshot of a device's backup, as a page: folders to walk, files to download, and a
 * read-only viewer for text files. `snapshotId` may be "latest".
 */
export function SnapshotPage({ deviceId, snapshotId, onNavigate }: { deviceId: string; snapshotId: string; onNavigate: (route: Route) => void }) {
  const base = `/api/backups/devices/${encodeURIComponent(deviceId)}`
  const [snapshots, setSnapshots] = useState<Snapshot[] | null>(null)
  const [deviceName, setDeviceName] = useState('')
  const [directory, setDirectory] = useState('/')
  const [entries, setEntries] = useState<Entry[] | null>(null)
  const [preview, setPreview] = useState<Preview | null>(null)
  const [error, setError] = useState('')

  useEffect(() => {
    let active = true
    void read<Snapshot[]>(`${base}/snapshots`).then((list) => { if (active) setSnapshots([...list].sort((a, b) => b.time.localeCompare(a.time))) })
      .catch((e: unknown) => { if (active) { setSnapshots([]); setError(e instanceof Error ? e.message : 'Snapshots could not be loaded.') } })
    void read<{ devices: DeviceRef[] }>('/api/inventory/devices?limit=100&offset=0').then((v) => { if (active) setDeviceName(v.devices.find((d) => d.id === deviceId)?.name ?? '') }).catch(() => undefined)
    return () => { active = false }
  }, [base, deviceId])

  const snapshot = snapshots && (snapshotId === 'latest' ? snapshots[0] : snapshots.find((s) => s.id === snapshotId || s.id.startsWith(snapshotId)))
  const snap = snapshot?.id

  // A new snapshot starts at its top folder.
  useEffect(() => { setDirectory('/'); setPreview(null) }, [snap])

  useEffect(() => {
    if (!snap) return
    let active = true
    setEntries(null); setError('')
    void read<Entry[]>(`${base}/snapshots/${snap}/entries?path=${encodeURIComponent(directory)}`)
      .then((list) => { if (active) setEntries([...list].sort((a, b) => (a.type === 'dir' ? 0 : 1) - (b.type === 'dir' ? 0 : 1) || a.name.localeCompare(b.name))) })
      .catch((e: unknown) => { if (active) { setEntries([]); setError(e instanceof Error ? e.message : 'This folder could not be read.') } })
    return () => { active = false }
  }, [base, snap, directory])

  const download = (path: string) => `${base}/snapshots/${snap}/download?path=${encodeURIComponent(path)}`
  const previewing = preview?.entry.path
  useEffect(() => {
    if (!preview || preview.state !== 'loading' || !snap) return
    const controller = new AbortController()
    const { entry } = preview
    void readHead(`${base}/snapshots/${snap}/download?path=${encodeURIComponent(entry.path)}`, previewLimit, controller.signal).then(({ bytes, truncated }) => {
      if (truncated) setPreview({ entry, state: 'large' })
      else if (looksBinary(bytes)) setPreview({ entry, state: 'binary' })
      else setPreview({ entry, state: 'text', text: new TextDecoder().decode(bytes) })
    }).catch((e: unknown) => { if (!controller.signal.aborted) setPreview({ entry, state: 'error', message: e instanceof Error ? e.message : 'The file could not be read.' }) })
    return () => controller.abort()
  }, [previewing, snap, base]) // only a newly chosen file starts a read

  const open = (entry: Entry) => {
    if (entry.type === 'dir') { setDirectory(entry.path); setPreview(null); return }
    if (entry.type !== 'file') return
    setPreview((entry.size ?? 0) > previewLimit ? { entry, state: 'large' } : { entry, state: 'loading' })
  }

  const title = deviceName ? `${deviceName} backup` : 'Backup'
  if (snapshots && !snapshot) return <Stack gap="md">
    <Title order={1}>{title}</Title>
    {error ? <Alert color="red">{error}</Alert> : <Text c="dimmed">{snapshots.length ? 'This snapshot no longer exists. It may have been removed by the retention policy.' : 'No snapshots yet.'}</Text>}
    <Group>{snapshots.length > 0 && <Button variant="light" onClick={() => onNavigate(route('backups', deviceId, 'latest'))}>Open the latest snapshot</Button>}<Button variant="default" onClick={() => onNavigate(route('backups'))}>All backups</Button></Group>
  </Stack>

  return <Stack gap="md">
    <Group justify="space-between" align="flex-end" wrap="wrap">
      <div>
        <Title order={1}>{title}</Title>
        <Text c="dimmed" size="sm">{snapshot ? `Snapshot ${fmtTime(snapshot.time)}${snapshot.hostname ? ` · ${snapshot.hostname}` : ''} · ${snapshot.id.slice(0, 8)}` : 'Loading snapshot…'} · read-only, nothing is restored</Text>
      </div>
      {snapshots && snapshots.length > 1 && <Select aria-label="Snapshot" w={260} size="sm" value={snap ?? null} allowDeselect={false}
        data={snapshots.map((s, i) => ({ value: s.id, label: `${fmtTime(s.time)}${i === 0 ? ' (latest)' : ''}` }))}
        onChange={(v) => { if (v) onNavigate(route('backups', deviceId, v)) }} />}
    </Group>
    {error && <Alert color="red">{error}</Alert>}

    <div className={preview ? 'vg-snapshot vg-snapshot-open' : 'vg-snapshot'}>
      <Panel flush title={<nav className="vg-path" aria-label="Folder">{pathSteps(directory).map((step, i, all) => <span key={step.path}>
        {i > 1 && <span className="vg-crumb-sep" aria-hidden="true">/</span>}
        {i === all.length - 1 ? <b>{step.name}</b> : <a href="#" onClick={(e) => { e.preventDefault(); setDirectory(step.path); setPreview(null) }}>{step.name}</a>}
      </span>)}</nav>} actions={snap && <Button size="xs" variant="default" component="a" href={download(directory)}>Download folder as ZIP</Button>}>
        {!entries ? <Group justify="center" p="lg"><Loader size="sm" /></Group>
          : !entries.length ? <Text size="sm" c="dimmed" p="md">This folder is empty.</Text>
          : <div className="table-scroll"><Table className="vg-table" verticalSpacing={6}><Table.Tbody>
            {entries.map((entry) => <Table.Tr key={entry.path} className={entry.path === previewing ? 'vg-row-selected' : undefined}>
              <Table.Td><UnstyledButton className="vg-entry" onClick={() => open(entry)} disabled={!['dir', 'file'].includes(entry.type)}>
                {entry.type === 'dir' ? <IconFolder size={16} aria-hidden="true" /> : <IconFile size={16} aria-hidden="true" />}<span>{entry.name}</span>
              </UnstyledButton></Table.Td>
              <Table.Td className="vg-num"><Text size="sm" c="dimmed">{entry.type === 'file' ? formatBytes(entry.size ?? 0) : entry.type === 'dir' ? 'Folder' : entry.type}</Text></Table.Td>
              <Table.Td className="vg-num">{['file', 'dir'].includes(entry.type) && <Button component="a" href={download(entry.path)} variant="subtle" size="xs" aria-label={`Download ${entry.name}`}>{entry.type === 'dir' ? 'ZIP' : 'Download'}</Button>}</Table.Td>
            </Table.Tr>)}
          </Table.Tbody></Table></div>}
      </Panel>

      {preview && <Panel title={<span className="vg-ellipsis" title={preview.entry.path}>{preview.entry.name}</span>} actions={<Group gap="xs">
        <Button size="xs" variant="default" component="a" href={download(preview.entry.path)}>Download</Button>
        <Button size="xs" variant="subtle" onClick={() => setPreview(null)}>Close</Button>
      </Group>}>
        {preview.state === 'loading' ? <Group justify="center" p="lg"><Loader size="sm" /></Group>
          : preview.state === 'text' ? <Suspense fallback={<Loader size="sm" />}><TextViewer name={preview.entry.name} value={preview.text} original="" compare={false} readOnly onChange={() => undefined} /></Suspense>
          : <Text size="sm" c="dimmed">{preview.state === 'large' ? `This file is ${formatBytes(preview.entry.size ?? 0)}; only text files up to ${formatBytes(previewLimit)} open here. Download it to view.`
            : preview.state === 'binary' ? 'This looks like a binary file, so it can\'t be shown as text. Download it to open it.'
            : preview.message}</Text>}
      </Panel>}
    </div>
    <Text size="xs" c="dimmed">Downloads are limited to 2 GiB each.</Text>
  </Stack>
}
