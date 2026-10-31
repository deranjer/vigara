import { useEffect, useRef, useState } from 'react'
import { Alert, Button, Group, Stack, Text, TextInput } from '@mantine/core'
import { addBackupPath, maxBackupPaths } from './backup-path-utils'

import { LocalFolderBrowse } from './local-folder-browse'
type Props = { paths: string[]; onChange: (paths: string[]) => void; disabled?: boolean; onPendingChange?: (pending: boolean) => void }

export function BackupPathInput({ paths, onChange, disabled = false, onPendingChange }: Props) {
  const [draft, setDraft] = useState('')
  const [error, setError] = useState('')
  const [selectedFolder, setSelectedFolder] = useState('')
  const pathInput = useRef<HTMLInputElement>(null)
  const unavailable = disabled || paths.length >= maxBackupPaths

  useEffect(() => { onPendingChange?.(draft.trim().length > 0) }, [draft, onPendingChange])

  function add() {
    if (unavailable) return
    const result = addBackupPath(paths, draft)
    if (result.error) { setError(result.error); pathInput.current?.focus(); return }
    onChange(result.paths); setDraft(''); setError(''); setSelectedFolder('')
    pathInput.current?.focus()
  }

  function selectFolder(name: string) {
    setSelectedFolder(name); setDraft(name); setError('')
    pathInput.current?.focus()
  }

  return <Stack gap="xs">
    <TextInput ref={pathInput} label="Folders to back up" description="Enter a full path on the backup device, then add it to the list." placeholder="C:\Users\Alex\Documents or /home/alex/Documents" value={draft} onChange={(e) => { setDraft(e.currentTarget.value); setError('') }} onKeyDown={(e) => { if (e.key === 'Enter' && !e.nativeEvent.isComposing) { e.preventDefault(); add() } }} disabled={unavailable} error={error || undefined} />
    <Group gap="xs"><Button type="button" onClick={add} disabled={unavailable || !draft.trim()}>Add path</Button><LocalFolderBrowse label="Browse local folders for backup sources" onSelect={selectFolder} disabled={unavailable} />
      {draft && <Button type="button" variant="subtle" disabled={disabled} onClick={() => { setDraft(''); setSelectedFolder(''); setError('') }}>Clear input</Button>}</Group>
    <Text size="xs" c="dimmed">Browse selects a folder on the computer running this browser. Browsers hide full paths, so you will need to confirm the path. File contents are never read or uploaded.</Text>
    {selectedFolder && <Alert color="blue">Selected “{selectedFolder}”. Enter its full path on the backup device above, then choose Add path.</Alert>}
    {paths.length ? <ul aria-label="Backup folders" style={{ listStyle: 'none', padding: 0, margin: 0 }}>
      {paths.map((path, index) => <li key={`${index}-${path}`}><Group justify="space-between" wrap="nowrap" gap="sm" py={4}><Text size="sm" style={{ overflowWrap: 'anywhere', minWidth: 0 }}>{path}</Text><Button type="button" size="compact-xs" variant="subtle" color="red" aria-label={`Remove ${path}`} disabled={disabled} onClick={() => onChange(paths.filter((_, i) => i !== index))}>Remove</Button></Group></li>)}
    </ul> : <Text size="sm" c="dimmed">No folders added yet.</Text>}
    <Text size="xs" c="dimmed">{paths.length}/{maxBackupPaths} folders{draft.trim() ? ' · Choose Add path or clear the input before saving.' : ''}</Text>
  </Stack>
}
