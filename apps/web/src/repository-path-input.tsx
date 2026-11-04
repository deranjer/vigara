import { useState } from 'react'
import { Alert, Stack, Text, TextInput } from '@mantine/core'
import { LocalFolderBrowse } from './local-folder-browse'
import { isAbsoluteBackupPath } from './backup-path-utils'

export function RepositoryPathInput({ value, onChange, disabled = false }: { value: string; onChange: (value: string) => void; disabled?: boolean }) {
  const [selected, setSelected] = useState('')
  const needsPath = !!selected && !isAbsoluteBackupPath(value)
  return <Stack gap="xs">
    <TextInput label="Repository path on the device" description="A dedicated local folder or network share for this device's backups." value={value} onChange={(e) => { onChange(e.currentTarget.value); if (isAbsoluteBackupPath(e.currentTarget.value)) setSelected('') }} disabled={disabled} required error={needsPath ? 'Enter the full repository path on the backup device.' : undefined} />
    <LocalFolderBrowse label="Browse local folders for the device repository" disabled={disabled} onSelect={(name) => { setSelected(name); onChange(name) }} />
    {!disabled && <Text size="xs" c="dimmed">Browse opens folders on the computer running this browser. Enter the full path after selecting a folder; file contents are never read or uploaded.</Text>}
    {needsPath && <Alert color="blue">Selected “{selected}”. Your browser hides its full path. Enter the absolute repository path above before saving.</Alert>}
  </Stack>
}
