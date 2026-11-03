import { lazy, Suspense, useCallback, useEffect, useRef, useState } from 'react'
import { Alert, Badge, Button, Code, Group, Loader, Modal, Stack, Table, Text } from '@mantine/core'
import { formatBytes } from './container-utils'
import { childPath, fileRequest, parentPath, textBytes, type FileListing, type StorageTarget, type TextFile } from './mount-file-utils'

const TextEditor = lazy(() => import('./mount-text-editor'))
type Props = { hostId: string; target: StorageTarget; label: string; onClose: () => void }

export function MountFiles({ hostId, target, label, onClose }: Props) {
  const [folder, setFolder] = useState('')
  const [listing, setListing] = useState<FileListing | null>(null)
  const [opened, setOpened] = useState<{ path: string; file: TextFile } | null>(null)
  const [draft, setDraft] = useState('')
  const [editing, setEditing] = useState(false)
  const [compare, setCompare] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [saved, setSaved] = useState(false)
  const request = useRef<AbortController | null>(null)
  const dirty = opened !== null && draft !== opened.file.content
  const bytes = textBytes(draft)
  const { kind, id, mount } = target

  const load = useCallback(async (path: string) => {
    request.current?.abort()
    const controller = new AbortController()
    request.current = controller
    setBusy(true); setError('')
    try {
      const result = await fileRequest<FileListing>(hostId, 'list', { kind, id, mount }, path, undefined, controller.signal)
      if (!controller.signal.aborted) { setListing(result); setFolder(path) }
    } catch (cause) { if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : 'The folder could not be loaded.') }
    finally { if (!controller.signal.aborted) setBusy(false) }
  }, [hostId, kind, id, mount])

  useEffect(() => { void load(''); return () => { request.current?.abort() } }, [load])
  useEffect(() => {
    if (!dirty) return
    const warn = (event: BeforeUnloadEvent) => { event.preventDefault() }
    window.addEventListener('beforeunload', warn)
    return () => window.removeEventListener('beforeunload', warn)
  }, [dirty])

  const discard = () => !dirty || window.confirm('Discard your unsaved changes?')
  const close = () => { if (!busy && discard()) onClose() }

  async function open(path: string) {
    if (!discard()) return
    setBusy(true); setError(''); setSaved(false)
    const controller = new AbortController(); request.current = controller
    try {
      const file = await fileRequest<TextFile>(hostId, 'read', target, path, undefined, controller.signal)
      if (!controller.signal.aborted) { setOpened({ path, file }); setDraft(file.content); setEditing(false); setCompare(false) }
    } catch (cause) { if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : 'The file could not be opened.') }
    finally { if (!controller.signal.aborted) setBusy(false) }
  }

  async function save() {
    if (!opened || !dirty || !compare || bytes > 65536 || busy) return
    setBusy(true); setError(''); setSaved(false)
    try {
      const file = await fileRequest<TextFile>(hostId, 'write', target, opened.path, { content: draft, version: opened.file.version })
      setOpened({ path: opened.path, file }); setDraft(file.content); setEditing(false); setCompare(false); setSaved(true)
    } catch (cause) { setError(cause instanceof Error ? cause.message : 'The file could not be saved.') }
    finally { setBusy(false) }
  }

  function download() {
    if (!opened) return
    const url = URL.createObjectURL(new Blob([opened.file.content], { type: 'text/plain;charset=utf-8' }))
    const link = document.createElement('a'); link.href = url; link.download = opened.path.split('/').pop() || 'file.txt'; link.click()
    window.setTimeout(() => URL.revokeObjectURL(url), 1000)
  }

  return <Modal opened onClose={close} title={`Files · ${label}`} size="90vw" closeOnClickOutside={false} closeOnEscape={!busy} withCloseButton={!busy}>
    <Stack gap="sm">
      <Text size="sm" c="dimmed">{listing?.writable ? 'Browse and edit text files up to 64 KiB. Saving backs up the previous version on the host.' : 'Browse text files up to 64 KiB. Editing requires file write access on the host agent.'}</Text>
      {error && <Alert color="red" title="The request could not be completed">{error}</Alert>}
      {saved && <Alert color="green">Saved. The previous version was backed up on the host. Reload the service when you are ready to apply the configuration.</Alert>}
      {busy && <Group><Loader size="xs" /><Text size="sm">Working…</Text></Group>}
      {opened ? <>
        <Group justify="space-between"><Code>{opened.path}</Code>{dirty && <Badge color="orange">Unsaved changes</Badge>}</Group>
        <Group>
          <Button variant="default" size="xs" disabled={busy} onClick={() => { if (discard()) { setOpened(null); setSaved(false); setError(''); void load(folder) } }}>← Folder</Button>
          <Button variant="default" size="xs" disabled={busy} onClick={() => void open(opened.path)}>Reopen saved file</Button>
          <Button variant="default" size="xs" disabled={busy} onClick={download}>Download saved file</Button>
          {opened.file.writable && !editing && <Button size="xs" disabled={busy} onClick={() => setEditing(true)}>Edit text</Button>}
          {editing && <><Button variant="default" size="xs" disabled={busy || !dirty} onClick={() => setCompare((value) => !value)}>{compare ? 'Back to editor' : 'Review changes'}</Button>
            <Button size="xs" disabled={busy || !dirty || !compare || bytes > 65536} onClick={() => void save()}>Save file</Button></>}
        </Group>
        {compare && <Text size="xs" fw={600}>Saved version ↔ Your changes</Text>}
        <Suspense fallback={<Loader size="sm" />}><TextEditor name={opened.path} value={draft} original={opened.file.content} compare={compare} readOnly={!editing || busy} onChange={setDraft} /></Suspense>
        <Text size="xs" c={bytes > 65536 ? 'red' : 'dimmed'}>{formatBytes(bytes)} / 64 KiB{editing && ' · Review changes to enable saving.'}</Text>
      </> : <>
        <Group justify="space-between"><Code>{folder || '/'}</Code><Group gap="xs">
          {folder && !listing?.singleFile && <Button size="xs" variant="default" disabled={busy} onClick={() => void load(parentPath(folder))}>Up one folder</Button>}
          <Button size="xs" variant="default" disabled={busy} onClick={() => void load(folder)}>Refresh</Button>
        </Group></Group>
        {listing && <Table.ScrollContainer minWidth={400}><Table><Table.Thead><Table.Tr><Table.Th>Name</Table.Th><Table.Th>Type</Table.Th><Table.Th>Size</Table.Th><Table.Th /></Table.Tr></Table.Thead>
          <Table.Tbody>{listing.entries.map((entry) => <Table.Tr key={entry.name}>
            <Table.Td style={{ wordBreak: 'break-all' }}>{entry.name}</Table.Td><Table.Td>{entry.kind}</Table.Td><Table.Td>{entry.kind === 'file' ? formatBytes(entry.size) : '–'}</Table.Td>
            <Table.Td>{(entry.kind === 'directory' || entry.kind === 'file') && <Button variant="subtle" size="xs" disabled={busy || entry.kind === 'file' && entry.size > 65536} onClick={() => { const path = childPath(folder, entry.name); if (entry.kind === 'directory') void load(path); else void open(path) }}>{entry.kind === 'directory' ? 'Open folder' : 'Open text'}</Button>}</Table.Td>
          </Table.Tr>)}</Table.Tbody></Table></Table.ScrollContainer>}
        {listing?.entries.length === 0 && <Text c="dimmed">This folder is empty.</Text>}
        {listing?.truncated && <Alert color="yellow">Showing the first 500 entries. Open a subfolder to narrow the listing.</Alert>}
        <Text size="xs" c="dimmed">Symlinks, special files, binary files and files over 64 KiB cannot be opened.</Text>
      </>}
    </Stack>
  </Modal>
}
